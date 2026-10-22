package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brettinternet/backlog/internal/query"
)

// The menu table is shared by dispatch and the which-key popup.
type queryBinding struct{ key, label, value string }

var queryMenus = map[string][]queryBinding{
	"f": {{"s", "status", "status"}, {"p", "priority", "priority"}, {"a", "assignee", "assignee"}, {"l", "label", "label"}, {"t", "type", "type"}, {"m", "milestone", "milestone"}, {"r", "ready only", "is:ready"}, {"d", "show done", "is:open,closed"}, {"c", "clear", ""}},
	"o": {{"n", "next", "next"}, {"p", "priority", "priority"}, {"r", "rank", "rank"}, {"u", "updated", "updated"}, {"c", "created", "created"}, {"d", "due", "due"}, {"i", "ID", "id"}, {"o", "reverse", "reverse"}},
	"z": {{"s", "status", "status"}, {"c", "category", "category"}, {"p", "priority", "priority"}, {"A", "assignee", "assignee"}, {"l", "label", "label"}, {"m", "milestone", "milestone"}, {"t", "type", "type"}, {"P", "parent", "parent"}, {"n", "none", "none"}, {"a", "toggle fold", "fold"}, {"M", "fold all", "fold-all"}, {"R", "unfold all", "unfold-all"}},
}

type queryEditor struct {
	open, picker   bool
	text, original string
	cursor         int // UTF-8 byte offset
	candidates     []query.Candidate
	candidate      int
	ready          uint64
}

func (m Model) queryText() string { return m.query }

