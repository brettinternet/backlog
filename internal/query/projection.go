package query

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
)

// Project filters, sorts, and groups a snapshot without mutating it. Call it on
// a projection worker, never on a UI update loop. Validate before calling.
func (q Query) Project(s Snapshot, event *Event) []Group {
	groups, _ := q.ProjectContext(context.Background(), s, event)
	return groups
}

// ProjectContext cooperatively cancels filtering, sorting and grouping. A
// canceled projection never returns partially prepared rows.
func (q Query) ProjectContext(ctx context.Context, s Snapshot, event *Event) ([]Group, error) {
	// Keep matches as indices until their count is known. A selective filter
	// must not allocate a backlog-sized array of the much larger Item values.
	matches := make([]int, 0, len(s.Items))
	for n, i := range s.Items {
		if n%256 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if q.Match(i, s, event) {
			matches = append(matches, n)
		}
	}
	items := make([]model.Item, len(matches))
	for n, index := range matches {
		if n%256 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		items[n] = s.Items[index]
	}
	s.sortContext(ctx, items, q.Sort)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	groups := s.groupContext(ctx, items, q.Group)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return groups, nil
}

// Sort orders the supplied slice in place. The engine-provided next order is
// always the final tie-break before natural ID and source identity.
func (s Snapshot) Sort(items []model.Item, keys []SortKey) {
	s.sortContext(context.Background(), items, keys)
}
func (s Snapshot) sortContext(ctx context.Context, items []model.Item, keys []SortKey) {
	if len(items) < 2 {
		return
	}
	next := make(map[model.Ref]int, len(s.Next))
	for n, r := range s.Next {
		next[r] = n
	}
	nextCmp := func(a, b model.Item) int {
		ai, aok := next[a.Ref]
		bi, bok := next[b.Ref]
		if aok != bok {
			if aok {
				return -1
			}
			return 1
		}
		if aok {
			return cmp.Compare(ai, bi)
		}
		return 0
	}
	slices.SortFunc(items, func(a, b model.Item) int {
		if ctx.Err() != nil {
			return 0
		}
		for _, key := range keys {
			c := 0
			switch key.Field {
			case "next":
				c = nextCmp(a, b)
			case "priority":
				c = cmp.Compare(priority(string(b.Priority)), priority(string(a.Priority)))
			case "rank":
				c = emptyLast(a.Rank, b.Rank)
			case "updated":
				c = b.Updated.Compare(a.Updated)
			case "created":
				c = a.Created.Compare(b.Created)
			case "due":
				c = emptyLast(a.Due, b.Due)
			case "id":
				c = natural(a.Ref.ID, b.Ref.ID)
			case "title":
				c = strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
			case "status":
				c = s.groupCompare("status", a.Status, b.Status)
			}
			if key.Desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		if c := nextCmp(a, b); c != 0 {
			return c
		}
		if c := natural(a.Ref.ID, b.Ref.ID); c != 0 {
			return c
		}
		return strings.Compare(a.Ref.Source, b.Ref.Source)
	})
}
func emptyLast(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	return strings.Compare(a, b)
}

type Group struct {
	Value string
	Items []model.Item
}

// Group preserves input order within each group. An empty group value denotes
// an unset field (not the literal value "none"). Empty groups are not emitted.
func (s Snapshot) Group(items []model.Item, key string) []Group {
	return s.groupContext(context.Background(), items, key)
}
func (s Snapshot) groupContext(ctx context.Context, items []model.Item, key string) []Group {
	if key == "" || key == "none" {
		return []Group{{Items: slices.Clone(items)}}
	}
	groups := map[string][]model.Item{}
	for n, i := range items {
		if n%256 == 0 && ctx.Err() != nil {
			return nil
		}
		var values []string
		switch key {
		case "status":
			values = []string{i.Status}
		case "category":
			values = []string{string(i.Category)}
		case "priority":
			values = []string{string(i.Priority)}
		case "assignee":
			values = i.Assignees
		case "label":
			values = i.Labels
		case "milestone":
			values = []string{i.Milestone}
		case "type":
			values = []string{i.Type}
		case "parent":
			if i.Parent != nil {
				source := i.Parent.Source
				if source == "" {
					source = i.Ref.Source
				}
				values = []string{source + ":" + i.Parent.ID}
			}
		case "source":
			values = []string{i.Ref.Source}
		}
		if len(values) == 0 {
			values = []string{""}
		}
		seen := map[string]bool{}
		for _, v := range values {
			if !seen[v] {
				groups[v] = append(groups[v], i)
				seen[v] = true
			}
		}
	}
	out := make([]Group, 0, len(groups))
	for v, items := range groups {
		out = append(out, Group{v, items})
	}
	slices.SortFunc(out, func(a, b Group) int { return s.groupCompare(key, a.Value, b.Value) })
	return out
}
func (s Snapshot) groupCompare(key, a, b string) int {
	var order []string
	switch key {
	case "status":
		for _, v := range s.Settings.Statuses {
			order = append(order, v.Key)
		}
	case "category":
		order = categories
	case "priority":
		order = []string{"urgent", "high", "medium", "low"}
	case "milestone":
		for _, v := range s.Settings.Milestones {
			order = append(order, v.Key)
		}
	}
	if len(order) > 0 {
		ai, bi := slices.Index(order, a), slices.Index(order, b)
		if ai >= 0 && bi >= 0 {
			return cmp.Compare(ai, bi)
		}
		if ai >= 0 {
			return -1
		}
		if bi >= 0 {
			return 1
		}
	}
	return strings.Compare(a, b)
}

// Natural ID comparison handles arbitrary-length imported numeric components
// without integer conversion or overflow. Raw ID breaks zero-padding ties.
func natural(a, b string) int {
	x, y := a, b
	for len(x) > 0 && len(y) > 0 {
		if digit(x[0]) && digit(y[0]) {
			i, j := 0, 0
			for i < len(x) && digit(x[i]) {
				i++
			}
			for j < len(y) && digit(y[j]) {
				j++
			}
			xx, yy := strings.TrimLeft(x[:i], "0"), strings.TrimLeft(y[:j], "0")
			if c := cmp.Compare(len(xx), len(yy)); c != 0 {
				return c
			}
			if c := strings.Compare(xx, yy); c != 0 {
				return c
			}
			x, y = x[i:], y[j:]
		} else {
			if c := cmp.Compare(x[0], y[0]); c != 0 {
				return c
			}
			x, y = x[1:], y[1:]
		}
	}
	if c := cmp.Compare(len(x), len(y)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}
func digit(b byte) bool { return b >= '0' && b <= '9' }
