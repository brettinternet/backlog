package tui

import (
	"fmt"
	"strings"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/textutil"
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
func boardGroups(groups []engine.RowGroup, statuses []string, status bool) []engine.RowGroup {
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
	for _, key := range statuses {
		result = append(result, engine.RowGroup{Value: key, Rows: byValue[key]})
		delete(byValue, key)
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
func (m Model) boardGeometry() (int, int) {
	count := max(1, min(len(m.data.starts), max(1, m.boardWidth()/28)))
	return count, max(1, (m.boardWidth()-count+1)/count)
}
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
	page := max(1, m.height-5)
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
	case prefix == "g" && key == "g":
		m.selected = start + 1
	case key == "G":
		m.selected = end - 1
		if counted {
			m.selected = start + n
		}
	case key == "ctrl+d":
		m.selected += max(1, (m.height-5)/2) * n
	case key == "ctrl+u":
		m.selected -= max(1, (m.height-5)/2) * n
	case key == "ctrl+f":
		m.selected += max(1, m.height-5) * n
	case key == "ctrl+b":
		m.selected -= max(1, m.height-5) * n
	default:
		return false
	}
	m.selected = max(start+min(1, end-start-1), min(m.selected, end-1))
	m.keepBoardVisible()
	return true
}
func (m Model) boardLine(line int) string {
	if m.loading || m.failure != "" || len(m.data.starts) == 0 || m.showHelp || m.editor.open || m.keyPrefix != "" || m.prefix == "f" || m.prefix == "o" || m.prefix == "z" {
		// The header has no list row; rendering row 0 there would show it twice.
		if line < 0 {
			return cell("", m.boardWidth())
		}
		return cell(m.listLine(m.top+line, false), m.boardWidth())
	}
	count, width := m.boardGeometry()
	parts := make([]string, 0, count)
	for c := m.board.left; c < min(len(m.data.starts), m.board.left+count); c++ {
		start, end := m.data.starts[c], m.boardEnd(c)
		group := m.data.rows[start].value
		label := ""
		if line < 0 {
			marker := "  "
			if c == m.boardColumn() {
				marker = "> "
			}
			label = fmt.Sprintf("%s%s (%d)", marker, textutil.Line(group), end-start-1)
		} else {
			p := m.board.positions[group]
			index := start + 1 + p.top + line
			if index < end {
				row := m.data.rows[index].row
				marker := "  "
				if m.isMarked(index, row.Ref) {
					marker = "* "
				}
				if index == m.selected {
					marker = "> "
				}
				state := ""
				if row.Pending {
					state = "● "
				}
				if row.Error != "" {
					state = "! "
				}
				label = marker + row.Ref.ID + " " + state + textutil.Line(row.Title)
			} else if end == start+1 && line == 0 {
				label = "  ─"
			}
		}
		parts = append(parts, cell(label, width))
	}
	return cell(strings.Join(parts, "│"), m.boardWidth())
}