// tokenSpans preserves source text, including quoted whitespace and escapes.
func tokenSpans(s string) [][2]int {
	var spans [][2]int
	start, quoted, escaped := -1, false, false
	for i, r := range s {
		if start < 0 && !unicode.IsSpace(r) {
			start = i
		}
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if unicode.IsSpace(r) && !quoted && start >= 0 {
			spans = append(spans, [2]int{start, i})
			start = -1
		}
	}
	if start >= 0 {
		spans = append(spans, [2]int{start, len(s)})
	}
	return spans
}
func setQueryField(text, field, value string) string {
	var terms []string
	for _, span := range tokenSpans(text) {
		term := text[span[0]:span[1]]
		if !strings.HasPrefix(strings.TrimPrefix(strings.ToLower(term), "-"), field+":") {
			terms = append(terms, term)
		}
	}
	if value != "" {
		terms = append(terms, field+":"+value)
	}
	return strings.Join(terms, " ")
}
func clearQuery(text string) string {
	var terms []string
	for _, span := range tokenSpans(text) {
		term := text[span[0]:span[1]]
		if strings.HasPrefix(term, "sort:") || strings.HasPrefix(term, "group:") {
			terms = append(terms, term)
		}
	}
	return strings.Join(terms, " ")
}
func (e queryEditor) completion() (field, prefix string, start, end int) {
	start, end = e.cursor, e.cursor
	for _, span := range tokenSpans(e.text) {
		if e.cursor >= span[0] && e.cursor <= span[1] {
			start, end = span[0], span[1]
			break
		}
	}
	raw := e.text[start:e.cursor]
	if strings.HasPrefix(raw, "-") {
		start++
		raw = raw[1:]
	}
	if colon := strings.IndexByte(raw, ':'); colon >= 0 {
		field = raw[:colon]
		start += colon + 1
		raw = raw[colon+1:]
		quoted, escaped := false, false
		for i, r := range raw {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				quoted = !quoted
			}
			if r == ',' && !quoted {
				start = e.cursor - len(raw) + i + 1
			}
		}
		raw = e.text[start:e.cursor]
	}
	var b strings.Builder
	escaped := false
	for _, r := range raw {
		if escaped {
			b.WriteRune(r)
			escaped = false
		} else if r == '\\' {
			escaped = true
		} else if r != '"' {
			b.WriteRune(r)
		}
	}
	return field, b.String(), start, end
}
func quoteQuery(s string) string {
	if strings.ContainsAny(s, " \t\n\r,:\"\\") || strings.HasPrefix(s, "-") {
		return strconv.Quote(s)
	}
	return s
}
func (m *Model) editQuery(text string, picker bool) {
	m.editor = queryEditor{open: true, picker: picker, text: text, original: m.query, cursor: len(text)}
	m.prefix = ""
	m.count = 0
	m.inputSerial++
	m.queryError = ""
	m.queryWarning = ""
	m.project()
}
func (m *Model) changedQuery() {
	m.editor.candidates = nil
	m.editor.candidate = 0
	m.editor.ready = 0
	m.queryError = ""
	m.queryWarning = ""
	m.serial++ // fence preparation from a previous valid draft
	m.project()
}
func (m *Model) insertCompletion() bool {
	e := &m.editor
	if e.ready != m.request || len(e.candidates) == 0 {
		return false
	}
	field, _, start, end := e.completion()
	value := e.candidates[e.candidate].Value
	if field != "" {
		value = quoteQuery(value)
	}
	e.text = e.text[:start] + value + e.text[end:]
	e.cursor = start + len(value)
	m.changedQuery()
	return true
}
func (m Model) updateQuery(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	e := &m.editor
	switch key.String() {
	case "esc":
		m.query = e.original
		m.editor = queryEditor{}
		m.queryError = ""
		m.queryWarning = ""
		m.serial++
		m.project()
	case "enter":
		if e.picker && m.insertCompletion() {
			e.picker = false
		}
		if _, err := query.Parse(e.text, query.Options{}); err != nil {
			m.queryError = err.Error()
			return m, nil
		}
		m.query = e.text
		m.editor = queryEditor{}
		m.queryError = ""
		m.serial++
		m.project()
	case "tab":
		m.insertCompletion()
	case "up", "shift+tab":
		if len(e.candidates) > 0 {
			e.candidate = (e.candidate + len(e.candidates) - 1) % len(e.candidates)
		}
	case "down":
		if len(e.candidates) > 0 {
			e.candidate = (e.candidate + 1) % len(e.candidates)
		}
	case "left":
		if e.cursor > 0 {
			_, size := utf8.DecodeLastRuneInString(e.text[:e.cursor])
			e.cursor -= size
			m.changedQuery()
		}
	case "right":
		if e.cursor < len(e.text) {
			_, size := utf8.DecodeRuneInString(e.text[e.cursor:])
			e.cursor += size
			m.changedQuery()
		}
	case "home", "ctrl+a":
		e.cursor = 0
		m.changedQuery()
	case "end", "ctrl+e":
		e.cursor = len(e.text)
		m.changedQuery()
	case "ctrl+u":
		e.text = e.text[e.cursor:]
		e.cursor = 0
		m.changedQuery()
	case "backspace":
		if e.cursor > 0 {
			_, size := utf8.DecodeLastRuneInString(e.text[:e.cursor])
			e.text = e.text[:e.cursor-size] + e.text[e.cursor:]
			e.cursor -= size
			m.changedQuery()
		}
	case "delete":
		if e.cursor < len(e.text) {
			_, size := utf8.DecodeRuneInString(e.text[e.cursor:])
			e.text = e.text[:e.cursor] + e.text[e.cursor+size:]
			m.changedQuery()
		}
	default:
		if key.Text != "" && !strings.ContainsAny(key.Text, "\x00\x1b\r\n") {
			e.text = e.text[:e.cursor] + key.Text + e.text[e.cursor:]
			e.cursor += len(key.Text)
			m.changedQuery()
		}
	}
	return m, nil
}
func (m *Model) queryMenu(prefix, key string) (bool, tea.Cmd) {
	for _, binding := range queryMenus[prefix] {
		if binding.key != key {
			continue
		}
		switch prefix {
		case "f":
			switch key {
			case "c":
				m.query = clearQuery(m.query)
			case "r", "d":
				m.query = setQueryField(m.query, "is", strings.TrimPrefix(binding.value, "is:"))
			default:
				text := setQueryField(m.query, binding.value, "")
				m.editQuery(strings.TrimSpace(text+" ")+" "+binding.value+":", true)
				return true, nil
			}
		case "o":
			value := binding.value
			if value == "reverse" {
				q, err := query.Parse(m.query, query.Options{})
				value = "-next"
				if err == nil && len(q.Sort) > 0 {
					var keys []string
					for _, k := range q.Sort {
						v := k.Field
						if !k.Desc {
							v = "-" + v
						}
						keys = append(keys, v)
					}
					value = strings.Join(keys, ",")
				}
			}
			m.query = setQueryField(m.query, "sort", value)
		case "z":
			switch key {
			case "a":
				if len(m.data.rows) > 0 && !m.ungrouped {
					r := m.data.rows[m.selected]
					m.folded = &foldState{group: r.value, previous: m.folded}
					m.selected = m.data.starts[r.group]
					m.serial++
					return true, m.prepare()
				}
				return true, nil
			case "M", "R":
				m.folded = nil
				m.foldAll = key == "M"
				m.serial++
				return true, m.prepare()
			default:
				m.query = setQueryField(m.query, "group", binding.value)
				m.folded = nil
				m.foldAll = false
			}
		}
		m.queryError = ""
		m.queryWarning = ""
		m.serial++
		m.project()
		return true, nil
	}
	return false, nil
}
func (m Model) queryOverlay(index int) (string, bool) {
	if menu, ok := queryMenus[m.prefix]; ok {
		if index == 0 {
			return m.prefix + " · choose a key · esc cancel", true
		}
		if index <= len(menu) {
			b := menu[index-1]
			return fmt.Sprintf(" %s  %s", b.key, b.label), true
		}
		return "", true
	}
	if m.editor.open && index >= max(0, m.height-9) {
		n := index - max(0, m.height-9)
		if n == 0 {
			return "tab complete · ↑/↓ choose · enter apply · esc restore", true
		}
		c := m.editor.candidate + n - 1
		if c < len(m.editor.candidates) {
			candidate := m.editor.candidates[c]
			marker := "  "
			if n == 1 {
				marker = "> "
			}
			return marker + candidate.Value + " " + candidate.Name, true
		}
		return "", true
	}
	return "", false
}
func (m Model) highlightLine(line string) string {
	if m.opts.NoColor || len(m.highlights) == 0 {
		return line
	}
	style := lipgloss.NewStyle().Bold(true).Underline(true)
	// Work only on the already sanitized, clipped viewport. Decode runes so a
	// Unicode case mapping cannot turn lowercase byte offsets into broken UTF-8.
	runes := []rune(line)
	marked := make([]bool, len(runes))
	for _, term := range m.highlights {
		needle := []rune(strings.ToLower(term))
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
				for j := range needle {
					marked[i+j] = true
				}
			}
		}
	}
	var out strings.Builder
	for i := 0; i < len(runes); {
		end := i + 1
		for end < len(runes) && marked[end] == marked[i] {
			end++
		}
		s := string(runes[i:end])
		if marked[i] {
			s = style.Render(s)
		}
		out.WriteString(s)
		i = end
	}
	return out.String()
}
