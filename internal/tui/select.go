package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// textDrag is a drag over detail body text. Positions are a detailLines
// index and a column inside the padded pane. Mouse capture hides the
// terminal's own textDrag, so the drag is drawn here and copied on release.
type textDrag struct {
	active       bool
	moved        bool
	line0, col0  int
	line1, col1  int
	copied, text string
}

func (m *Model) beginSelection(col, line int) {
	m.mouse.sel = textDrag{active: true, line0: line, col0: col, line1: line, col1: col}
}

func (m *Model) extendSelection(x, y int) {
	s := &m.mouse.sel
	col := x - detailPad
	if m.split() {
		col -= m.listWidth() + 1
	}
	row := y - m.bodyY() - m.detailBodyTop()
	// Each motion event past an edge scrolls a line, so a drag reaches text
	// beyond the visible rows.
	switch {
	case row < 0:
		m.scrollDetail(-1)
	case row >= m.detailVisible():
		m.scrollDetail(1)
	}
	row = max(0, min(row, m.detailVisible()-1))
	s.line1 = max(0, min(m.detailScroll+row, len(m.detailLines)-1))
	s.col1 = max(0, col)
	s.moved = true
}

// bounds orders the endpoints and reports whether anything is covered.
func (s textDrag) bounds() (l0, c0, l1, c1 int, ok bool) {
	l0, c0, l1, c1 = s.line0, s.col0, s.line1, s.col1
	if l1 < l0 || l1 == l0 && c1 < c0 {
		l0, c0, l1, c1 = l1, c1, l0, c0
	}
	return l0, c0, l1, c1, s.active && s.moved && (l1 > l0 || c1 > c0)
}

// finishSelection copies the dragged text and clears the textDrag.
func (m *Model) finishSelection() tea.Cmd {
	s := m.mouse.sel
	m.mouse.sel = textDrag{}
	l0, c0, l1, c1, ok := s.bounds()
	if !ok || l0 >= len(m.detailLines) {
		return nil
	}
	var out []string
	for i := l0; i <= min(l1, len(m.detailLines)-1); i++ {
		plain := ansi.Strip(m.detailLines[i])
		from, to := 0, ansi.StringWidth(plain)
		if i == l0 {
			from = c0
		}
		if i == l1 {
			to = c1
		}
		out = append(out, strings.TrimRight(ansi.Cut(plain, from, to), " "))
	}
	text := strings.Join(out, "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	m.notice = "Copied " + textutil.Line(ansi.Truncate(text, 40, "…"))
	return osc52(text)
}

// highlightSelection reverses the selected cells of detail line i.
func (m Model) highlightSelection(i int, line string) string {
	l0, c0, l1, c1, ok := m.mouse.sel.bounds()
	if !ok || i < l0 || i > l1 {
		return line
	}
	from, to := 0, ansi.StringWidth(line)
	if i == l0 {
		from = c0
	}
	if i == l1 {
		to = min(c1, to)
	}
	if from >= to {
		return line
	}
	mid := ansi.Strip(ansi.Cut(line, from, to))
	return ansi.Cut(line, 0, from) + m.theme.reverse.render(mid) + ansi.Cut(line, to, ansi.StringWidth(line))
}
