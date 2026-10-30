package history

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/contact"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/textutil"
)

// Load composes item's history from the project that stores it. coordinator
// is cfg's claim system, or nil when it has none; linked loads the item's
// changes.
func Load(ctx context.Context, cfg project.Config, coordinator model.Coordinator, item model.Item, linked func(context.Context) ([]changes.Change, error)) Result {
	if cfg.Workspace != nil {
		p, local := cfg.SourceProject(item.Ref.Source)
		if p.Workspace != nil {
			return Result{Providers: []Status{{Provider: "events", State: Unavailable, Reason: "unknown workspace source " + item.Ref.Source}}}
		}
		cfg, item.Ref.Source, coordinator = p, local, engine.NewCoordinator(p)
	}
	var events []hooks.Event
	var claims []model.ClaimEpoch
	var claimsErr string
	eventsDone, claimsDone := make(chan struct{}), make(chan struct{})
	providers := []Provider{
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
			if err == nil {
				claims, err = coordinator.History(ctx, resource)
			}
			if err != nil {
				claimsErr = err.Error()
				return nil, err
			}
			return claimEntries(claims, time.Now()), nil
		}},
		Provider{Name: "sessions", Load: func(ctx context.Context) ([]Entry, error) {
			<-eventsDone
			<-claimsDone
			sessions := engine.SessionsFrom(ctx, cfg, events, claims)
			return sessionEntries(sessions, engine.ResumeRefusals(cfg, coordinator, sessions, claimsErr)), nil
		}},
		Provider{Name: "comments", Load: func(context.Context) ([]Entry, error) { return commentEntries(item.Comments), nil }},
		// Before git, so a commit both report nests under its change.
		Provider{Name: "changes", Load: func(ctx context.Context) ([]Entry, error) {
			cs, err := linked(ctx)
			return changeEntries(cs), err
		}},
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
	}
	if p, ok := activityProvider(cfg, item); ok {
		providers = append(providers, p)
	}
	return Compose(ctx, providers...)
}

// activityProvider reads the activity a remote provider recorded, when item's
// source has the read-activity capability. Its entries carry the provider's
// name, beside local events for the same change.
func activityProvider(cfg project.Config, item model.Item) (Provider, bool) {
	reader, kind, ok, err := engine.ActivityReader(cfg, item.Ref)
	if err != nil {
		return Provider{Name: cmp.Or(kind, "activity"), Load: func(context.Context) ([]Entry, error) { return nil, err }}, true
	}
	if !ok {
		return Provider{}, false
	}
	return Provider{Name: kind, Load: func(ctx context.Context) ([]Entry, error) {
		activity, truncated, err := reader.Activity(ctx, item)
		if err != nil {
			return nil, err
		}
		entries := make([]Entry, 0, len(activity))
		for _, a := range activity {
			entries = append(entries, Entry{ID: kind + ":" + a.ID, Time: a.Time, Kind: "activity", Actor: textutil.Line(a.Actor), Summary: textutil.Line(a.Summary), Detail: textutil.Sanitize(a.Detail)})
		}
		if truncated {
			return entries, PartialError(fmt.Errorf("showing %d entries", len(entries)))
		}
		return entries, nil
	}}, true
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
			entry.Summary, entry.Detail = changeEntry(e.Changes)
		case "item.readiness":
			c := e.Changes["readiness"]
			entry.Summary = fmt.Sprintf("readiness %v → %v", c.From, c.To)
			if c.Reason != nil {
				entry.Detail = textutil.Line(display(c.Reason))
			}
		case "claim.acquired", "claim.released":
			if len(e.Versions) > 0 {
				entry.ID = "claim:" + e.Versions[0] + ":" + strings.TrimPrefix(e.Type, "claim.")
			}
			entry.Summary = strings.TrimPrefix(e.Type, "claim.")
		case "hook.finished", "action.finished":
			entry.Summary = e.Name + " " + e.Outcome
			entry.Detail = e.Message
		case "session.handoff":
			entry.Summary, entry.Detail = firstLine(e.Handoff), e.Handoff
		}
		out = append(out, entry)
	}
	return out
}

func claimEntries(claims []model.ClaimEpoch, now time.Time) []Entry {
	var out []Entry
	for _, c := range claims {
		base := Entry{Kind: "claim.acquired", Actor: c.Agent, Session: c.Session}
		acquired := base
		acquired.ID, acquired.Time, acquired.Summary = "claim:"+c.ID+":acquired", c.AcquiredAt, "acquired"
		out = append(out, acquired)
		ended := base
		ended.ID, ended.Kind = "claim:"+c.ID+":released", "claim.released"
		switch {
		case !c.EndedAt.IsZero():
			ended.Time, ended.Summary = c.EndedAt, cmp.Or(c.EndReason, "ended")
		case !c.ExpiresAt.IsZero() && c.ExpiresAt.Before(now):
			ended.Time, ended.Summary = c.ExpiresAt, "lapsed"
		default:
			continue
		}
		out = append(out, ended)
	}
	return out
}

// sessionEntries makes one row per session, labelled resumable or with the
// check in refused, by session ID, that refuses reopening it.
func sessionEntries(sessions []model.ItemSession, refused map[string]string) []Entry {
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
		// A session that ends in the category it reached says so once.
		if len(s.Phases) == 0 || s.Phases[len(s.Phases)-1] != span.State {
			summary = append(summary, span.State)
		}
		resumable := false
		switch reason := refused[s.ID]; {
		case strings.HasPrefix(s.ID, "tui:") || s.Agent == "" && s.AgentSession == "":
		case reason != "":
			summary = append(summary, strings.ReplaceAll(reason, "-", " "))
		default:
			resumable = true
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
		agentSession := s.AgentSession
		if agentSession == s.ID {
			agentSession = ""
		}
		// Values align after the longest key, so the fields scan as a table.
		for _, field := range [][2]string{{"session", s.ID}, {"agent session", agentSession}, {"host", s.Host}, {"cwd", s.CWD}, {"branch", s.Branch}, {"contact", reach}} {
			if field[1] != "" {
				detail = append(detail, fmt.Sprintf("%-14s %s", field[0]+":", field[1]))
			}
		}
		if s.Handoff != "" {
			detail = append(detail, "handoff:\n"+s.Handoff)
		}
		out = append(out, Entry{ID: sessionID(s.ID), Time: s.FirstSeen, Kind: "session", Actor: s.Agent, Session: s.ID, Span: span, Resumable: resumable, Summary: strings.Join(summary, " · "), Detail: strings.Join(detail, "\n")})
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
