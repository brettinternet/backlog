package tui

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

type mouseState struct {
	dragging     bool
	lastRef      model.Ref
	lastEntry    string // the History entry last clicked
	lastClick    time.Time
	lastX, lastY int
	lastSwipe    time.Time
	sel          textDrag
}

// Mouse routing reuses the renderer's geometry (tab spans, column layout,
// board columns), so hit-testing cannot drift from what is drawn.
func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	p := msg.Mouse()
	if m.modalOpen() {
		// A click outside the palette or menu closes it, like a context
		// menu; a right-click then opens the menu where it landed.
		if _, click := msg.(tea.MouseClickMsg); !click || !m.launcher.open || m.launcher.help || m.insideLauncher(p.X, p.Y) {
			return m.modalMouse(msg)
		}
		m.launcher = launcherState{}
		m.launcherSerial++
		if p.Button != tea.MouseRight {
			return m, nil
		}
	}
	switch msg.(type) {
	case tea.MouseReleaseMsg:
		m.mouse.dragging = false
		return m, m.finishSelection()
	case tea.MouseMotionMsg:
		if m.mouse.sel.active {
			m.extendSelection(p.X, p.Y)
			return m, nil
		}
		if m.mouse.dragging && m.split() {
			m.splitWidth = max(30, min(p.X, m.width-31))
			return m, m.scheduleDetail()
		}
		return m, nil
	}
	if p.X < 0 || p.X >= m.width || p.Y < 0 || p.Y >= m.height {
		return m, nil
	}
	split := m.split()
	detail := m.detailOpen && (!split || p.X > m.listWidth())
	if _, ok := msg.(tea.MouseWheelMsg); ok {
		delta := 3
		switch p.Button {
		case tea.MouseWheelUp:
			delta = -3
		case tea.MouseWheelDown:
		case tea.MouseWheelLeft, tea.MouseWheelRight:
			// A sideways swipe moves one board column. A trackpad sends a
			// burst of events per swipe; events until a pause count once.
			now := m.now()
			burst := now.Sub(m.mouse.lastSwipe) >= 0 && now.Sub(m.mouse.lastSwipe) < 200*time.Millisecond
			m.mouse.lastSwipe = now
			if burst || detail || !m.isBoard() {
				return m, nil
			}
			key := "l"
			if p.Button == tea.MouseWheelLeft {
				key = "h"
			}
			m.detailFocus = false
			m.detached = nil
			m.navigateBoard(key, 1, false, "")
			if m.detailOpen {
				return m, m.scheduleDetail()
			}
			return m, nil
		default:
			return m, nil
		}
		if detail {
			m.detailFocus = true
			m.scrollDetail(delta)
			return m, nil
		}
		m.detailFocus = false
		m.detached = nil
		if m.isBoard() {
			key := "j"
			if delta < 0 {
				key = "k"
			}
			m.navigateBoard(key, 3, false, "")
		} else {
			m.selected = max(0, min(m.selected+delta, len(m.data.rows)-1))
			m.keepSelectionVisible()
		}
		if m.detailOpen {
			return m, m.scheduleDetail()
		}
		return m, nil
	}
	if _, ok := msg.(tea.MouseClickMsg); !ok || p.Button != tea.MouseLeft && p.Button != tea.MouseRight {
		return m, nil
	}
	// A right-click on an item opens its ! menu.
	right := p.Button == tea.MouseRight
	if right && (p.Y == 0 || p.Y == m.height-1) {
		return m, nil
	}
	if p.Y == 0 {
		_, spans, _ := m.tabs()
		for i, s := range spans {
			if p.X >= s.start && p.X < s.end {
				m.detached = nil
				m.switchView(i)
				return m, nil
			}
		}
		return m, nil
	}
	if p.Y == m.height-1 {
		// The query reads as a search field: a click edits it there.
		column := p.X - 1 - ansi.StringWidth(m.visualTag()+m.queryPrefix())
		if m.queryError == "" && m.notice == "" && column >= 0 && column <= ansi.StringWidth(textutil.Line(m.query)) {
			m.editQuery(m.query)
			m.editor.cursor = offsetAt(m.query, column)
			m.project()
		}
		return m, nil
	}
	// y is the body row; the blank rows beside the bars do nothing.
	y := p.Y - m.bodyY()
	if y < 0 || y >= m.bodyRows() {
		return m, nil
	}
	if split && p.X == m.listWidth() && !right {
		m.mouse.dragging = true
		return m, nil
	}
	if detail && right {
		m.detailFocus = true
		return m.openItemMenu()
	}
	if detail {
		// Row hints and the back label show only while detail has focus.
		hinted := m.detailFocus
		m.detailFocus = true
		x, width := p.X-detailPad, m.width
		if split {
			x, width = x-m.listWidth()-1, m.detailWidth()
		}
		inner, line := max(1, width-2*detailPad), m.detailScroll+y-m.detailBodyTop()
		if y >= m.detailBodyTop() {
			m.beginSelection(x, line)
		}
		switch {
		case y == 0:
			if row, ok := m.selectedRow(); ok {
				// Hit-test the labels as drawn, before this click took focus.
				drawn := m
				drawn.detailFocus = hinted
				_, back, more := drawn.detailMetaLine(row, inner, m.bodyRows())
				switch {
				case x >= back.start && x < back.end:
					return m, m.followBack()
				case x >= more.start && x < more.end:
					return m, m.toggleMore()
				}
			}
		case y == 2: // meta, title, then the tab bar
			_, spans := m.detailTabBar(inner)
			first, _ := m.tabRange()
			for i, s := range spans {
				if x >= s.start && x < s.end {
					return m, m.switchDetailTab(first + detailTab(i))
				}
			}
		case y >= m.detailBodyTop() && line == len(m.detailLines) && m.detailTruncated && !m.detailPlaceholder() && m.detailError == "":
			// The "… m more" line below the body.
			return m, m.toggleMore()
		case m.detailTab == tabCriteria && y >= m.detailBodyTop():
			return m, m.clickCriterion(x, line)
		case m.detailTab == tabRelations && y >= m.detailBodyTop():
			return m, m.clickRelation(p.X, p.Y, line)
		case m.hasEntries() && m.detailTab >= tabChanges && m.detailTab != tabHistory && y >= m.detailBodyTop():
			// A click selects the change drawn on any of its lines. Overview
			// checks are one line each, among lines that aren't checks.
			if m.detailCurrent() {
				for i, start := range m.detailEntries {
					if start == line || start < line && m.detailTab != tabChangeOverview {
						m.entryCursor = i
					}
				}
			}
			return m, nil
		case m.detailTab == tabHistory && y >= m.detailBodyTop():
			return m.clickHistory(x, p.X, p.Y, line, inner, hinted)
		}
		return m, nil
	}
	m.detailFocus = false
	m.detached = nil
	if m.loading {
		return m, nil
	}
	width := m.width
	if split {
		width = m.listWidth()
	}
	index := -1
	switch {
	case right && y == 0:
		return m, nil
	case m.isBoard():
		widths := m.boardColumns(m.board.left)
		column, x := -1, 0
		for i, w := range widths {
			if p.X >= x && p.X < x+w {
				column = m.board.left + i
			}
			x += w + boardGap
		}
		switch {
		// The header row's edge arrows focus the hidden column beside them.
		case y == 0 && p.X == 0 && m.board.left > 0:
			column = m.board.left - 1
		case y == 0 && p.X == width-1 && m.board.left+len(widths) < len(m.data.starts):
			column = m.board.left + len(widths)
		case column < 0:
			return m, nil
		}
		start, end := m.data.starts[column], m.boardEnd(column)
		position := m.board.positions[m.data.rows[start].value]
		if y == 0 {
			index = min(end-1, start+1+position.cursor)
		} else {
			index = start + 1 + position.top + (y-1)/cardHeight
			if index >= end {
				return m, nil
			}
		}
	case y == 0:
		if c, ok := m.layout(width).columnAt(p.X); ok && c.field != "" {
			// A second click on the sorted column reverses it.
			value := c.field
			if len(m.shown.Sort) > 0 && m.shown.Sort[0].Field == c.field && !m.shown.Sort[0].Desc {
				value = "-" + c.field
			}
			m.query = setQueryField(m.query, "sort", value)
			m.serial++
			m.project()
		}
		return m, nil
	default:
		index = m.top + y - 1
	}
	if index < 0 || index >= len(m.data.rows) {
		return m, nil
	}
	m.selected = index
	m.keepBoardVisible()
	m.keepSelectionVisible()
	row, ok := m.selectedRow()
	if right {
		var load tea.Cmd
		if m.detailOpen {
			load = m.scheduleDetail()
		}
		if !ok {
			return m, load
		}
		next, open := m.openItemMenu()
		return next, tea.Batch(load, open)
	}
	if !ok {
		// A group header has no item to open; a click toggles its fold, like
		// enter.
		if m.data.rows[index].header != "" && !m.isBoard() {
			return m, m.fold("fold")
		}
		return m, nil
	}
	now := m.now()
	double := row.Ref == m.mouse.lastRef && p.X == m.mouse.lastX && p.Y == m.mouse.lastY && now.Sub(m.mouse.lastClick) >= 0 && now.Sub(m.mouse.lastClick) <= 400*time.Millisecond
	m.mouse.lastClick, m.mouse.lastRef, m.mouse.lastX, m.mouse.lastY = now, row.Ref, p.X, p.Y
	if double {
		m.detailOpen = true
		m.detailFocus = true
		m.mouse.lastRef = model.Ref{}
	}
	if m.detailOpen {
		return m, m.scheduleDetail()
	}
	return m, nil
}

