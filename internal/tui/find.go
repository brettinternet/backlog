package tui

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// findState searches the detail tab in view. The search outlives tab and item
// changes, so n and N keep working, until esc clears it or detail closes.
type findState struct {
	open    bool
	input   lineInput
	text    string // the applied search; empty for none
	origin  int    // the scroll a cancelled prompt restores
	lines   []int  // detailLines index of each line with a match
	current int    // index into lines; -1 before the first jump
}

func (m *Model) openFind() {
	m.find = findState{open: true, origin: m.detailScroll, current: -1}
}

// updateFind searches as you type, from where the prompt opened.
func (m Model) updateFind(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.detailScroll = m.find.origin
		m.find = findState{}
	case "enter":
		m.find.open = false
		if m.find.text == "" {
			m.find = findState{}
		}
	default:
		if _, changed := m.find.input.edit(key.String(), key.Text); changed {
			m.find.text = m.find.input.text
			m.detailScroll = m.find.origin
			m.findMatches()
			m.stepFind(true)
		}
	}
	return m, nil
}

// findMatches indexes the lines of the body in view that contain the search.
func (m *Model) findMatches() {
	m.find.lines, m.find.current = nil, -1
	needle := []rune(strings.ToLower(m.find.text))
	if len(needle) == 0 {
		return
	}
	for i, line := range m.detailLines {
		if _, ok := markMatches([]rune(ansi.Strip(line)), [][]rune{needle}); ok {
			m.find.lines = append(m.find.lines, i)
		}
	}
}

// stepFind moves to the next or previous matching line, wrapping. The first
// step goes to the first match at or below the top of the pane, or the last
// above it.
func (m *Model) stepFind(forward bool) {
	n := len(m.find.lines)
	if n == 0 {
		return
	}
	switch {
	case m.find.current >= 0 && forward:
		m.find.current = (m.find.current + 1) % n
	case m.find.current >= 0:
		m.find.current = (m.find.current + n - 1) % n
	case forward:
		m.find.current = 0
		for i, line := range m.find.lines {
			if line >= m.detailScroll {
				m.find.current = i
				break
			}
		}
	default:
		m.find.current = n - 1
		for i := n - 1; i >= 0; i-- {
			if m.find.lines[i] < m.detailScroll {
				m.find.current = i
				break
			}
		}
	}
	line, visible := m.find.lines[m.find.current], m.detailVisible()
	if m.detailTab == tabChangeDiff {
		m.diffCursorTo(line)
	}
	if line < m.detailScroll || line >= m.detailScroll+visible {
		m.detailScroll = line - visible/2
		m.scrollDetail(0)
	}
}

// findStatus is the search and its position for the status bar.
func (m Model) findStatus() string {
	t := m.theme
	switch {
	case m.find.text == "":
		return ""
	case len(m.find.lines) == 0:
		return t.red.render("no match")
	}
	return t.muted.render(strconv.Itoa(m.find.current+1) + "/" + strconv.Itoa(len(m.find.lines)))
}

// markMatches marks every rune inside a case-insensitive match of a needle,
// which must already be lowercase.
func markMatches(runes []rune, needles [][]rune) ([]bool, bool) {
	marked := make([]bool, len(runes))
	found := false
	for _, needle := range needles {
		if len(needle) == 0 {
			continue
		}
		for i := 0; i+len(needle) <= len(runes); i++ {
			match := true
			for j, r := range needle {
				if unicode.ToLower(runes[i+j]) != r {
					match = false
					break
				}
			}
			if match {
				found = true
				for j := range needle {
					marked[i+j] = true
				}
			}
		}
	}
	return marked, found
}

// highlightFind marks matches inside an already styled line, keeping its
// escape sequences in place so the surrounding style survives.
func (m Model) highlightFind(line string) string {
	hl := m.theme.reverse
	if m.find.text == "" || hl.on == "" {
		return line
	}
	// Split the line into printable runes and the raw bytes before each.
	var runes []rune
	var raw []string
	var pending strings.Builder
	for i := 0; i < len(line); {
		if line[i] == '\x1b' {
			n := escapeLength(line[i:])
			pending.WriteString(line[i : i+n])
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		raw = append(raw, pending.String())
		pending.Reset()
		runes = append(runes, r)
		i += size
	}
	marked, ok := markMatches(runes, [][]rune{[]rune(strings.ToLower(m.find.text))})
	if !ok {
		return line
	}
	var out strings.Builder
	for i, r := range runes {
		out.WriteString(raw[i])
		if marked[i] && (i == 0 || !marked[i-1]) {
			out.WriteString(hl.on)
		}
		out.WriteRune(r)
		if marked[i] && (i == len(runes)-1 || !marked[i+1]) {
			out.WriteString(hl.off)
		}
	}
	out.WriteString(pending.String())
	return out.String()
}

// escapeLength is the byte length of the escape sequence s starts with: CSI up
// to its final byte, OSC up to BEL or ST, otherwise ESC and one byte.
func escapeLength(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
	case ']':
		for i := 2; i < len(s); i++ {
			if s[i] == '\a' {
				return i + 1
			}
			if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
	default:
		return 2
	}
	return len(s)
}
