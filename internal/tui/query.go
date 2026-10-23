package tui

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/query"
)

// The menu table is shared by dispatch and the which-key popup.
type queryBinding struct{ key, label, value string }

// Menus follow Vim where it has a meaning: z folds and scrolls, so grouping by
// category and type take the capitals.
var queryMenus = map[string][]queryBinding{
	"f": {{"s", "Status", "status"}, {"p", "Priority", "priority"}, {"a", "Assignee", "assignee"}, {"l", "Label", "label"}, {"t", "Type", "type"}, {"m", "Milestone", "milestone"}, {"r", "Toggle ready", "is:ready"}, {"d", "Toggle done", "is:open,closed"}, {"b", "Toggle drafts", "drafts"}, {"c", "Clear", ""}},
	"o": {{"n", "Next", "next"}, {"p", "Priority", "priority"}, {"r", "Rank", "rank"}, {"u", "Updated", "updated"}, {"c", "Created", "created"}, {"d", "Due", "due"}, {"i", "ID", "id"}, {"o", "Reverse", "reverse"}},
	"z": {{"s", "Status", "status"}, {"C", "Category", "category"}, {"p", "Priority", "priority"}, {"A", "Assignee", "assignee"}, {"l", "Label", "label"}, {"m", "Milestone", "milestone"}, {"T", "Type", "type"}, {"P", "Parent", "parent"}, {"n", "None", "none"},
		{"a", "Toggle fold", "fold"}, {"o", "Open fold", "fold-open"}, {"c", "Close fold", "fold-close"}, {"M", "Fold all", "fold-all"}, {"R", "Unfold all", "unfold-all"},
		{"t", "Scroll to top", "scroll-top"}, {"z", "Scroll to center", "scroll-center"}, {"b", "Scroll to bottom", "scroll-bottom"}},
}

// zCommand reports a z binding that folds or scrolls instead of grouping.
func zCommand(value string) bool {
	return strings.HasPrefix(value, "fold") || strings.HasPrefix(value, "unfold") || strings.HasPrefix(value, "scroll")
}

// Candidates stay on screen while the next ones load, so typing never flashes
// the list. Only candidates computed for the current text can be inserted.
type queryEditor struct {
	lineInput
	open       bool
	original   string
	candidates []query.Candidate
	candidate  int
	field      string // what the candidates complete: a field, or "" for field names
	current    bool   // the candidates are for the current text and cursor
	chosen     bool   // a candidate was picked since the last edit
	unfinished bool   // the word at the cursor is incomplete or unknown
}

// highlighted reports the candidate enter inserts: one picked, or the first
// when the word at the cursor can't stand as typed.
func (e queryEditor) highlighted() bool {
	return len(e.candidates) > 0 && (e.chosen || e.unfinished)
}

// word is the span of the token under the cursor.
func (e queryEditor) word() (start, end int) {
	for _, span := range tokenSpans(e.text) {
		if e.cursor >= span[0] && e.cursor <= span[1] {
			return span[0], span[1]
		}
	}
	return e.cursor, e.cursor
}

// inWord reports a query problem located in the word being typed.
func (e queryEditor) inWord(err error) bool {
	var problem *query.Error
	if !errors.As(err, &problem) {
		return false
	}
	start, end := e.word()
	return problem.Pos >= start && problem.Pos <= end
}

// receive takes the candidates for the current text. A refresh of the same
// text keeps the highlighted value.
func (e *queryEditor) receive(candidates []query.Candidate) {
	previous := ""
	if e.candidate < len(e.candidates) {
		previous = e.candidates[e.candidate].Value
	}
	if !e.current {
		e.chosen = false
	}
	e.candidates, e.candidate, e.current = candidates, 0, true
	e.field, _, _, _ = e.completion()
	for i, c := range candidates {
		if c.Value == previous {
			e.candidate = i
			break
		}
	}
}

// move highlights the next or previous candidate; the first move from no
// highlight lands on the first or last.
func (e *queryEditor) move(delta int) {
	n := len(e.candidates)
	if n == 0 {
		return
	}
	switch {
	case e.highlighted():
		e.candidate = ((e.candidate+delta)%n + n) % n
	case delta > 0:
		e.candidate = 0
	default:
		e.candidate = n - 1
	}
	e.chosen = true
}

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

