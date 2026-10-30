package linear

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

const historyQuery = `query($id:String!,$after:String){issue(id:$id){history(first:100,after:$after){nodes{id createdAt actor{name} fromState{name} toState{name} fromAssignee{name} toAssignee{name} addedLabels{name} removedLabels{name} fromPriority toPriority fromCycle{number name} toCycle{number name} fromParent{identifier} toParent{identifier}} pageInfo{hasNextPage endCursor}}}}`

type named struct{ Name string }
type historyRow struct {
	ID                         string
	CreatedAt                  time.Time
	Actor                      *named
	FromState, ToState         *named
	FromAssignee, ToAssignee   *named
	AddedLabels, RemovedLabels []named
	FromPriority, ToPriority   *float64
	FromCycle, ToCycle         *cycle
	FromParent, ToParent       *struct{ Identifier string }
}

// Activity reads an issue's history: state, assignee, label, priority, cycle,
// and parent changes. Other changes, such as description edits, are skipped.
func (s *Source) Activity(ctx context.Context, item model.Item) ([]model.Activity, bool, error) {
	id := item.Ref.ID
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n\x00") {
		return nil, false, source.Refuse("invalid-id", "Linear identifier required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.authenticate(ctx)
	if err != nil {
		return nil, false, err
	}
	vars := map[string]any{"id": id}
	var rows []historyRow
	seen := map[string]bool{}
	for {
		var r struct {
			Issue *struct {
				History struct {
					Nodes    []historyRow
					PageInfo *pageInfo
				}
			}
		}
		if err := s.query(ctx, c, historyQuery, vars, &r); err != nil {
			return nil, false, err
		}
		if r.Issue == nil {
			return nil, false, source.Refuse("not-found", "Linear issue unavailable")
		}
		rows = append(rows, r.Issue.History.Nodes...)
		if r.Issue.History.PageInfo == nil || !r.Issue.History.PageInfo.HasNextPage {
			break
		}
		if len(rows) >= source.ActivityLimit {
			return historyActivity(rows), true, nil
		}
		next, err := advance(r.Issue.History.PageInfo, seen)
		if err != nil {
			return nil, false, err
		}
		vars["after"] = next
	}
	return historyActivity(rows), false, nil
}

func historyActivity(rows []historyRow) []model.Activity {
	name := func(n *named) string {
		if n == nil || n.Name == "" {
			return "∅"
		}
		return n.Name
	}
	var out []model.Activity
	for _, r := range rows {
		var changes []string
		if r.FromState != nil || r.ToState != nil {
			changes = append(changes, "state "+name(r.FromState)+" → "+name(r.ToState))
		}
		if r.FromAssignee != nil || r.ToAssignee != nil {
			changes = append(changes, "assignee "+name(r.FromAssignee)+" → "+name(r.ToAssignee))
		}
		for _, l := range r.AddedLabels {
			changes = append(changes, "labeled "+l.Name)
		}
		for _, l := range r.RemovedLabels {
			changes = append(changes, "unlabeled "+l.Name)
		}
		if r.FromPriority != nil && r.ToPriority != nil && *r.FromPriority != *r.ToPriority {
			changes = append(changes, "priority "+priorityName(*r.FromPriority)+" → "+priorityName(*r.ToPriority))
		}
		if r.FromCycle != nil || r.ToCycle != nil {
			changes = append(changes, "cycle "+cycleName(r.FromCycle)+" → "+cycleName(r.ToCycle))
		}
		if r.FromParent != nil || r.ToParent != nil {
			from, to := "∅", "∅"
			if r.FromParent != nil {
				from = r.FromParent.Identifier
			}
			if r.ToParent != nil {
				to = r.ToParent.Identifier
			}
			changes = append(changes, "parent "+from+" → "+to)
		}
		if len(changes) == 0 || r.ID == "" || r.CreatedAt.IsZero() {
			continue
		}
		a := model.Activity{ID: r.ID, Time: r.CreatedAt, Summary: strings.Join(changes, "; ")}
		if r.Actor != nil {
			a.Actor = r.Actor.Name
		}
		out = append(out, a)
	}
	slices.SortStableFunc(out, func(a, b model.Activity) int { return a.Time.Compare(b.Time) })
	return out
}

func priorityName(p float64) string {
	if names := []string{"none", "urgent", "high", "medium", "low"}; p >= 0 && int(p) < len(names) && float64(int(p)) == p {
		return names[int(p)]
	}
	return fmt.Sprint(p)
}

func cycleName(c *cycle) string {
	if c == nil {
		return "∅"
	}
	return cmp.Or(c.Name, fmt.Sprintf("cycle %g", c.Number))
}
