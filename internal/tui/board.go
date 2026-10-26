package tui

import (
	"cmp"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// Cards are two rows: priority, ID and signals, then the title.
const (
	cardHeight = 2
	boardGap   = 2
)

// Column offsets and flattened rows are prepared in a command, just like lists.
// Navigation and rendering touch only the focused column or visible cards.
type boardPosition struct{ cursor, top int }
type boardState struct {
	positions map[string]boardPosition
	left      int
}

func (m Model) isBoard() bool { return m.views[m.view].Config.Layout == "board" }
func (m Model) boardStatusGroup() bool {
	text := m.query
	if m.editor.open {
		text = m.editor.text
	}
	q, _ := query.Parse(text, query.Options{})
	return q.Group == "status" || q.Group == "" || q.Group == "none"
}

// boardEmpty reports which declared statuses keep a column without cards. A
// status or category term means the query, not the workflow, picks columns,
// but a negated one, like the Board tab's -category:backlog, hides only the
// columns it excludes. Empty closed columns are omitted: an open-only board
// would otherwise always end in empty Done and Canceled columns.
func boardEmpty(q query.Query, statuses []project.StatusConfig) func(project.StatusConfig) bool {
	var excluded query.Query
	for _, term := range q.Terms {
		if term.Field != "status" && term.Field != "category" {
			continue
		}
		if !term.Negated {
			return func(project.StatusConfig) bool { return false }
		}
		excluded.Terms = append(excluded.Terms, term)
	}
	snapshot := query.Snapshot{Settings: project.Settings{Statuses: statuses}}
	return func(s project.StatusConfig) bool {
		category := model.StatusCategory(s.Category)
		return category != model.Done && category != model.Canceled && excluded.Match(model.Item{Status: s.Key, Category: category}, snapshot, nil)
	}
}

// boardGroups orders status columns by workflow, followed by undeclared
// statuses. Declared statuses without cards appear only where empty allows.
func boardGroups(groups []engine.RowGroup, statuses []project.StatusConfig, status bool, empty func(project.StatusConfig) bool) []engine.RowGroup {
	if !status {
		return groups
	}
	byValue := make(map[string][]engine.Row, len(groups))
	for _, g := range groups {
		for _, r := range g.Rows {
			byValue[r.Status] = append(byValue[r.Status], r)
		}
	}
	result := make([]engine.RowGroup, 0, len(byValue)+len(statuses))
	for _, s := range statuses {
		if rows := byValue[s.Key]; len(rows) > 0 || empty(s) {
			result = append(result, engine.RowGroup{Value: s.Key, Rows: rows})
		}
		delete(byValue, s.Key)
	}
	// Preserve projection order for undeclared provider statuses.
	for _, g := range groups {
		for _, r := range g.Rows {
			if rows, ok := byValue[r.Status]; ok {
				result = append(result, engine.RowGroup{Value: r.Status, Rows: rows})
				delete(byValue, r.Status)
			}
		}
	}
	return result
}
func (m Model) boardColumn() int {
	if len(m.data.starts) == 0 || len(m.data.rows) == 0 {
		return 0
	}
	return m.data.rows[min(m.selected, len(m.data.rows)-1)].group
}
func (m Model) boardEnd(column int) int {
	if column+1 < len(m.data.starts) {
		return m.data.starts[column+1]
	}
	return len(m.data.rows)
}
func (m Model) boardWidth() int {
	if m.detailOpen && m.width >= 120 {
		return m.listWidth()
	}
	return m.width
}

// boardGeometry returns the visible column count and column width.
func (m Model) boardGeometry() (int, int) {
	count := max(1, min(len(m.data.starts), max(1, (m.boardWidth()+boardGap)/(28+boardGap))))
	return count, max(1, (m.boardWidth()-boardGap*(count-1))/count)
}

// boardPage is the number of cards visible in one column.
func (m Model) boardPage() int { return max(1, m.page()/cardHeight) }
func (m *Model) keepBoardVisible() {
	if !m.isBoard() || len(m.data.starts) == 0 {
		return
	}
	column := m.boardColumn()
	start, end := m.data.starts[column], m.boardEnd(column)
	m.selected = max(start+min(1, end-start-1), min(m.selected, end-1))
	if m.board.positions == nil {
		m.board.positions = map[string]boardPosition{}
	}
	key := m.data.rows[start].value
	p := m.board.positions[key]
	p.cursor = max(0, m.selected-start-1)
	page := m.boardPage()
	p.top = max(0, min(p.top, max(0, end-start-1-page)))
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+page {
		p.top = p.cursor - page + 1
	}
	m.board.positions[key] = p
	count, _ := m.boardGeometry()
	m.board.left = max(0, min(m.board.left, max(0, len(m.data.starts)-count)))
	if column < m.board.left {
		m.board.left = column
	}
	if column >= m.board.left+count {
		m.board.left = column - count + 1
	}
}
func (m *Model) navigateBoard(key string, n int, counted bool, prefix string) bool {
	if len(m.data.starts) == 0 {
		return false
	}
	column := m.boardColumn()
	start, end := m.data.starts[column], m.boardEnd(column)
	switch {
	case key == "h" || key == "left" || key == "l" || key == "right" || key == "[" || key == "]":
		delta := n
		if key == "h" || key == "left" || key == "[" {
			delta = -n
		}
		column = max(0, min(column+delta, len(m.data.starts)-1))
		start, end = m.data.starts[column], m.boardEnd(column)
		p := m.board.positions[m.data.rows[start].value]
		m.selected = min(end-1, start+1+p.cursor)
	case key == "j" || key == "down":
		m.selected += n
	case key == "k" || key == "up":
		m.selected -= n
	case prefix == "g" && key == "g", key == "home":
		m.selected = start + 1
	case key == "end":
		m.selected = end - 1
	case key == "G":
		m.selected = end - 1
		if counted {
			m.selected = start + n
		}
	case key == "H" || key == "M" || key == "L":
		top := m.board.positions[m.data.rows[start].value].top
		m.selected = start + 1 + top + screenOffset(key, min(m.boardPage(), end-start-1-top), n)
	case key == "ctrl+d":
		m.selected += max(1, m.boardPage()/2) * n
	case key == "ctrl+u":
		m.selected -= max(1, m.boardPage()/2) * n
	case key == "ctrl+f":
		m.selected += m.boardPage() * n
	case key == "ctrl+b":
		m.selected -= m.boardPage() * n
	default:
		return false
	}
	m.selected = max(start+min(1, end-start-1), min(m.selected, end-1))
	m.keepBoardVisible()
	return true
}

// boardPane draws the visible columns. Only on-screen cards are rendered, so
// cost is independent of backlog size.
func (m Model) boardPane(width, rows int) []string {
	t := m.theme
	count, columnWidth := m.boardGeometry()
	focused := m.boardColumn()
	var columns [][]string
	for c := m.board.left; c < min(len(m.data.starts), m.board.left+count); c++ {
		columns = append(columns, m.boardColumnLines(c, columnWidth, rows, c == focused))
	}
	out := make([]string, rows)
	gap := blank(boardGap)
	for i := range out {
		var b strings.Builder
		for j, column := range columns {
			if j > 0 {
				b.WriteString(gap)
			}
			b.WriteString(column[i])
		}
		out[i] = fit(b.String(), width)
	}
	if rows > 0 && width > 2 {
		// Hidden columns are hinted at the header row's edges.
		if m.board.left > 0 {
			out[0] = t.accent.render("\u2039") + ansi.TruncateLeft(out[0], 1, "")
		}
		if m.board.left+count < len(m.data.starts) {
			out[0] = ansi.Truncate(out[0], width-1, "") + t.accent.render("\u203a")
		}
	}
	return out
}

func (m Model) boardColumnLines(column, width, rows int, focused bool) []string {
	t := m.theme
	start, end := m.data.starts[column], m.boardEnd(column)
	header := m.data.rows[start]
	lines := make([]string, rows)
	if rows == 0 {
		return lines
	}
	lines[0] = fit(" "+m.groupLabel(header, focused)+"  "+t.muted.render(strconv.Itoa(end-start-1)), width)
	for i := 1; i < rows; i++ {
		lines[i] = blank(width)
	}
	if end == start+1 {
		if rows > 1 {
			cursor := " "
			if focused {
				cursor = m.cursorBar()
			}
			lines[1] = fit(cursor+"  "+t.faint.render("No items"), width)
		}
		return lines
	}
	top := m.board.positions[header.value].top
	for k := 0; 1+k*cardHeight < rows; k++ {
		index := start + 1 + top + k
		if index >= end {
			break
		}
		meta, title := m.card(index, width)
		lines[1+k*cardHeight] = meta
		if 2+k*cardHeight < rows {
			lines[2+k*cardHeight] = title
		}
	}
	return lines
}

// card renders one board card as a signal row and a title row.
func (m Model) card(index, width int) (string, string) {
	t := m.theme
	row := m.data.rows[index].row
	selected := index == m.selected
	cursor, mark := " ", " "
	if selected {
		cursor = m.cursorBar()
	}
	if m.isMarked(index, row.Ref) {
		mark = t.mark.render("\u2022")
	}
	left := t.priority(row.Priority).render(priorityGlyph(row.Priority)) + " " + t.muted.render(textutil.Line(row.Ref.ID))
	switch {
	case row.Error != "":
		left += " " + t.failed.render("!")
	case row.Pending:
		left += " " + t.pending.render("\u25cf")
	}
	var right []string
	if row.OpenBlockers > 0 {
		right = append(right, t.red.render("\u2298 "+strconv.Itoa(row.OpenBlockers)))
	}
	if len(row.Assignees) > 0 {
		style := t.muted
		if m.opts.Me != "" && row.Assignees[0] == m.opts.Me {
			style = t.accent
		}
		right = append(right, style.render(ansi.Truncate(textutil.Line(cmp.Or(row.Names[row.Assignees[0]], row.Assignees[0])), 12, "\u2026")))
	}
	inner := max(1, width-gutter-1)
	if signals := strings.Join(right, " "); signals != "" {
		if gap := inner - ansi.StringWidth(left) - ansi.StringWidth(signals); gap >= 1 {
			left += blank(gap) + signals
		}
	}
	base := paint{}
	if selected {
		base = t.selectedTitle
	}
	if row.Category == model.Canceled {
		base = t.muted
	}
	title := m.highlightText(ansi.Truncate(textutil.Line(row.Title), inner, "\u2026"), base)
	meta := fit(cursor+mark+" "+left, width)
	body := fit(cursor+"  "+title, width)
	if selected {
		meta, body = t.selected(meta), t.selected(body)
	}
	return meta, body
}
