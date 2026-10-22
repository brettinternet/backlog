package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/textutil"
)

func (m Model) currentSnapshot() *engine.Snapshot {
	if m.opts.Store != nil {
		return m.opts.Store.Current()
	}
	if m.snapshot != nil {
		return m.snapshot
	}
	return &engine.Snapshot{}
}

func (m *Model) updateEditKey(key string) (tea.Cmd, bool) {
	switch key {
	case "s":
		return m.beginPicker(pickerStatus), true
	case "<":
		return m.workflowStatus(-1), true
	case ">":
		return m.workflowStatus(1), true
	case "p":
		return m.beginPicker(pickerPriority), true
	case "a":
		return m.beginPicker(pickerAssignees), true
	case "A":
		if m.opts.Me == "" {
			m.notice = "Disabled: no current user is configured."
			return nil, true
		}
		row, ok := m.selectedRow()
		if !ok {
			m.notice = "Select an item before editing."
			return nil, true
		}
		if reason := m.pickerCapability(row.Ref, pickerAssignees); reason != "" {
			m.notice = "Disabled: " + reason
			return nil, true
		}
		action := editAction{kind: editSelfToggle, me: m.opts.Me}
		m.startEdit(action)
		return m.editCommand(action, false), true
	case "l":
		return m.beginPicker(pickerLabels), true
	case "m":
		return m.beginPicker(pickerMilestone), true
	case "b":
		return m.beginPicker(pickerBlocker), true
	case "P":
		return m.beginPicker(pickerParent), true
	case "t":
		return m.beginPicker(pickerTitle), true
	case "n", "N":
		m.beginCapture(key == "N")
		return nil, true
	case "E":
		row, ok := m.selectedRow()
		if !ok {
			m.notice = "Select an item before editing."
			return nil, true
		}
		if m.opts.Store == nil || m.opts.NativeBytes == nil || m.opts.NativeSource == nil || !m.opts.NativeSource(row.Ref.Source) || !m.opts.Store.Capabilities(row.Ref.Source).Allows(model.Restore) {
			m.beginFullEdit()
			return nil, true
		}
		return m.beginLongEdit(""), true
	case "e":
		return m.beginLongEdit("Description"), true
	case "c":
		return m.beginTextAppend(model.AppendComment), true
	case "i":
		return m.beginTextAppend(model.AppendNote), true
	case "C":
		return m.claimCommand(), true
	case "S":
		return m.workflowTransition(m.opts.Workflow.Start, "active"), true
	case "D":
		return m.workflowTransition(m.opts.Workflow.Done, "done"), true
	case ".":
		if m.lastEdit == nil {
			m.notice = "No edit to repeat."
			return nil, true
		}
		return m.editCommand(*m.lastEdit, true), true
	case "u":
		return m.historyCommand(false), true
	case "ctrl+r":
		return m.historyCommand(true), true
	case "O":
		return m.openSelectedURL(), true
	default:
		return nil, false
	}
}

func (m Model) historyCommand(redo bool) tea.Cmd {
	if m.opts.Store == nil {
		return func() tea.Msg {
			return editResultMsg{message: "Undo and redo are unavailable without an engine store."}
		}
	}
	store, ctx := m.opts.Store, m.opts.Context
	var operation <-chan error
	name := "Undid"
	if redo {
		operation = store.Redo()
		name = "Redid"
	} else {
		operation = store.Undo()
	}
	return func() tea.Msg {
		select {
		case err := <-operation:
			if err != nil {
				return editResultMsg{message: name + " edit failed: " + textutil.Line(err.Error())}
			}
			return editResultMsg{message: name + " the last edit."}
		case <-ctx.Done():
			return editResultMsg{message: name + " canceled."}
		}
	}
}

