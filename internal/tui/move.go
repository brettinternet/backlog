package tui

import (
	"cmp"
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/textutil"
)

// moveReason reports why the selected item can't be reordered, or "".
func (m Model) moveReason() string {
	if m.opts.Store == nil {
		return "reordering is unavailable without an engine store"
	}
	row, ok := m.selectedRow()
	if !ok {
		return "select an item"
	}
	if m.isTree() || m.isBoard() {
		return "sort by rank or next to reorder"
	}
	return m.rankReason(row.Ref.Source)
}

// rankReason reports why the view's sort or the source keeps an item of
// source from being placed by rank, or "".
func (m Model) rankReason(source string) string {
	if len(m.shown.Sort) == 0 || m.shown.Sort[0].Field != "rank" && m.shown.Sort[0].Field != "next" {
		return "sort by rank or next to reorder"
	}
	if kind := m.sourceType(source); kind != "native" {
		return "source " + source + " (" + kind + ") can't be reordered; only native sources store rank"
	}
	return ""
}

// moveRow moves the selected item past the next row of its source up (-1) or
// down (1), writing rank off the update loop. Under sort:next a move stops at
// a priority boundary, since priority outranks rank there.
func (m *Model) moveRow(step int) tea.Cmd {
	reason := m.moveReason()
	if reason == "" && m.detailFocus {
		reason = focusList
	}
	if reason != "" {
		m.notice = "Disabled: " + reason
		return nil
	}
	row, _ := m.selectedRow()
	direction := map[int]string{-1: "first", 1: "last"}[step]
	var target engine.Row
	for n := m.selected + step; ; n += step {
		if n < 0 || n >= len(m.data.rows) || m.data.rows[n].header != "" {
			m.notice = row.Ref.ID + " is already " + direction + "."
			return nil
		}
		if m.data.rows[n].row.Ref.Source == row.Ref.Source {
			target = m.data.rows[n].row
			break
		}
	}
	if m.shown.Sort[0].Field == "next" && target.Priority != row.Priority {
		m.notice = row.Ref.ID + " is " + direction + " in its priority; press p to change priority."
		return nil
	}
	m.startEdit(editAction{move: step})
	// Moving down the display places the item after its neighbor, unless the
	// sort runs backwards.
	after := step > 0 != m.shown.Sort[0].Desc
	store, ctx, catalog, ref := m.opts.Store, m.opts.Context, m.catalog, row.Ref
	return func() tea.Msg {
		items := slices.DeleteFunc(store.Current().Items(), func(i model.Item) bool { return i.Ref.Source != ref.Source })
		var next []model.Ref
		if catalog != nil {
			next = catalog.Next()
		}
		writes, err := engine.PlanMove(engine.RankOrder(items, next), []model.Ref{ref}, target.Ref, after)
		if err != nil {
			return editResultMsg{message: "Move failed: " + textutil.Line(engine.ErrorMessage(err))}
		}
		mutations := engine.MoveMutations(writes)
		if len(mutations) == 0 {
			return editResultMsg{message: "No changes."}
		}
		select {
		case err := <-store.Mutate(mutations...):
			if err != nil {
				return editResultMsg{message: "Move failed: " + boundedLine(err.Error())}
			}
			return editResultMsg{message: "Moved " + ref.ID + "."}
		case <-ctx.Done():
			return editResultMsg{message: "Move canceled while waiting for the source."}
		}
	}
}

// dropCommand writes a dropped board card's status, when column is another
// column, and its rank beside the nearest card of its source in cards from
// slot, as moveRow does, in one edit. Under sort:next only a card of the same
// priority places it. Without such a card the drop changes only status.
func (m Model) dropCommand(row engine.Row, column string, cards []screenRow, slot int) tea.Cmd {
	store, ctx, catalog, statuses := m.opts.Store, m.opts.Context, m.catalog, m.opts.Statuses
	desc, next := m.shown.Sort[0].Desc, m.shown.Sort[0].Field == "next"
	ref := row.Ref
	return func() tea.Msg {
		item, _, ok := store.Current().Get(ref)
		if !ok {
			return editResultMsg{message: "No editable items are selected."}
		}
		var status *model.Mutation
		if (project.Settings{Statuses: statuses}).StatusEntry(item.Status) != column {
			accepted, err := store.StatusChoices(item)
			if err != nil {
				return editResultMsg{message: "Disabled: " + ref.ID + ": " + textutil.Line(engine.ErrorMessage(err))}
			}
			key := columnStatus(statuses, accepted, column)
			if key == "" {
				return editResultMsg{message: "Disabled: " + noStatus(ref, column)}
			}
			status = &model.Mutation{Ref: ref, Op: model.Set, Field: "status", Value: key}
		}
		peer := func(c screenRow) bool {
			return c.row.Ref != ref && c.row.Ref.Source == ref.Source && (!next || c.row.Priority == item.Priority)
		}
		var target model.Ref
		after := !desc
		for n := min(slot, len(cards)) - 1; n >= 0 && target == (model.Ref{}); n-- {
			if peer(cards[n]) {
				target = cards[n].row.Ref
			}
		}
		for n := slot; n < len(cards) && target == (model.Ref{}); n++ {
			if peer(cards[n]) {
				target, after = cards[n].row.Ref, desc
			}
		}
		var mutations []model.Mutation
		if target != (model.Ref{}) {
			items := slices.DeleteFunc(store.Current().Items(), func(i model.Item) bool { return i.Ref.Source != ref.Source })
			var order []model.Ref
			if catalog != nil {
				order = catalog.Next()
			}
			writes, err := engine.PlanMove(engine.RankOrder(items, order), []model.Ref{ref}, target, after)
			if err != nil {
				return editResultMsg{message: "Move failed: " + textutil.Line(engine.ErrorMessage(err))}
			}
			mutations = engine.MoveMutations(writes)
		}
		if status != nil {
			// The card's status and rank change in one write, which one undo reverses.
			n := slices.IndexFunc(mutations, func(w model.Mutation) bool { return w.Ref == ref })
			if n < 0 {
				mutations = append(mutations, *status)
			} else {
				rank := mutations[n]
				rank.ExpectedVersion = ""
				mutations[n] = model.Mutation{Ref: ref, Op: model.Batch, Changes: []model.Mutation{*status, rank}}
			}
			n = slices.IndexFunc(mutations, func(w model.Mutation) bool { return w.Ref == ref })
			mutations[n].ExpectedVersion = cmp.Or(row.Version, item.Version)
			if reason := mutationCapabilityReason(store.Capabilities(ref.Source), mutations[n]); reason != "" {
				return editResultMsg{message: "Disabled: " + ref.ID + ": " + reason}
			}
		}
		if len(mutations) == 0 {
			return editResultMsg{message: "No changes."}
		}
		select {
		case err := <-store.Mutate(mutations...):
			if err != nil {
				return editResultMsg{message: "Move failed: " + boundedLine(err.Error())}
			}
			return editResultMsg{message: "Moved " + ref.ID + "."}
		case <-ctx.Done():
			return editResultMsg{message: "Move canceled while waiting for the source."}
		}
	}
}
