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
	// Keep cached order and append new rows, so the cache and its bounded warm
	// preview are stable across refreshes.
	index := make(map[model.Ref]int, len(previous.Items)+len(next.Items))
	out.Items = make([]model.Item, 0, len(previous.Items)+len(next.Items))
	put := func(item model.Item) {
		if i, ok := index[item.Ref]; ok {
			out.Items[i] = item
			return
		}
		index[item.Ref] = len(out.Items)
		out.Items = append(out.Items, item)
	}
	for _, item := range previous.Items {
		put(item)
		if previous.Stale[item.Ref] || !next.RetainUnseen && (!complete || previous.Coverage != Complete) {
			out.Stale[item.Ref] = true
		}
	}
	for _, item := range next.Items {
		put(item)
		delete(out.Stale, item.Ref)
		if next.Stale[item.Ref] {
			out.Stale[item.Ref] = true
		}
	}
	out.ImmutableItems = nil
	out.Incremental = false // the returned rows now represent the merged snapshot
	out.RetainUnseen = false
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
	// A default view omits newly archived rows; flag their cached copies.
	for ref, value := range next.Archived {
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
