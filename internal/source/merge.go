package source

import "github.com/brettinternet/backlog/internal/model"

// Merge combines a remote enumeration or incremental response with cached rows.
// Only a fresh, complete enumeration can remove an unseen row. An incomplete
// response makes unseen rows stale, even if they were formerly known done.
func Merge(previous, next Delta) Delta {
	complete := next.Coverage == Complete && len(next.Stale) == 0
	if complete && !next.Incremental {
		return next
	}
	out := next
	out.Stale = make(map[model.Ref]bool)
	rows := make(map[model.Ref]model.Item, len(previous.Items)+len(next.Items))
	for _, item := range previous.Items {
		rows[item.Ref] = item
		if !complete || previous.Coverage != Complete || previous.Stale[item.Ref] {
			out.Stale[item.Ref] = true
		}
	}
	for _, item := range next.Items {
		rows[item.Ref] = item
		delete(out.Stale, item.Ref)
		if next.Stale[item.Ref] {
			out.Stale[item.Ref] = true
		}
	}
	out.Items = make([]model.Item, 0, len(rows))
	for _, item := range rows {
		out.Items = append(out.Items, item)
	}
	out.ImmutableItems = nil
	out.Incremental = false // the returned rows now represent the merged snapshot
	if complete && previous.Coverage != Complete {
		out.Coverage = Partial
	}
	if len(out.Stale) > 0 && out.Coverage == Complete {
		out.Coverage = Partial
	}
	out.Archived = make(map[model.Ref]bool)
	for ref, value := range previous.Archived {
		out.Archived[ref] = value
	}
	for _, item := range next.Items {
		out.Archived[item.Ref] = next.Archived[item.Ref]
	}
	if next.Cursor.Value == "" {
		out.Cursor = previous.Cursor
	}
	return out
}
