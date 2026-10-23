package tui

import (
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

var keyGlyphs = map[string]string{"down": "↓", "up": "↑", "left": "←", "right": "→", "pagedown": "pgdn", "pageup": "pgup"}

// keyText shows bindings compactly: g g reads gg, and arrows are glyphs.
func keyText(bindings []string) string {
	parts := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		tokens := strings.Fields(binding)
		single := true
		for i, token := range tokens {
			if glyph, ok := keyGlyphs[token]; ok {
				tokens[i] = glyph
			}
			single = single && len([]rune(tokens[i])) == 1
		}
		separator := " "
		if single {
			separator = ""
		}
		parts = append(parts, strings.Join(tokens, separator))
	}
	return strings.Join(parts, " ")
}

// launcherOverlay draws help over the whole body and the palette or go-to
// list as a centered box.
func (m Model) launcherOverlay(lines []string) []string {
	if m.launcher.help {
		out := append([]string(nil), lines...)
		body := m.helpBody(m.width, m.helpRows())
		copy(out[1:], body)
		return out
	}
	t := m.theme
	title := "Commands"
	if m.launcher.goTo {
		title = "Go to"
	}
	width := min(max(40, m.width*3/5), 88, m.width-2)
	inner := width - 4
	visible := max(1, min(14, m.height-7))
	body := []string{t.accent.render("›") + " " + textutil.Line(m.launcher.query) + t.accent.render("▏"), ""}
	switch {
	case m.launcher.loading && len(m.launcher.rows) == 0:
		body = append(body, t.muted.render("Searching…"))
	case m.launcher.err != "":
		body = append(body, t.red.render("! "+m.launcher.err))
	case len(m.launcher.rows) == 0:
		body = append(body, t.muted.render("No matches"))
	}
	start := max(0, min(m.launcher.cursor-visible/2, len(m.launcher.rows)-visible))
	for i := start; i < min(len(m.launcher.rows), start+visible); i++ {
		body = append(body, m.launcherLine(m.launcher.rows[i], i == m.launcher.cursor, inner))
	}
	if m.launcher.err != "" && len(m.launcher.rows) > 0 {
		body = append(body, "", t.red.render("! "+m.launcher.err))
	}
	return m.modal(lines, title, body, width, len(body)+2)
}

