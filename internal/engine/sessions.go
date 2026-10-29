package engine

import (
	"context"
	"slices"
	"time"

	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
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
