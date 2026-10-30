package tui

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// detailPad is the detail pane's left and right margin.
const detailPad = 2

// View paints fixed rows: header, body (list, board, tree or detail) and status
// bar, with a blank row inside each bar on a tall terminal. Overlays draw over the body, so nothing beneath them shifts.
func (m Model) View() tea.View {
	v := tea.NewView(strings.Join(m.frame(), "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) frame() []string {
	lines := make([]string, 0, m.height)
	spacer := make([]string, m.gap())
	for i := range spacer {
		spacer[i] = blank(m.width)
	}
	lines = append(lines, m.headerLine())
	lines = append(lines, spacer...)
	lines = append(lines, m.body(m.bodyRows())...)
	lines = append(lines, spacer...)
	lines = append(lines, m.statusBar())
	lines = m.overlay(lines)
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	return lines
}

// heldFrame is the list as it was before a view switch. It stays on screen
// until the new rows are ready, or slow enough to show as loading, so a
// switch paints once.
type heldFrame struct {
	body     []string
	position string
}

func (m Model) holding() bool { return m.held != nil && !m.loads[listLoad].shown }

func (m Model) body(rows int) []string {
	switch {
	case m.holding():
		return m.held.body
	case m.split():
		left, right := m.listPane(m.listWidth(), rows), m.detailPane(m.detailWidth(), rows)
		divider := m.theme.faint.render("│")
		out := make([]string, rows)
		for i := range out {
			out[i] = left[i] + divider + right[i]
		}
		return out
	case m.detailOpen:
		return m.detailPane(m.width, rows)
	}
	return m.listPane(m.width, rows)
}

func blank(width int) string { return strings.Repeat(" ", max(0, width)) }

// center places styled text in the middle of width cells.
func center(s string, width int) string {
	w := ansi.StringWidth(s)
	if w >= width {
		return fit(s, width)
	}
	left := (width - w) / 2
	return blank(left) + s + blank(width-w-left)
}

// listPending reports that the list has no current rows to show yet.
func (m Model) listPending() bool {
	return m.loading || m.failure == "" && len(m.data.rows) == 0 && (m.applied != m.request || m.prepared != m.serial || m.syncing)
}

// unsettled reports that the list has nothing to show yet, not even a
// previous answer.
func (m Model) unsettled() bool {
	return m.loading || m.failure == "" && len(m.data.rows) == 0 && m.syncing
}

// listMessage reports what replaces the rows, if anything. Pending rows are
// blank until the wait is long enough to say loading, never empty; a query
// being answered keeps its last empty result meanwhile.
func (m Model) listMessage() (string, bool) {
	t := m.theme
	switch {
	case m.loads[listLoad].shown:
		return t.muted.render("Loading…"), true
	case m.unsettled():
		return "", true
	case m.failure != "":
		return t.red.render("! " + textutil.Line(m.failure)), true
	case len(m.data.rows) > 0:
		return "", false
	}
	return t.muted.render("No matching items"), true
}

func (m Model) listPane(width, rows int) []string {
	out := make([]string, rows)
	if rows == 0 {
		return out
	}
	message, replaced := m.listMessage()
	if m.isBoard() && !replaced && len(m.data.starts) > 0 {
		return m.boardPane(width, rows)
	}
	l := m.layout(width)
	out[0] = blank(width)
	if !m.isBoard() {
		out[0] = m.columnHeader(l)
	}
	if replaced {
		for i := 1; i < rows; i++ {
			out[i] = blank(width)
		}
		// A one-row pane shows the message instead of the column header.
		out[min(rows-1, max(1, rows/2))] = center(message, width)
		return out
	}
	for i := 1; i < rows; i++ {
		if index := m.top + i - 1; index < len(m.data.rows) {
			out[i] = m.listRow(index, l)
		} else {
			out[i] = blank(width)
		}
	}
	return out
}

// detailBanner reports a failed edit above the scrolled content.
func (m Model) detailBanner() []string {
	row, ok := m.selectedRow()
	if !ok || row.Error == "" {
		return nil
	}
	t := m.theme
	lines := []string{t.failed.render("! " + textutil.Line(row.Error))}
	if row.ErrorLocalVersion != "" || row.ErrorRemoteVersion != "" {
		lines = append(lines, t.muted.render("local "+textutil.Line(row.ErrorLocalVersion)+" · source "+textutil.Line(row.ErrorRemoteVersion)))
	}
	return append(lines, "")
}

// detailVisible is the number of scrolled content rows below the detail
// header (meta, title, tabs and a gap) and any failure banner.
func (m Model) detailVisible() int {
	return max(1, m.bodyRows()-m.detailBodyTop())
}

// detailBodyTop is the pane row of the first body line, below the meta,
// title and tab rows, a gap, and any banners.
func (m Model) detailBodyTop() int { return 4 + len(m.detailBanner()) }

// detailPlaceholder reports that the body is replaced while it loads.
func (m Model) detailPlaceholder() bool {
	return m.detailMasked || m.detailLoading && len(m.detailLines) == 0
}

// detailCurrent reports that the body on screen is the selected item's.
// Another item's body, kept while this one loads, takes no cursor or clicks.
func (m Model) detailCurrent() bool {
	return m.detailIdentity.Valid && m.detailError == "" && !m.detailPlaceholder() && (!m.detailLoading || m.detailShownRef == m.detailIdentity.Ref)
}

func (m Model) detailMeta(row engine.Row) string {
	t := m.theme
	parts := []string{t.muted.render(textutil.Line(row.Ref.ID))}
	if row.Status != "" {
		parts = append(parts, t.categoryIcon(row.Category)+" "+t.category(row.Category).render(m.statusName(row.Status, row.Names)))
	}
	if row.Priority != "" {
		parts = append(parts, t.priority(row.Priority).render(priorityGlyph(row.Priority)+" "+textutil.Line(string(row.Priority))))
	}
	if label, remaining := claimLease(row, m.now()); label != "" {
		parts = append(parts, t.claim(label).render(strings.TrimSpace(textutil.Line(label)+" "+remaining)))
	}
	if row.Pending {
		parts = append(parts, t.pending.render("● saving"))
	}
	if row.Error != "" {
		parts = append(parts, t.failed.render("! failed"))
	}
	return strings.Join(parts, "  ")
}

// claimLease is the holder and remaining lease, such as "mine" and "12m". It
// reads the clock, not the overlay, so a lapse shows before the next rebuild.
func claimLease(row engine.Row, now time.Time) (label, remaining string) {
	if row.Claim == "" {
		return "", ""
	}
	if !row.ClaimExpires.After(now) {
		return "expired", ""
	}
	remaining = age(now, row.ClaimExpires)
	if remaining == "now" {
		remaining = "<1m"
	}
	return row.Claim, remaining
}

// detailTabBar renders the tabs with counts and their spans relative to the
// pane's inner left edge. Too narrow for all tabs, it shows the active one.
func (m Model) detailTabBar(width int) (string, []span) {
	t := m.theme
	row, _ := m.selectedRow()
	counts := tabCounts(row)
	labels := make([]string, len(detailTabNames))
	total := 0
	for i, name := range detailTabNames {
		label := name
		if count := counts[i]; count != "" {
			label += " " + count
		}
		labels[i] = label
		total += ansi.StringWidth(label) + 2
	}
	if total > width {
		active := labels[m.detailTab]
		text := t.faint.render("‹ ") + t.bold.render(active) + t.faint.render(" ›")
		return text, nil
	}
	var b strings.Builder
	spans := make([]span, len(labels))
	x := 0
	for i, label := range labels {
		cell := " " + label + " "
		switch {
		case detailTab(i) != m.detailTab:
			b.WriteString(t.muted.render(cell))
		case !t.color:
			cell = "[" + label + "]"
			b.WriteString(cell)
		case m.detailFocus:
			b.WriteString(t.tab.render(cell))
		default:
			b.WriteString(t.bold.render(cell))
		}
		w := ansi.StringWidth(cell)
		spans[i] = span{x, x + w}
		x += w
	}
	return b.String(), spans
}

func (m Model) detailPane(width, rows int) []string {
	t := m.theme
	out := make([]string, rows)
	pad := blank(detailPad)
	inner := max(1, width-2*detailPad)
	put := func(i int, s string) {
		if i < rows {
			out[i] = fit(pad+s, width)
		}
	}
	for i := range out {
		out[i] = blank(width)
	}
	row, ok := m.selectedRow()
	if !ok {
		if rows > 0 {
			out[rows/2] = center(t.muted.render("No item selected"), width)
		}
		return out
	}
	meta := m.detailMeta(row)
	visible := max(1, rows-m.detailBodyTop())
	placeholder := m.detailPlaceholder()
	var right []string
	// A reload keeps the body and says so leftmost, so the right-aligned
	// labels never move; it is the first dropped when the row is full.
	if m.loads[detailLoad].shown && !placeholder {
		right = append(right, "Loading…")
	}
	if n := len(m.detailLines); n > visible && !placeholder {
		right = append(right, strconv.Itoa(min(100, (m.detailScroll+visible)*100/n))+"%")
	}
	// A capped body says so where it is always visible, not only at its end.
	// The meta row has room; the tab row may be full.
	if m.detailFocus && m.detached != nil && len(m.detailHistory) > 0 {
		right = append(right, "⌫ back")
	}
	if m.detailExpanded {
		right = append(right, "m less")
	} else if m.detailTruncated {
		right = append(right, "m more")
	}
	for len(right) > 0 {
		text := strings.Join(right, " · ")
		if gap := inner - ansi.StringWidth(meta) - ansi.StringWidth(text); gap >= 2 {
			meta += blank(gap) + t.faint.render(text)
			break
		}
		right = right[1:]
	}
	put(0, meta)
	title := textutil.Line(row.Title)
	put(1, t.bold.render(ansi.Truncate(title, inner, "…")))
	tabs, _ := m.detailTabBar(inner)
	put(2, tabs)
	line := 4
	for _, banner := range m.detailBanner() {
		put(line, banner)
		line++
	}
	switch {
	case !m.detailIdentity.Valid:
		put(line, t.muted.render("No item selected"))
	case m.detailError != "":
		put(line, t.red.render("! "+m.detailError))
	case placeholder:
		if m.detailMasked {
			put(line, t.muted.render("Loading…"))
		}
	default:
		cursor, hint := -1, ""
		if m.detailCurrent() && (m.detailTab == tabCriteria || m.detailTab == tabRelations || m.detailTab == tabHistory) && m.entryCursor >= 0 && m.entryCursor < len(m.detailEntries) {
			cursor = m.detailEntries[m.entryCursor]
			// A row-specific key shows only on the focused row, so the list
			// stays clean while the key is still found where it applies.
			if m.detailFocus && m.entryCursor < len(m.detailHints) {
				hint = m.detailHints[m.entryCursor]
			}
		}
		for i := m.detailScroll; i < len(m.detailLines) && line < rows; i++ {
			if i == cursor {
				bar := t.faint.render("▌")
				if m.detailFocus {
					bar = t.accent.render("▌")
				}
				text := m.highlightSelection(i, m.highlightFind(m.detailLines[i]))
				if hint != "" {
					// History rows keep room for their hint; truncation is a guard.
					text = ansi.Truncate(text, max(0, inner-hintWidth(hint)), "…")
					text += blank(inner-ansi.StringWidth(text)-ansi.StringWidth(hint)) + t.faint.render(hint)
				}
				out[line] = t.selected(fit(bar+blank(detailPad-1)+text, width))
			} else {
				put(line, m.highlightSelection(i, m.highlightFind(m.detailLines[i])))
			}
			line++
		}
		if m.detailTruncated && line < rows {
			more := "m more"
			if m.detailExpanded {
				more = "m less"
			}
			put(line, t.faint.render("… "+more))
		}
	}
	return out
}

func (m Model) archivePromptText() string {
	t := m.theme
	target := "marked items"
	if !m.visual && (m.marks == nil || m.marks.size() == 0) {
		if row, ok := m.selectedRow(); ok {
			target = textutil.Line(row.Ref.ID) + " " + strconv.Quote(ansi.Truncate(textutil.Line(row.Title), 40, "…"))
		}
	}
	return t.yellow.render("Archive "+target+"?") + t.muted.render("  y/n")
}

// overlay draws the active transient surface over the composed frame.
func (m Model) overlay(lines []string) []string {
	switch {
	case m.launcher.open:
		return m.launcherOverlay(lines)
	case m.longEdit.choice != "":
		return m.longEditOverlay(lines)
	case m.capture.open:
		return m.captureOverlay(lines)
	case m.picker.open:
		return m.drawSheet(lines, m.pickerSheet())
	case m.editor.open:
		return m.drawSheet(lines, m.completionSheet())
	}
	return m.drawSheet(lines, m.whichKeySheet())
}

// drawSheet places a bottom sheet directly above the status bar.
func (m Model) drawSheet(lines, sheet []string) []string {
	last := len(lines) - 2
	if len(sheet) == 0 || last < 1 {
		return lines
	}
	sheet = sheet[max(0, len(sheet)-last):]
	out := append([]string(nil), lines...)
	copy(out[last-len(sheet)+1:], sheet)
	return out
}

// maxSheet keeps the header and some context visible above a sheet.
func (m Model) maxSheet() int { return max(3, (m.height-2)*2/3) }

type keyHint struct{ key, label string }

// prefixNames title the which-key sheet of a pending key prefix.
var prefixNames = map[string]string{"f": "Filter", "o": "Sort", "z": "Group", "y": "Copy", "g": "Go", "d": "Delete"}

// grid lays hints out column-major in as many columns as fit.
func (m Model) grid(hints []keyHint, width int) []string {
	t := m.theme
	if len(hints) == 0 {
		return nil
	}
	keyWidth, cellWidth := 0, 0
	for _, h := range hints {
		keyWidth = max(keyWidth, ansi.StringWidth(h.key))
	}
	for _, h := range hints {
		cellWidth = max(cellWidth, keyWidth+2+ansi.StringWidth(h.label))
	}
	cellWidth = min(cellWidth+4, max(1, width-2))
	columns := max(1, (width-2)/cellWidth)
	height := (len(hints) + columns - 1) / columns
	lines := make([]string, height)
	for i, h := range hints {
		r := i % height
		text := t.accent.render(cellRight(h.key, keyWidth)) + "  " + ansi.Truncate(textutil.Line(h.label), max(1, cellWidth-keyWidth-4), "…")
		lines[r] += fit(text, cellWidth)
	}
	for i := range lines {
		lines[i] = fit("  "+lines[i], width)
	}
	return lines
}

func (m Model) whichKeySheet() []string {
	var title string
	var hints []keyHint
	if menu, ok := queryMenus[m.prefix]; ok {
		title = map[string]string{"f": "Filter", "o": "Sort", "z": "Group"}[m.prefix]
		for _, b := range menu {
			hints = append(hints, keyHint{b.key, b.label})
		}
	} else if m.keyPrefix != "" {
		title = m.keyPrefix
		if name, ok := prefixNames[m.keyPrefix]; ok {
			title = name
		}
		for _, a := range m.keymap {
			for _, binding := range a.bindings {
				if suffix, ok := strings.CutPrefix(binding, m.keyPrefix+" "); ok {
					hints = append(hints, keyHint{keyText([]string{suffix}), strings.TrimPrefix(a.label, title+": ")})
				}
			}
		}
	} else {
		return nil
	}
	body := m.grid(hints, m.width)
	return m.sheet(title, body[:min(len(body), m.maxSheet()-1)], m.width)
}

// completionWindow is the range of candidates on screen, around the
// highlighted one.
func (m Model) completionWindow() (start, end int) {
	e := m.editor
	focus := 0
	if e.highlighted() {
		focus = e.candidate
	}
	limit := min(8, m.maxSheet()-1)
	start = max(0, min(focus-limit/2, len(e.candidates)-limit))
	return start, min(len(e.candidates), start+limit)
}

// completionSheet offers candidates for the query being typed, and keeps its
// error or warning in view directly above the input.
func (m Model) completionSheet() []string {
	t := m.theme
	e := m.editor
	var problem []string
	switch {
	case m.queryError != "":
		problem = []string{fit(" "+t.red.render("! "+textutil.Line(m.queryError)), m.width)}
	case m.queryWarning != "":
		problem = []string{fit(" "+t.yellow.render("\u26a0 "+textutil.Line(m.queryWarning)), m.width)}
	}
	if len(e.candidates) == 0 {
		return problem
	}
	title := "Fields"
	if e.field != "" {
		title = textutil.Line(e.field)
	}
	start, end := m.completionWindow()
	var body []string
	for i := start; i < end; i++ {
		c := e.candidates[i]
		// Lead with the name, as pickers do; the muted value is what inserts.
		text := "  " + textutil.Line(c.Value)
		if c.Name != "" && c.Name != c.Value {
			text = "  " + textutil.Line(c.Name) + "  " + t.muted.render(textutil.Line(c.Value))
		}
		if i == e.candidate && e.highlighted() {
			text = t.accent.render("▌") + text[1:]
			body = append(body, t.selected(fit(text, m.width)))
			continue
		}
		body = append(body, text)
	}
	return append(m.sheet(title, body, m.width), problem...)
}

// targetLabel names what an edit applies to: marked items or the selection.
func (m Model) targetLabel() string {
	if m.marks != nil && m.marks.size() > 0 {
		return strconv.Itoa(m.marks.size()) + " marked"
	}
	if m.visual {
		return "range"
	}
	if row, ok := m.selectedRow(); ok {
		return textutil.Line(row.Ref.ID)
	}
	return ""
}

// pickerChoices reports whether the picker sheet lists choices: below its
// title and input, from pickerWindow's start.
func (m Model) pickerChoices() bool {
	p := m.picker
	return !p.inputOnly && !p.loading && len(p.filtered) > 0
}

// pickerWindow is the range of choices on screen, around the cursor.
func (m Model) pickerWindow() (start, end int) {
	p := m.picker
	limit := max(1, min(10, m.maxSheet()-2))
	start = max(0, min(p.cursor-limit/2, len(p.filtered)-limit))
	return start, min(len(p.filtered), start+limit)
}

func (m Model) pickerSheet() []string {
	t := m.theme
	p := m.picker
	title := textutil.Line(p.title)
	if target := m.targetLabel(); target != "" {
		title += " · " + target
	}
	input := t.accent.render("›") + " " + p.query.view(t.accent.render("▏"))
	body := []string{input}
	if p.inputOnly {
		return m.sheet(title, body, m.width)
	}
	switch {
	case p.loading:
		body = append(body, t.muted.render("  Loading…"))
	case len(p.filtered) == 0 && p.free && p.query.text != "":
		body = append(body, "  "+t.accent.render("+")+" "+textutil.Line(p.query.text))
	case len(p.filtered) == 0:
		body = append(body, t.muted.render("  No matches"))
	default:
		start, end := m.pickerWindow()
		for i := start; i < end; i++ {
			choice := p.filtered[i]
			mark := ""
			switch {
			case p.multi && p.selected[choice.value]:
				mark = t.green.render("☑") + " "
			case p.multi:
				mark = t.muted.render("☐") + " "
			}
			label := textutil.Line(choice.label)
			if p.kind == pickerStatus {
				label = t.categoryIcon(m.statusCategory(choice.value)) + " " + label
			}
			if choice.hint != "" {
				label += "  " + t.muted.render(textutil.Line(choice.hint))
			}
			if choice.current && !p.multi {
				label += "  " + t.muted.render("current")
			}
			text := "  " + mark + label
			if i == p.cursor {
				body = append(body, t.selected(fit(t.accent.render("▌")+" "+mark+label, m.width)))
				continue
			}
			body = append(body, text)
		}
	}
	hint := ""
	switch {
	case p.kind == pickerBlocker && !p.remove:
		hint = "d remove"
	case p.multi:
		hint = "space toggle"
	}
	return m.hintSheet(title, hint, body, m.width)
}