// openItemMenu opens the ! menu for the selected item, as right-click does.
func (m Model) openItemMenu() (tea.Model, tea.Cmd) {
	m.routeSelectedProject()
	m.actionSession, m.actionChange = "", nil
	return m.openConfiguredActions()
}

// insideLauncher reports whether screen cell x, y is on the palette's box.
func (m Model) insideLauncher(x, y int) bool {
	if m.launcher.hidden {
		return false
	}
	body, _, _, _, width := m.launcherLayout()
	left, top, w, h := m.modalRect(width, len(body)+2)
	return x >= left && x < left+w && y >= top && y < top+h
}

// modalMouse lets the lists in modals take the pointer: the wheel moves the
// highlight, and a click picks the row under it as enter would. A click in
// the query being edited moves its cursor. Other prompts ignore the mouse.
// The modal that takes keys, in the same order, takes the mouse.
func (m Model) modalMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	p := msg.Mouse()
	step := 0
	switch msg.(type) {
	case tea.MouseWheelMsg:
		switch p.Button {
		case tea.MouseWheelUp:
			step = -1
		case tea.MouseWheelDown:
			step = 1
		default:
			return m, nil
		}
	case tea.MouseClickMsg:
		if p.Button != tea.MouseLeft {
			return m, nil
		}
	default:
		return m, nil
	}
	switch {
	case m.actionConfirm != nil:
	case m.launcher.open && m.launcher.help:
		// Help's cursor is its scroll offset.
		m.launcher.cursor = max(0, min(m.launcherLast(), m.launcher.cursor+3*step))
	case m.launcher.open:
		if step != 0 {
			m.launcher.cursor = max(0, min(m.launcherLast(), m.launcher.cursor+step))
			return m, nil
		}
		body, head, start, end, width := m.launcherLayout()
		x, y, w, h := m.modalRect(width, len(body)+2)
		line := p.Y - y - 1
		if i := start + line - head; p.X > x && p.X < x+w-1 && line < h-2 && i >= start && i < end {
			m.launcher.cursor = i
			return m.updateLauncher("enter", "")
		}
	case m.longEdit.choice != "" || m.capture.open:
	case m.picker.open:
		if step != 0 {
			key := "down"
			if step < 0 {
				key = "up"
			}
			return m, m.updatePickerKey(key)
		}
		if !m.pickerChoices() {
			return m, nil
		}
		start, end := m.pickerWindow()
		if i := start + m.sheetIndex(m.pickerSheet(), p.Y) - 2; i >= start && i < end { // below the title and input
			if m.picker.multi {
				// The cursor stays, so the list holds still for the next click.
				value := m.picker.filtered[i].value
				m.picker.selected[value] = !m.picker.selected[value]
				return m, nil
			}
			m.picker.cursor = i
			return m, m.updatePickerKey("enter")
		}
	case m.archiveConfirm || m.save.open || m.find.open:
	case m.editor.open:
		e := &m.editor
		if step != 0 {
			e.move(step)
			return m, nil
		}
		if p.Y == m.height-1 {
			// The input follows "/ "; the cursor glyph takes a cell.
			column := p.X - 1 - ansi.StringWidth(m.visualTag()+"/ ")
			if column > ansi.StringWidth(textutil.Line(e.text[:e.cursor])) {
				column--
			}
			if column >= 0 {
				e.cursor = offsetAt(e.text, column)
				m.changedQuery()
			}
			return m, nil
		}
		if len(e.candidates) == 0 {
			return m, nil
		}
		start, end := m.completionWindow()
		if i := start + m.sheetIndex(m.completionSheet(), p.Y) - 1; i >= start && i < end { // below the title
			e.candidate, e.chosen = i, true
			m.insertCompletion()
		}
	}
	return m, nil
}

