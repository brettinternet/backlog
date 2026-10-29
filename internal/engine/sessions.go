package engine

import (
	"cmp"
	"context"
	"crypto/rand"
	"slices"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

// ItemSessions lists the sessions that worked an item: its events grouped by
// session ID, joined with its claim history (design.md D40). A failed event
// read comes back as a message beside the claim-only sessions.
func ItemSessions(ctx context.Context, cfg project.Config, item model.Item, claims []model.ClaimEpoch) ([]model.ItemSession, string) {
	var events []hooks.Event
	message := ""
	if cfg.Paths.StateDir != "" {
		var err error
		if events, err = hooks.New(cfg.Paths.StateDir, cfg.Project.ID).ItemEvents(ctx, refString(item.Ref)); err != nil {
			message = "event history unavailable: " + err.Error()
		}
	}
	return foldSessions(events, claims, time.Now()), message
}

func foldSessions(events []hooks.Event, claims []model.ClaimEpoch, now time.Time) []model.ItemSession {
	var order []string
	byID := map[string]*model.ItemSession{}
	done, released := map[string]bool{}, map[string]bool{}
	row := func(id string, at time.Time) *model.ItemSession {
		s := byID[id]
		if s == nil {
			s = &model.ItemSession{ID: id, FirstSeen: at, LastSeen: at}
			byID[id] = s
			order = append(order, id)
		}
		if at.Before(s.FirstSeen) {
			s.FirstSeen = at
		}
		if at.After(s.LastSeen) {
			s.LastSeen = at
		}
		return s
	}
	for _, e := range events {
		if e.Session == nil || e.Session.ID == "" {
			continue
		}
		s := row(e.Session.ID, e.Time)
		// The latest event that knows a value wins, so a session that moved
		// checkouts shows where it was last.
		for _, v := range [][2]*string{{&s.Agent, &e.Session.Agent}, {&s.AgentSession, &e.Session.AgentSession}, {&s.Host, &e.Session.Host}, {&s.CWD, &e.Session.CWD}, {&s.Branch, &e.Session.Branch}} {
			if *v[1] != "" {
				*v[0] = *v[1]
			}
		}
		category := ""
		if item, ok := e.Item.(map[string]any); ok {
			category, _ = item["category"].(string)
		}
		if category != "" && !slices.Contains(s.Phases, category) {
			s.Phases = append(s.Phases, category)
		}
		// Done means this session closed the item, not that it touched a
		// closed one.
		closed := e.Changes["category"].To
		switch {
		case e.Type == "session.handoff":
			s.Handoff = e.Handoff
		case e.Type == "claim.released":
			released[s.ID] = true
		case closed == string(model.Done) || closed == string(model.Canceled):
			done[s.ID] = true
		}
	}
	for _, c := range claims {
		if c.Session == "" {
			continue
		}
		s := row(c.Session, c.AcquiredAt)
		if !c.EndedAt.IsZero() {
			row(c.Session, c.EndedAt)
		}
		if s.Claim == nil || !c.AcquiredAt.Before(s.Claim.AcquiredAt) {
			epoch := c
			s.Claim = &epoch
		}
	}
	out := make([]model.ItemSession, 0, len(order))
	for _, id := range order {
		s := byID[id]
		switch c := s.Claim; {
		case done[id]:
			s.Ended = "done"
		case c != nil && c.EndReason == "released":
			s.Ended = "released"
		case c != nil && (!c.EndedAt.IsZero() || !c.ExpiresAt.IsZero() && c.ExpiresAt.Before(now)):
			s.Ended = "lapsed"
		case c == nil && released[id]:
			s.Ended = "released"
		}
		out = append(out, *s)
	}
	slices.SortStableFunc(out, func(a, b model.ItemSession) int { return a.FirstSeen.Compare(b.FirstSeen) })
	return out
}

// HandoffLimit bounds a handoff's trimmed text in bytes. Longer text is
// refused rather than truncated (design.md D40).
const HandoffLimit = 4 << 10

// HandoffResult reports a recorded handoff. Comment is posted or unsupported
// under history.handoff: comment and empty when the handoff stays local; Item
// is the commented item.
type HandoffResult struct {
	Event   string
	Comment string
	Item    model.Item
}

// RecordHandoff appends a session.handoff event for item under the caller's
// session, which needs no claim. Under history.handoff: comment it first posts
// the text as a comment at version, naming only the agent kind and date, so a
// refused comment records nothing. An unknown-outcome comment still records
// the handoff and returns the comment's error.
func RecordHandoff(ctx context.Context, cfg project.Config, item model.Item, version, text string) (HandoffResult, error) {
	_, caller := Coordination(ctx)
	if strings.TrimSpace(caller.Session) == "" {
		return HandoffResult{}, source.Refuse("session-required", "handoff requires an explicit session")
	}
	if err := RequireSession(caller); err != nil {
		return HandoffResult{}, err
	}
	if cfg.Paths.StateDir == "" {
		return HandoffResult{}, source.Refuse("unavailable", "handoffs need a state directory for the event log")
	}
	result := HandoffResult{Item: item}
	var commentErr error
	if cfg.History.Handoff == "comment" {
		backend, err := writeBackend(ctx, cfg, item.Ref.Source)
		if err != nil {
			return HandoffResult{}, err
		}
		result.Comment = "unsupported"
		if backend.Info().Capabilities.Allows(model.AppendComment) {
			author, err := CommentAuthor(cfg, item.Ref.Source, "")
			if err != nil {
				return HandoffResult{}, err
			}
			now := time.Now()
			header := "Handoff from " + cmp.Or(caller.Agent, "an agent") + " on " + now.Format(time.DateOnly)
			comment := model.Comment{Author: author, Body: header + "\n\n" + text, Time: now}
			updated, err := ApplyWrite(ctx, cfg, model.Mutation{Op: model.AppendComment, Ref: item.Ref, ExpectedVersion: version, Comment: comment})
			if err != nil && ErrorReason(err) != "unknown-outcome" {
				return HandoffResult{}, err
			}
			result.Comment, commentErr = "posted", err
			if err == nil {
				result.Item = updated
			}
		}
	}
	origin := "cli"
	if e, ok := ctx.Value(eventContextKey{}).(eventContext); ok {
		origin = e.Origin
	}
	e := makeEvent(ctx, cfg, result.Item, "session.handoff", origin, []string{rand.Text()})
	e.Handoff = text
	err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Update(ctx, func(tx *hooks.Transaction) error {
		if _, err := tx.Append(e); err != nil {
			return err
		}
		result.Event = tx.Appended()[0].ID
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, commentErr
}
