package history

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/contact"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/textutil"
)

// Load composes item's history from the project that stores it. coordinator
// is cfg's claim system, or nil when it has none.
func Load(ctx context.Context, cfg project.Config, coordinator model.Coordinator, item model.Item) Result {
	if cfg.Workspace != nil {
		p, local := cfg.SourceProject(item.Ref.Source)
		if p.Workspace != nil {
			return Result{Providers: []Status{{Provider: "events", State: Unavailable, Reason: "unknown workspace source " + item.Ref.Source}}}
		}
		cfg, item.Ref.Source, coordinator = p, local, engine.NewCoordinator(p)
	}
	var events []hooks.Event
	var claims []model.ClaimEpoch
	eventsDone, claimsDone := make(chan struct{}), make(chan struct{})
	return Compose(ctx,
		Provider{Name: "events", Load: func(ctx context.Context) ([]Entry, error) {
			defer close(eventsDone)
			if cfg.Paths.StateDir == "" {
				return nil, errors.New("no event log")
			}
			var err error
			events, err = hooks.New(cfg.Paths.StateDir, cfg.Project.ID).ItemEvents(ctx, item.Ref.Source+":"+item.Ref.ID)
			return eventEntries(events), err
		}},
		Provider{Name: "claims", Load: func(ctx context.Context) ([]Entry, error) {
			defer close(claimsDone)
			if coordinator == nil {
				return nil, errors.New("coordination not configured")
			}
			resource, err := coordinator.Resource(item)
			if err != nil {
				return nil, err
			}
			if claims, err = coordinator.History(ctx, resource); err != nil {
				return nil, err
			}
			return claimEntries(claims, time.Now()), nil
		}},
		Provider{Name: "sessions", Load: func(ctx context.Context) ([]Entry, error) {
			<-eventsDone
			<-claimsDone
			return sessionEntries(engine.SessionsFrom(ctx, cfg, events, claims)), nil
		}},
		Provider{Name: "comments", Load: func(context.Context) ([]Entry, error) { return commentEntries(item.Comments), nil }},
		Provider{Name: "git", Load: func(ctx context.Context) ([]Entry, error) {
			path, ok, err := engine.ItemFile(ctx, cfg, item.Ref)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, errors.New("not a file source")
			}
			return gitHistory(ctx, cfg.Paths.CacheDir, item.Version, path)
		}},
	)
}

func eventEntries(events []hooks.Event) []Entry {
	out := make([]Entry, 0, len(events))
	for _, e := range events {
		entry := Entry{ID: "event:" + e.ID, Time: e.Time, Kind: e.Type, Actor: e.Actor, Summary: e.Type}
		if e.Session != nil {
			entry.Session = e.Session.ID
		}
		switch e.Type {
		case "item.created":
			entry.Summary = "created"
		case "item.archived":
			entry.Summary = "archived"
		case "item.updated":
			entry.Summary = changeSummary(e.Changes)
		case "item.readiness":
			c := e.Changes["readiness"]
			entry.Summary = fmt.Sprintf("readiness %v → %v", c.From, c.To)
			if c.Reason != nil {
				entry.Detail = fmt.Sprint(c.Reason)
			}
		case "claim.acquired", "claim.released":
			if len(e.Versions) > 0 {
				entry.ID = "claim:" + e.Versions[0] + ":" + strings.TrimPrefix(e.Type, "claim.")
			}
			entry.Summary = strings.Replace(e.Type, ".", " ", 1)
		case "hook.finished", "action.finished":
			entry.Summary = fmt.Sprintf("%s %s %s", strings.TrimSuffix(e.Type, ".finished"), e.Name, e.Outcome)
			entry.Detail = e.Message
		case "session.handoff":
			entry.Summary, entry.Detail = "handoff: "+firstLine(e.Handoff), e.Handoff
		}
		out = append(out, entry)
	}
	return out
}