// sheetIndex maps screen row y to a line of a bottom sheet as drawSheet
// places it, or -1.
func (m Model) sheetIndex(sheet []string, y int) int {
	last := m.height - 2
	shown := min(len(sheet), last)
	top := last - shown + 1
	if shown < 1 || y < top || y > last {
		return -1
	}
	return len(sheet) - shown + y - top
}

// clickCriterion selects the criterion drawn on a detail line, and toggles
// it when the click lands on its box. x is relative to the text's left edge.
func (m *Model) clickCriterion(x, line int) tea.Cmd {
	if !m.detailCurrent() {
		return nil
	}
	found := -1
	for i, start := range m.detailEntries {
		if start <= line {
			found = i
		}
	}
	if found < 0 {
		return nil
	}
	// A criterion spans its wrapped text and evidence, up to a blank line.
	for i := m.detailEntries[found] + 1; i <= line; i++ {
		if i >= len(m.detailLines) || m.detailLines[i] == "" {
			return nil
		}
	}
	m.entryCursor = found
	if line == m.detailEntries[found] && x >= 1 && x <= 3 { // the box at x 2, or a cell beside it
		return m.toggleCriterion()
	}
	return nil
}

// clickRelation selects the relation drawn on a detail line; a double-click
// follows it, as a double-click opens a list row. x and y are the screen cell.
func (m *Model) clickRelation(x, y, line int) tea.Cmd {
	if !m.detailCurrent() {
		return nil
	}
	found := slices.Index(m.detailEntries, line)
	if found < 0 || found >= len(m.detailTargets) {
		return nil
	}
	m.entryCursor = found
	ref, now := m.detailTargets[found].Ref, m.now()
	double := ref == m.mouse.lastRef && x == m.mouse.lastX && y == m.mouse.lastY && now.Sub(m.mouse.lastClick) >= 0 && now.Sub(m.mouse.lastClick) <= 400*time.Millisecond
	m.mouse.lastClick, m.mouse.lastRef, m.mouse.lastX, m.mouse.lastY = now, ref, x, y
	if double {
		m.mouse.lastRef = model.Ref{}
		return m.followRelation()
	}
	return nil
}

