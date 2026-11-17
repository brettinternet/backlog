package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
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
	if m.isTree() || m.isBoard() || len(m.shown.Sort) == 0 || m.shown.Sort[0].Field != "rank" && m.shown.Sort[0].Field != "next" {
		return "sort by rank or next to reorder"
	}
	if kind := m.sourceType(row.Ref.Source); kind != "native" {
		return "source " + row.Ref.Source + " (" + kind + ") can't be reordered; only native sources store rank"
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