func (m *Model) toggleCriterion() tea.Cmd {
	row, ok := m.selectedRow()
	if !ok {
		m.notice = "Select an item before toggling a criterion."
		return nil
	}
	item, _, found := m.currentSnapshot().Get(row.Ref)
	if !found {
		m.notice = "The selected item is no longer available."
		return nil
	}
	section, index := criterionAt(item, m.criteriaCursor)
	if index < 0 {
		m.notice = "No criterion is selected."
		return nil
	}
	action := editAction{kind: editCriterionToggle, section: section, index: index}
	m.startEdit(action)
	return m.editCommand(action, false)
}

func (m Model) criterionCount() int {
	row, ok := m.selectedRow()
	if !ok {
		return 0
	}
	item, _, ok := m.currentSnapshot().Get(row.Ref)
	if !ok {
		return 0
	}
	return len(item.Criteria) + len(item.DoD)
}

func (m *Model) keepCriterionVisible() {
	line := criteriaLineAt(m.detailLines, m.criteriaCursor)
	if line < 0 {
		return
	}
	visible := max(1, m.height-6)
	if line < m.detailScroll {
		m.detailScroll = line
	} else if line >= m.detailScroll+visible {
		m.detailScroll = line - visible + 1
	}
}

func criterionAt(item model.Item, cursor int) (string, int) {
	if cursor < 0 {
		return "", -1
	}
	if cursor < len(item.Criteria) {
		return "Acceptance criteria", cursor
	}
	cursor -= len(item.Criteria)
	if cursor < len(item.DoD) {
		return "Definition of done", cursor
	}
	return "", -1
}

func criteriaLineAt(lines []string, cursor int) int {
	position := 0
	for i, line := range lines {
		if strings.Contains(line, "[ ]") || strings.Contains(line, "[x]") || strings.Contains(line, "[X]") {
			if position == cursor {
				return i
			}
			position++
		}
	}
	return -1
}

func (m Model) editView() tea.View {
	lines := []string{"backlog · " + textutil.Line(m.opts.Project)}
	if m.picker.open {
		lines = append(lines, ""+textutil.Line(m.picker.title)+"  (type to filter · enter select · esc cancel)")
		if !m.picker.inputOnly {
			lines = append(lines, "> "+textutil.Line(m.picker.query))
			if m.picker.loading {
				lines = append(lines, "⠋ Loading choices…")
			} else if len(m.picker.filtered) == 0 {
				if m.picker.free && m.picker.query != "" {
					lines = append(lines, "  Create "+textutil.Line(m.picker.query))
				} else {
					lines = append(lines, "  No matches.")
				}
			} else {
				start := max(0, m.picker.cursor-max(1, m.height-7))
				end := min(len(m.picker.filtered), start+max(1, m.height-7))
				for i := start; i < end; i++ {
					choice := m.picker.filtered[i]
					cursor, selected := "  ", " "
					if i == m.picker.cursor {
						cursor = "> "
					}
					if m.picker.multi && m.picker.selected[choice.value] {
						selected = "✓"
					}
					label := choice.label
					if choice.current && !m.picker.multi {
						label += " · current"
					}
					lines = append(lines, cursor+selected+" "+textutil.Line(label))
				}
			}
		}
		footer := "enter apply · space toggle · esc cancel"
		if m.picker.inputOnly {
			footer = "Title: " + textutil.Line(m.picker.query) + "▏ · enter save · esc cancel"
		} else if m.picker.kind == pickerBlocker {
			footer = "enter select · d remove blocker · esc cancel"
		} else if m.picker.free {
			footer = "enter apply/create · space toggle · esc cancel"
		}
		lines = append(lines, footer)
	} else {
		if m.visual || m.marks != nil && m.marks.size() > 0 {
			// editCommand archives every marked and visual-range item, not the cursor row.
			lines = append(lines, "Archive all marked items?  enter/y confirm · esc/n cancel")
		} else if row, ok := m.selectedRow(); ok {
			lines = append(lines, "Archive selected item?  enter/y confirm · esc/n cancel")
			lines = append(lines, row.Ref.ID+" · "+textutil.Line(row.Title))
		}
	}
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = cell(lines[i], m.width)
	}
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}