func changeSummary(changes map[string]hooks.Change) string {
	if c, ok := changes["archived"]; ok && c.To == false {
		return "unarchived"
	}
	keys := make([]string, 0, len(changes))
	for k := range changes {
		// fields and updated change with every edit, so they say nothing.
		if k != "fields" && k != "updated" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		c := changes[k]
		from, to := changeValue(c.From), changeValue(c.To)
		if len(from)+len(to) > 60 {
			parts = append(parts, k+" changed")
		} else {
			parts = append(parts, fmt.Sprintf("%s %s → %s", k, from, to))
		}
	}
	return cmp.Or(strings.Join(parts, "; "), "updated")
}

func changeValue(v any) string {
	if s := textutil.Line(fmt.Sprint(v)); v != nil && s != "" {
		return s
	}
	return "∅"
}

func claimEntries(claims []model.ClaimEpoch, now time.Time) []Entry {
	var out []Entry
	for _, c := range claims {
		base := Entry{Kind: "claim.acquired", Actor: c.Agent, Session: c.Session}
		acquired := base
		acquired.ID, acquired.Time, acquired.Summary = "claim:"+c.ID+":acquired", c.AcquiredAt, "claim acquired"
		out = append(out, acquired)
		ended := base
		ended.ID, ended.Kind = "claim:"+c.ID+":released", "claim.released"
		switch {
		case !c.EndedAt.IsZero():
			ended.Time, ended.Summary = c.EndedAt, "claim "+cmp.Or(c.EndReason, "ended")
		case !c.ExpiresAt.IsZero() && c.ExpiresAt.Before(now):
			ended.Time, ended.Summary = c.ExpiresAt, "claim lapsed"
		default:
			continue
		}
		out = append(out, ended)
	}
	return out
}

func sessionEntries(sessions []model.ItemSession) []Entry {
	out := make([]Entry, 0, len(sessions))
	for _, s := range sessions {
		span := &Span{Start: s.FirstSeen, State: cmp.Or(s.Ended, "open")}
		if s.Ended != "" {
			span.End = s.LastSeen
		}
		summary := []string{cmp.Or(s.Agent, "session "+s.ID)}
		if len(s.Phases) > 0 {
			summary = append(summary, strings.Join(s.Phases, " → "))
		}
		if d := s.LastSeen.Sub(s.FirstSeen); d >= time.Minute {
			summary = append(summary, duration(d))
		}
		summary = append(summary, span.State)
		if s.AgentSession != "" {
			summary = append(summary, "resumable")
		}
		if s.Handoff != "" {
			summary = append(summary, "handoff: "+firstLine(s.Handoff))
		}
		var via []string
		for _, r := range s.Contact {
			for _, c := range r.Channels {
				via = append(via, c.Channel+" "+c.Handle)
			}
		}
		if len(via) > 0 {
			summary = append(summary, "via "+strings.Join(via, ", "))
		}
		reach := contact.Line(s.Contact)
		var detail []string
		for _, field := range [][2]string{{"session", s.ID}, {"agent session", s.AgentSession}, {"host", s.Host}, {"cwd", s.CWD}, {"branch", s.Branch}} {
			if field[1] != "" {
				detail = append(detail, field[0]+": "+field[1])
			}
		}
		if reach != "" {
			detail = append(detail, "contact: "+reach)
		}
		if s.Handoff != "" {
			detail = append(detail, "handoff:\n"+s.Handoff)
		}
		out = append(out, Entry{ID: sessionID(s.ID), Time: s.FirstSeen, Kind: "session", Actor: s.Agent, Session: s.ID, Span: span, Summary: strings.Join(summary, " · "), Detail: strings.Join(detail, "\n")})
	}
	return out
}

// duration is d in whole minutes, such as 1h05m or 12m.
func duration(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}

func commentEntries(comments []model.Comment) []Entry {
	out := make([]Entry, 0, len(comments))
	for i, c := range comments {
		entry := Entry{ID: fmt.Sprintf("comment:%d:%s", i, c.Time.UTC().Format(time.RFC3339Nano)), Time: c.Time, Kind: "comment", Actor: c.Author, Summary: firstLine(c.Body)}
		if entry.Summary != strings.TrimSpace(c.Body) {
			entry.Detail = c.Body
		}
		out = append(out, entry)
	}
	return out
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return textutil.Line(line)
}