func (m Model) launcherLine(r launcherRow, selected bool, width int) string {
	t := m.theme
	var left, right string
	if r.item != nil {
		item := r.item
		right = t.muted.render(m.statusName(item.Status))
		left = t.categoryIcon(item.Category) + " " + t.muted.render(textutil.Line(item.Ref.ID)) + " " + textutil.Line(item.Title)
	} else {
		left = textutil.Line(r.label)
		right = t.accent.render(keyText(r.action.bindings))
		if r.reason != "" {
			left, right = t.muted.render(left), t.faint.render("disabled: "+textutil.Line(r.reason))
		}
	}
	room := width - 2
	if w := ansi.StringWidth(right); w > room/2 {
		right = ansi.Truncate(right, room/2, "…")
	}
	left = ansi.Truncate(left, max(1, room-ansi.StringWidth(right)-2), "…")
	line := "  " + left + blank(room-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
	if selected {
		return t.selected(t.accent.render("▌") + line[1:])
	}
	return line
}

// helpRows is the help page's height: the frame between header and status bar.
func (m Model) helpRows() int { return max(0, m.height-2) }

// helpSection is one group of the key catalog on the help page.
type helpSection struct {
	name  string
	lines []launcherRow
}

func (s helpSection) height() int { return len(s.lines) + 2 } // header and gap

// helpLayout groups the catalog and picks the column count and the smallest
// column height (limit) that keeps groups whole and in reading order.
func helpLayout(rows []launcherRow, width int) (sections []helpSection, columns, limit int) {
	for _, r := range rows {
		if len(sections) == 0 || sections[len(sections)-1].name != r.group {
			sections = append(sections, helpSection{name: r.group})
		}
		s := &sections[len(sections)-1]
		s.lines = append(s.lines, r)
	}
	columns = max(1, min(4, (width-2)/38))
	total := 0
	for _, s := range sections {
		total += s.height()
	}
	fits := func(limit int) bool {
		column, used := 0, 0
		for _, s := range sections {
			if used > 0 && used+s.height() > limit {
				column, used = column+1, 0
			}
			used += s.height()
		}
		return column < columns
	}
	limit = (total + columns - 1) / columns
	for !fits(limit) {
		limit++
	}
	return sections, columns, limit
}

// helpScrollMax is the help page's last scroll offset, which puts the final
// group's last line on the bottom row.
func helpScrollMax(limit, rows int) int { return max(0, limit-1-(rows-2)) }

// helpBody lays the grouped key catalog out in columns. Groups fill columns in
// reading order with the smallest even height; the page scrolls vertically.
func (m Model) helpBody(width, rows int) []string {
	t := m.theme
	out := make([]string, rows)
	for i := range out {
		out[i] = blank(width)
	}
	if rows == 0 {
		return out
	}
	out[0] = fit(" "+t.title.render("Keys")+"  "+t.accent.render("›")+" "+textutil.Line(m.launcher.query)+t.accent.render("▏"), width)
	sections, columns, limit := helpLayout(m.launcher.rows, width)
	if len(sections) == 0 {
		if rows > 2 {
			out[2] = fit("  "+t.muted.render("No matches"), width)
		}
		return out
	}
	keyWidth := 1
	for _, r := range m.launcher.rows {
		keyWidth = max(keyWidth, min(14, ansi.StringWidth(keyText(r.action.bindings))))
	}
	columnWidth := (width - 2) / columns
	grid := make([][]string, columns)
	column, used := 0, 0
	for _, s := range sections {
		if used > 0 && used+s.height() > limit {
			column, used = column+1, 0
		}
		grid[column] = append(grid[column], cell(" "+s.name, columnWidth))
		grid[column][len(grid[column])-1] = t.bold.render(grid[column][len(grid[column])-1])
		for _, r := range s.lines {
			keys := t.accent.render(cell(keyText(r.action.bindings), keyWidth))
			text := strings.TrimPrefix(r.action.label, s.name+": ")
			label := ansi.Truncate(textutil.Line(text), max(1, columnWidth-keyWidth-4), "…")
			if r.reason != "" {
				keys, label = t.faint.render(cell(keyText(r.action.bindings), keyWidth)), t.muted.render(label)
			}
			grid[column] = append(grid[column], fit(" "+keys+"  "+label, columnWidth))
		}
		grid[column] = append(grid[column], blank(columnWidth))
		used += s.height()
	}
	visible := rows - 2
	last := helpScrollMax(limit, rows)
	scroll := min(m.launcher.cursor, last)
	for i := 0; i < visible; i++ {
		var b strings.Builder
		b.WriteString(" ")
		for _, lines := range grid {
			if line := scroll + i; line < len(lines) {
				b.WriteString(lines[line])
			} else {
				b.WriteString(blank(columnWidth))
			}
		}
		out[2+i] = fit(b.String(), width)
	}
	if visible > 0 && scroll < last {
		out[rows-1] = fit(blank(width-3)+t.accent.render("↓"), width)
	}
	return out
}

// longEditOverlay asks how to resolve an external edit that cannot be saved.
func (m Model) longEditOverlay(lines []string) []string {
	t := m.theme
	choice := func(key, label string) string { return t.accent.render(key) + "  " + label }
	title := "Edit conflict"
	body := []string{textutil.Line(m.longEdit.ref.ID) + " changed while you were editing.", "", choice("y", "Keep yours"), choice("t", "Keep theirs"), choice("r", "Reopen with both")}
	if m.longEdit.choice == "invalid" {
		title = "Invalid edit"
		body = []string{t.red.render(textutil.Line(m.notice)), "", choice("r", "Reopen editor"), choice("d", "Discard changes")}
	}
	width := min(64, m.width-2)
	return m.modal(lines, title, body, width, len(body)+2)
}

// captureOverlay is quick-add as a bottom sheet and the full form as a box.
func (m Model) captureOverlay(lines []string) []string {
	t := m.theme
	c := m.capture
	if c.choosing {
		body := make([]string, 0, len(c.sources))
		for i, id := range c.sources {
			if i == c.cursor {
				body = append(body, t.selected(fit(t.accent.render("▌")+" "+textutil.Line(id), m.width)))
				continue
			}
			body = append(body, "  "+textutil.Line(id))
		}
		return m.drawSheet(lines, m.sheet("New item in", body, m.width))
	}
	title := "New item"
	if c.child {
		title = "New child"
	}
	if c.source != "" && len(m.opts.Sources) > 1 {
		title += " · " + textutil.Line(c.source)
	}
	if !c.full {
		body := []string{t.accent.render("›") + " " + textutil.Line(c.line) + t.accent.render("▏"), "  " + m.captureChips()}
		if c.errorText != "" {
			body[1] = "  " + t.red.render("! "+textutil.Line(c.errorText))
		}
		return m.drawSheet(lines, m.hintSheet(title, "tab form", body, m.width))
	}
	if c.version != "" {
		title = "Edit " + textutil.Line(c.original.Ref.ID)
	}
	width := min(96, m.width-2)
	height := min(len(c.fields)+2, m.height-2)
	if c.errorText != "" {
		height = min(len(c.fields)+4, m.height-2)
	}
	visible := max(1, height-2)
	if c.errorText != "" {
		visible = max(1, visible-2)
	}
	nameWidth := 12
	start := max(0, min(c.cursor-visible/2, len(c.fields)-visible))
	var body []string
	for i := start; i < min(len(c.fields), start+visible); i++ {
		f := c.fields[i]
		name := t.muted.render(cellRight(strings.ReplaceAll(f.name, "_", " "), nameWidth))
		value := textutil.Line(f.value)
		if i == c.cursor {
			name = t.accent.render(cellRight(strings.ReplaceAll(f.name, "_", " "), nameWidth))
			value += t.accent.render("▏")
		}
		body = append(body, name+"  "+value)
	}
	if c.errorText != "" {
		body = append(body, "", t.red.render("! "+textutil.Line(c.errorText)))
	}
	return m.modal(lines, title, body, width, height)
}

// captureChips previews what quick-add parsed, so the token syntax is learned
// by typing. An empty line shows the available tokens.
func (m Model) captureChips() string {
	t := m.theme
	line := m.capture.line
	if strings.TrimSpace(line) == "" {
		return t.faint.render("!priority  @assignee  #label  ~milestone  :type  ^parent  <blocker")
	}
	item, err := query.QuickAdd(line, m.opts.Me)
	if err != nil {
		item, err = query.QuickAdd("x "+line, m.opts.Me)
	}
	if err != nil {
		return t.yellow.render(textutil.Line(err.Error()))
	}
	var chips []string
	if item.Priority != "" {
		chips = append(chips, t.priority(item.Priority).render(priorityGlyph(item.Priority)+" "+string(item.Priority)))
	}
	for _, a := range item.Assignees {
		chips = append(chips, t.accent.render("@"+textutil.Line(a)))
	}
	for _, l := range item.Labels {
		chips = append(chips, t.magenta.render("#"+textutil.Line(l)))
	}
	if item.Milestone != "" {
		chips = append(chips, t.cyan.render("~"+textutil.Line(item.Milestone)))
	}
	if item.Type != "" {
		chips = append(chips, t.muted.render(":"+textutil.Line(item.Type)))
	}
	if item.Parent != nil {
		chips = append(chips, t.muted.render("^ "+textutil.Line(refLabel(*item.Parent))))
	}
	for _, ref := range item.BlockedBy {
		chips = append(chips, t.red.render("⊘ "+textutil.Line(refLabel(ref))))
	}
	return strings.Join(chips, "  ")
}

func refLabel(ref model.Ref) string {
	if ref.Source == "" {
		return ref.ID
	}
	return ref.Source + ":" + ref.ID
}