// toggleTerm removes term from the query, or appends it when absent.
func toggleTerm(text, term string) string {
	var terms []string
	found := false
	for _, span := range tokenSpans(text) {
		if t := text[span[0]:span[1]]; strings.EqualFold(t, term) {
			found = true
		} else {
			terms = append(terms, t)
		}
	}
	if !found {
		terms = append(terms, term)
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
func (m *Model) editQuery(text string) {
	m.editor = queryEditor{lineInput: newLine(text), open: true, original: m.query}
	m.prefix = ""
	m.count = 0
	m.inputSerial++
	m.queryError = ""
	m.queryWarning = ""
	m.project()
}
func (m *Model) changedQuery() {
	m.editor.current = false
	m.queryError = ""
	m.queryWarning = ""
	m.serial++ // fence preparation from a previous valid draft
	m.project()
}

// insertCompletion replaces the word at the cursor with the highlighted
// candidate, or the first.
func (m *Model) insertCompletion() bool {
	e := &m.editor
	if !e.current || len(e.candidates) == 0 {
		return false
	}
	field, _, start, end := e.completion()
	value := e.candidates[0].Value
	if e.highlighted() {
		value = e.candidates[e.candidate].Value
	}
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
		m.acceptQuery()
	case "tab":
		m.insertCompletion()
	case "up", "shift+tab":
		e.move(-1)
	case "down":
		e.move(1)
	default:
		if handled, _ := e.edit(key.String(), key.Text); handled {
			m.changedQuery()
		}
	}
	return m, nil
}

// acceptQuery inserts a highlighted candidate, then applies the draft if it
// parses. A draft that doesn't parse after an insert, such as a new field
// name, stays open for its value.
func (m *Model) acceptQuery() {
	e := &m.editor
	inserted := e.highlighted() && m.insertCompletion()
	if _, err := query.Parse(e.text, query.Options{}); err != nil {
		if !inserted {
			m.queryError = err.Error()
		}
		return
	}
	m.query = strings.TrimSpace(e.text)
	m.editor = queryEditor{}
	m.queryError = ""
	m.serial++
	m.project()
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
				// Removing is: falls back to the implicit is:open.
				value := strings.TrimPrefix(binding.value, "is:")
				if slices.ContainsFunc(tokenSpans(m.query), func(span [2]int) bool {
					return strings.EqualFold(m.query[span[0]:span[1]], binding.value)
				}) {
					value = ""
				}
				m.query = setQueryField(m.query, "is", value)
			case "b":
				m.query = toggleTerm(m.query, hideDrafts)
			default:
				text := setQueryField(m.query, binding.value, "")
				m.editQuery(strings.TrimSpace(text+" ") + " " + binding.value + ":")
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
			switch binding.value {
			case "fold", "fold-open", "fold-close":
				return true, m.fold(binding.value)
			case "fold-all", "unfold-all":
				m.folded = nil
				m.foldAll = binding.value == "fold-all"
				m.serial++
				return true, m.prepare()
			case "scroll-top", "scroll-center", "scroll-bottom":
				m.scrollTo(binding.value)
				return true, nil
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

// fold toggles, opens or closes the selected group, or the selected branch in
// a tree.
func (m *Model) fold(how string) tea.Cmd {
	if m.selected < 0 || m.selected >= len(m.data.rows) {
		return nil
	}
	r := m.data.rows[m.selected]
	if m.isTree() {
		if !r.branch || how == "fold-open" && !r.collapsed || how == "fold-close" && r.collapsed {
			return nil
		}
		return m.toggleTree()
	}
	if m.ungrouped || m.isBoard() {
		return nil
	}
	// Only a header can be collapsed; an item row's group is open.
	collapsed := r.header != "" && r.collapsed
	if how == "fold-open" && !collapsed || how == "fold-close" && collapsed {
		return nil
	}
	m.folded = &foldState{group: r.value, previous: m.folded}
	m.selected = m.data.starts[r.group]
	m.serial++
	return m.prepare()
}

// scrollTo places the cursor row at the top, center or bottom of the list.
func (m *Model) scrollTo(where string) {
	page := m.page()
	switch where {
	case "scroll-top":
		m.top = m.selected
	case "scroll-center":
		m.top = m.selected - page/2
	case "scroll-bottom":
		m.top = m.selected - page + 1
	}
	m.top = max(0, min(m.top, max(0, len(m.data.rows)-page)))
}