// clickHistory selects the History entry drawn on a detail line, including
// its wrapped summary and detail. A click on its marker, or a double-click,
// expands or collapses it as enter does; a click on the selected row's hint
// runs it. x is relative to the text's left edge, sx and sy are the screen
// cell, inner is the text width, and hinted says the hint was drawn.
func (m Model) clickHistory(x, sx, sy, line, inner int, hinted bool) (tea.Model, tea.Cmd) {
	if !m.detailCurrent() {
		return m, nil
	}
	found := -1
	for i, start := range m.detailEntries {
		if start <= line {
			found = i
		}
	}
	if found < 0 || found >= len(m.detailIDs) {
		return m, nil
	}
	start := m.detailEntries[found]
	if hinted && found == m.entryCursor && line == start && found < len(m.detailHints) && m.detailHints[found] != "" && x >= inner-ansi.StringWidth(m.detailHints[found]) {
		for _, a := range m.keymap {
			if a.id == "actions" {
				return m.executeAction(a)
			}
		}
	}
	m.entryCursor = found
	id, now := m.detailIDs[found], m.now()
	double := id == m.mouse.lastEntry && sx == m.mouse.lastX && sy == m.mouse.lastY && now.Sub(m.mouse.lastClick) >= 0 && now.Sub(m.mouse.lastClick) <= 400*time.Millisecond
	m.mouse.lastClick, m.mouse.lastEntry, m.mouse.lastX, m.mouse.lastY = now, id, sx, sy
	// The marker follows the row's indent, at x 0 or 2; a cell beside it counts.
	text := ansi.Strip(m.detailLines[start])
	body := strings.TrimLeft(text, " ")
	marker := len(text) - len(body)
	onMarker := line == start && (strings.HasPrefix(body, "▸") || strings.HasPrefix(body, "▾")) && x >= marker-1 && x <= marker+1
	if onMarker || double {
		m.mouse.lastEntry = ""
		return m, m.toggleHistoryEntry()
	}
	return m, nil
}

// offsetAt is the byte offset of the character at screen column col of
// text, or its end.
func offsetAt(text string, col int) int {
	width := 0
	for i, r := range text {
		width += ansi.StringWidth(string(r))
		if col < width {
			return i
		}
	}
	return len(text)
}
