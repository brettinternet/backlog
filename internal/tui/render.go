package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// Screen geometry: row 0 is the header, row 1 the column header (or the
// detail header), and the last row the status bar. Transient state never adds
// or removes rows, so content does not shift while sources load or edits land.
const bodyTop = 2

func (m Model) page() int   { return max(1, m.height-3) }
func (m Model) split() bool { return m.detailOpen && m.width >= 120 }

// cell sanitizes and clips provider text to exactly width terminal cells.
func cell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(textutil.Line(s), width, "…")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}
func cellRight(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(textutil.Line(s), width, "…")
	return strings.Repeat(" ", max(0, width-ansi.StringWidth(s))) + s
}

// fit clips already styled text to exactly width cells without stripping it.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if w := ansi.StringWidth(s); w > width {
		s = ansi.Truncate(s, width, "…")
	}
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}
func textWidth(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 || s[i] < 0x20 {
			return ansi.StringWidth(s)
		}
	}
	return len(s)
}

// statusName is a status's sanitized display name: its configured name, or
// the provider's raw value.
func (m Model) statusName(key string) string {
	for _, s := range m.opts.Statuses {
		if s.Key == key && s.Name != "" {
			return textutil.Line(s.Name)
		}
	}
	return textutil.Line(key)
}

// statusCategory is a configured status's category, for icons.
func (m Model) statusCategory(key string) model.StatusCategory {
	for _, s := range m.opts.Statuses {
		if s.Key == key {
			return model.StatusCategory(s.Category)
		}
	}
	return ""
}

func statusNames(statuses []project.StatusConfig) map[string]string {
	names := make(map[string]string, len(statuses))
	for _, s := range statuses {
		if s.Name != "" {
			names[s.Key] = s.Name
		}
	}
	return names
}

func (m Model) now() time.Time {
	if m.opts.Now != nil {
		return m.opts.Now()
	}
	return time.Now()
}

// age is a compact relative time for scanning, not an exact timestamp.
func age(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d < 14*24*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	case d < 60*24*time.Hour:
		return strconv.Itoa(int(d/(7*24*time.Hour))) + "w"
	case d < 365*24*time.Hour:
		return strconv.Itoa(int(d/(30*24*time.Hour))) + "mo"
	}
	return strconv.Itoa(int(d/(365*24*time.Hour))) + "y"
}

// widths are measured over every prepared row on the worker, so columns stay
// put while scrolling instead of resizing to whatever is visible.
type widths struct {
	id, status, assignee, claim, project, source int
	blockers, children                           bool // any row has a value
}

func measure(rows []screenRow, names map[string]string) widths {
	w := widths{id: 2}
	for i := range rows {
		r := &rows[i].row
		if rows[i].header != "" {
			continue
		}
		w.id = max(w.id, textWidth(r.Ref.ID))
		name := r.Status
		if n, ok := names[name]; ok {
			name = n
		}
		w.status = max(w.status, textWidth(name))
		if len(r.Assignees) > 0 {
			extra := 0
			if len(r.Assignees) > 1 {
				extra = 3
			}
			w.assignee = max(w.assignee, textWidth(r.Assignees[0])+extra)
		}
		w.blockers = w.blockers || r.OpenBlockers > 0
		w.children = w.children || r.ChildrenTotal > 0
		w.claim = max(w.claim, textWidth(r.Claim))
		w.project = max(w.project, textWidth(r.Project))
		w.source = max(w.source, textWidth(r.Source))
	}
	w.id = min(w.id, 14)
	w.status = min(w.status, 16)
	w.assignee = min(w.assignee, 14)
	w.claim = min(w.claim, 18)
	w.project = min(w.project, 18)
	w.source = min(w.source, 12)
	return w
}

type columnKind uint8

const (
	colPriority columnKind = iota
	colID
	colStatus
	colTitle
	colBlockers
	colChildren
	colClaim
	colAssignee
	colUpdated
	colProject
	colSource
)

type listColumn struct {
	kind         columnKind
	x, width     int
	right        bool
	label, field string // header label and sortable query field
	name         bool   // status column includes the status name
}
type listLayout struct {
	width   int
	columns []listColumn
}

// gutter holds the cursor bar and the mark; both are glyphs, not just color.
const gutter = 3

// layout fits the view's columns to width. Title takes the remaining space;
// secondary columns drop in a fixed order, so narrow panes stay scannable.
func (m Model) layout(width int) listLayout {
	w := m.data.widths
	coordination := false
	if c, _ := engine.Coordination(m.opts.Context); c != nil {
		coordination = true
	}
	statusGrouped := m.shown.Group == "status" && !m.ungrouped && !m.isTree()
	type spec struct {
		kind        columnKind
		width, drop int // drop: lower values are dropped first; 0 never
		right, name bool
	}
	var specs []spec
	add := func(s spec) { specs = append(specs, s) }
	custom := m.views[m.view].Config.Columns
	if m.hasWorkspace() && m.opts.WorkspaceScope {
		add(spec{kind: colProject, width: max(7, w.project), drop: 5})
		add(spec{kind: colSource, width: max(6, w.source), drop: 3})
	}
	if len(custom) > 0 {
		for _, c := range custom {
			switch c {
			case "id":
				add(spec{kind: colID, width: w.id + 2})
			case "priority":
				add(spec{kind: colPriority, width: 2})
			case "status":
				add(spec{kind: colStatus, width: 2 + w.status, name: true, drop: 6})
			case "blockers":
				add(spec{kind: colBlockers, width: 4, right: true, drop: 4})
			case "children":
				add(spec{kind: colChildren, width: 7, right: true, drop: 3})
			case "claim":
				add(spec{kind: colClaim, width: max(5, w.claim), drop: 5})
			case "assignee":
				add(spec{kind: colAssignee, width: max(8, w.assignee), drop: 2})
			case "updated":
				add(spec{kind: colUpdated, width: 7, right: true, drop: 1})
			case "title":
				add(spec{kind: colTitle})
			}
		}
	} else {
		add(spec{kind: colPriority, width: 2})
		add(spec{kind: colID, width: w.id + 2})
		if statusGrouped || w.status == 0 {
			add(spec{kind: colStatus, width: 1})
		} else {
			add(spec{kind: colStatus, width: 2 + w.status, name: true, drop: 6})
		}
		add(spec{kind: colTitle})
		if w.blockers {
			add(spec{kind: colBlockers, width: 4, right: true, drop: 4})
		}
		if w.children {
			add(spec{kind: colChildren, width: 7, right: true, drop: 3})
		}
		if coordination {
			add(spec{kind: colClaim, width: max(5, w.claim), drop: 5})
		}
		if w.assignee > 0 {
			add(spec{kind: colAssignee, width: max(8, w.assignee), drop: 2})
		}
		add(spec{kind: colUpdated, width: 7, right: true, drop: 1})
	}
	hasTitle := false
	for _, s := range specs {
		hasTitle = hasTitle || s.kind == colTitle
	}
	if !hasTitle {
		specs = append(specs, spec{kind: colTitle})
	}
	minTitle := max(16, min(40, width*2/5))
	total := func() int {
		sum := gutter
		for _, s := range specs {
			if s.kind != colTitle {
				sum += s.width + 2
			}
		}
		return sum
	}
	for level := 1; level <= 6 && width-total() < minTitle; level++ {
		kept := specs[:0]
		for _, s := range specs {
			if s.drop == level && s.name && s.kind == colStatus {
				s.width, s.name, s.drop = 1, false, 0
			} else if s.drop == level {
				continue
			}
			kept = append(kept, s)
		}
		specs = kept
	}
	layout := listLayout{width: width}
	x := gutter
	titleWidth := max(1, width-total())
	for _, s := range specs {
		c := listColumn{kind: s.kind, x: x, width: s.width, right: s.right, name: s.name}
		if s.kind == colTitle {
			c.width = titleWidth
		}
		switch s.kind {
		case colPriority:
			c.field = "priority"
		case colID:
			c.label, c.field = "ID", "id"
		case colStatus:
			if s.name {
				c.label = "Status"
			}
			c.field = "status"
		case colTitle:
			c.label, c.field = "Title", "title"
		case colBlockers:
			c.label = "⊘"
		case colChildren:
			c.label = "▣"
		case colClaim:
			c.label = "Claim"
		case colAssignee:
			c.label = "Assignee"
		case colUpdated:
			c.label, c.field = "Updated", "updated"
		case colProject:
			c.label = "Project"
		case colSource:
			c.label = "Source"
		}
		layout.columns = append(layout.columns, c)
		x += c.width + 2
	}
	return layout
}

// columnAt maps a terminal cell to a header column, for click sorting.
func (l listLayout) columnAt(x int) (listColumn, bool) {
	for _, c := range l.columns {
		if x >= c.x && x < c.x+c.width+2 {
			return c, true
		}
	}
	return listColumn{}, false
}

func (m Model) columnHeader(l listLayout) string {
	t := m.theme
	sortField, desc := "", false
	if len(m.shown.Sort) > 0 {
		sortField, desc = m.shown.Sort[0].Field, m.shown.Sort[0].Desc
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", gutter))
	for i, c := range l.columns {
		label := c.label
		if c.kind == colTitle && m.isTree() {
			label = "Title"
		}
		arrow := ""
		if c.field != "" && c.field == sortField {
			arrow = "↓"
			if desc {
				arrow = "↑"
			}
		}
		text := label
		if arrow != "" && c.width >= ansi.StringWidth(label)+2 {
			text = label + " " + arrow
		} else if arrow != "" && label == "" {
			text = arrow
		}
		var styled string
		if c.right {
			styled = cellRight(text, c.width)
		} else {
			styled = cell(text, c.width)
		}
		if arrow != "" {
			styled = t.accent.render(styled)
		} else {
			styled = t.muted.render(styled)
		}
		b.WriteString(styled)
		if i < len(l.columns)-1 {
			b.WriteString("  ")
		}
	}
	return fit(b.String(), l.width)
}

// listRow renders one prepared row. index is a display-row index.
func (m Model) listRow(index int, l listLayout) string {
	t := m.theme
	r := m.data.rows[index]
	selected := index == m.selected
	if r.header != "" {
		return m.groupHeader(r, l.width, selected)
	}
	var b strings.Builder
	cursor, mark := " ", " "
	if selected {
		cursor = m.cursorBar()
	}
	if m.isMarked(index, r.row.Ref) {
		mark = t.mark.render("•")
	}
	b.WriteString(cursor + mark + " ")
	row := r.row
	for i, c := range l.columns {
		b.WriteString(m.rowCell(r, row, c, selected))
		if i < len(l.columns)-1 {
			b.WriteString("  ")
		}
	}
	line := fit(b.String(), l.width)
	if r.context && t.color {
		line = t.dim(line)
	}
	if selected {
		line = t.selected(line)
	}
	return line
}

// cursorBar marks the list selection; it dims while detail has focus, but the
// row the detail describes stays obvious.
func (m Model) cursorBar() string {
	if m.detailFocus {
		return m.theme.faint.render("▌")
	}
	return m.theme.accent.render("▌")
}

func (m Model) rowCell(r screenRow, row engine.Row, c listColumn, selected bool) string {
	t := m.theme
	switch c.kind {
	case colPriority:
		return t.priority(row.Priority).render(cell(priorityGlyph(row.Priority), c.width))
	case colID:
		state := " "
		if row.Pending {
			state = t.pending.render("●")
		}
		if row.Error != "" {
			state = t.failed.render("!")
		}
		return t.muted.render(cell(row.Ref.ID, c.width-2)) + " " + state
	case colStatus:
		icon := t.categoryIcon(row.Category)
		if !c.name {
			return icon
		}
		return icon + " " + t.category(row.Category).render(cell(m.statusName(row.Status), c.width-2))
	case colTitle:
		return m.titleCell(r, row, c.width, selected)
	case colBlockers:
		if row.OpenBlockers == 0 {
			return strings.Repeat(" ", c.width)
		}
		return t.red.render(cellRight("⊘ "+strconv.Itoa(row.OpenBlockers), c.width))
	case colChildren:
		if row.ChildrenTotal == 0 {
			return strings.Repeat(" ", c.width)
		}
		text := cellRight(fmt.Sprintf("▣ %d/%d", row.ChildrenDone, row.ChildrenTotal), c.width)
		if row.ChildrenDone == row.ChildrenTotal {
			return t.green.render(text)
		}
		return t.muted.render(text)
	case colClaim:
		if row.Claim == "" {
			return strings.Repeat(" ", c.width)
		}
		style := t.muted
		if row.Claim == "mine" {
			style = t.accent
		} else if row.Claim == "expired" {
			style = t.yellow
		}
		return style.render(cell(row.Claim, c.width))
	case colAssignee:
		if len(row.Assignees) == 0 {
			return strings.Repeat(" ", c.width)
		}
		name := row.Assignees[0]
		extra := ""
		if len(row.Assignees) > 1 {
			extra = " +" + strconv.Itoa(min(9, len(row.Assignees)-1))
		}
		style := t.muted
		if m.opts.Me != "" && name == m.opts.Me {
			style = t.accent
		}
		return style.render(cell(name, c.width-ansi.StringWidth(extra))) + t.muted.render(extra)
	case colUpdated:
		return t.muted.render(cellRight(age(row.Updated, m.now()), c.width))
	case colProject:
		return t.muted.render(cell(row.Project, c.width))
	case colSource:
		return t.muted.render(cell(row.Source, c.width))
	}
	return strings.Repeat(" ", c.width)
}

// titleCell shows the title first; labels only fill leftover space.
func (m Model) titleCell(r screenRow, row engine.Row, width int, selected bool) string {
	t := m.theme
	prefix := ""
	if m.isTree() {
		prefix = m.treePrefix(r, width)
	}
	used := ansi.StringWidth(ansi.Strip(prefix))
	title := ansi.Truncate(textutil.Line(row.Title), max(0, width-used), "…")
	used += ansi.StringWidth(title)
	base := paint{}
	if selected {
		base = t.selectedTitle
	}
	if row.Category == model.Canceled {
		base = t.muted
	}
	text := prefix + m.highlightText(title, base)
	if len(row.Labels) > 0 && width-used > 4 {
		var labels strings.Builder
		for _, label := range row.Labels {
			labels.WriteString("  #" + textutil.Line(label))
		}
		rest := ansi.Truncate(labels.String(), width-used, "…")
		if strings.TrimSpace(rest) != "…" {
			text += t.faint.render(rest)
			used += ansi.StringWidth(rest)
		}
	}
	return text + strings.Repeat(" ", max(0, width-used))
}

func (m Model) groupHeader(r screenRow, width int, selected bool) string {
	t := m.theme
	glyph := "▾"
	if r.collapsed {
		glyph = "▸"
	}
	cursor := " "
	if selected {
		cursor = m.cursorBar()
	}
	line := cursor + " " + t.muted.render(glyph) + " " + m.groupLabel(r, true) + "  " + t.muted.render(strconv.Itoa(r.count))
	line = fit(line, width)
	if selected {
		line = t.selected(line)
	}
	return line
}

// groupLabel is a group's icon and name; unfocused board columns dim it.
func (m Model) groupLabel(r screenRow, strong bool) string {
	t := m.theme
	label := textutil.Line(r.value)
	icon := ""
	switch r.groupKey {
	case "status":
		label = m.statusName(r.value)
		icon = t.categoryIcon(r.category) + " "
	case "category":
		icon = t.categoryIcon(model.StatusCategory(r.value)) + " "
	case "priority":
		p := model.Priority(r.value)
		icon = t.priority(p).render(priorityGlyph(p)) + " "
	}
	if label == "" {
		label = "None"
	}
	if strong {
		return icon + t.bold.render(label)
	}
	return icon + t.muted.render(label)
}

// highlightText marks free-text query matches inside one sanitized cell,
// restoring the cell's base style after each match.
func (m Model) highlightText(text string, base paint) string {
	if !m.theme.color || len(m.highlights) == 0 {
		return base.render(text)
	}
	runes := []rune(text)
	marked := make([]bool, len(runes))
	any := false
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
				any = true
				for j := range needle {
					marked[i+j] = true
				}
			}
		}
	}
	if !any {
		return base.render(text)
	}
	hl := m.theme.highlight
	var out strings.Builder
	out.WriteString(base.on)
	for i := 0; i < len(runes); {
		end := i + 1
		for end < len(runes) && marked[end] == marked[i] {
			end++
		}
		s := string(runes[i:end])
		if marked[i] {
			out.WriteString(hl.on + s + hl.off + base.on)
		} else {
			out.WriteString(s)
		}
		i = end
	}
	out.WriteString(base.off)
	return out.String()
}

type span struct{ start, end int }

// tabs renders view tabs and reports their cell spans for mouse routing. Tabs
// that overflow scroll so the active one stays visible; ‹ and › mark the rest.
func (m Model) tabs() (string, []span, int) {
	t := m.theme
	scope := m.opts.Project
	if m.hasWorkspace() && m.opts.WorkspaceScope {
		scope = m.opts.Workspace.Workspace.Name
	}
	if scope == "" {
		scope = "backlog"
	}
	scope = textutil.Line(scope)
	if ansi.StringWidth(scope) > 24 {
		scope = ansi.Truncate(scope, 24, "…")
	}
	var b strings.Builder
	b.WriteString(" " + t.title.render(scope) + "  ")
	x := 1 + ansi.StringWidth(scope) + 2
	labels := make([]string, len(m.views))
	for i, v := range m.views {
		name := textutil.Line(v.Config.Name)
		labels[i] = " " + name + " "
		if i == m.view && !t.color {
			labels[i] = "[" + name + "]"
		}
	}
	// strip is the width of labels[from..to] with their separators.
	strip := func(from, to int) int {
		w := 0
		for i := from; i <= to; i++ {
			w += ansi.StringWidth(labels[i]) + 1
		}
		return w - 1
	}
	first := 0
	for first < m.view && 2*min(first, 1)+strip(first, m.view)+2 > m.width-x {
		first++
	}
	if first > 0 {
		b.WriteString(t.faint.render("‹") + " ")
		x += 2
	}
	spans := make([]span, len(m.views))
	for i := first; i < len(m.views); i++ {
		w, last := ansi.StringWidth(labels[i]), i == len(m.views)-1
		reserve := 2 // room for a following › marker
		if last {
			reserve = 0
		}
		if i > m.view && x+w+reserve > m.width {
			b.WriteString(t.faint.render("›"))
			x++
			break
		}
		switch {
		case i == m.view && !t.color:
			b.WriteString(labels[i])
		case i == m.view:
			b.WriteString(t.tab.render(labels[i]))
		default:
			b.WriteString(t.muted.render(labels[i]))
		}
		spans[i] = span{x, x + w}
		x += w
		if !last {
			b.WriteString(" ")
			x++
		}
	}
	return b.String(), spans, x
}

type sourceBadge struct {
	id, state, err string
}

func (m Model) badgeText() string {
	t := m.theme
	if m.opts.TrustWarning != "" {
		return t.yellow.render("⚠ " + textutil.Line(m.opts.TrustWarning))
	}
	parts := make([]string, 0, len(m.sources))
	for _, s := range m.sources {
		id := textutil.Line(s.id)
		switch s.state {
		case "error":
			parts = append(parts, t.red.render("! "+id+": "+textutil.Line(s.err)))
		case "syncing":
			parts = append(parts, t.accent.render("↻ "+id))
		case "partial":
			parts = append(parts, t.yellow.render("~ "+id))
		default:
			parts = append(parts, t.muted.render("✓ "+id))
		}
	}
	return strings.Join(parts, "  ")
}

func (m Model) headerLine() string {
	left, _, used := m.tabs()
	right := m.badgeText()
	room := m.width - used - 2
	if room < 6 || right == "" {
		return fit(left, m.width)
	}
	if ansi.StringWidth(right) > room {
		right = ansi.Truncate(right, room, "…")
	}
	gap := m.width - used - ansi.StringWidth(right) - 1
	return fit(left+strings.Repeat(" ", max(1, gap))+right+" ", m.width)
}

// stale reports a projection or preparation still in flight. It is shown as a
// fixed-width glyph so the status bar never reflows while results arrive.
func (m Model) stale() bool {
	return m.loading || m.applied != m.request || m.prepared != m.serial
}

func (m Model) statusBar() string {
	t := m.theme
	var left string
	switch {
	case m.actionConfirm != nil:
		left = t.yellow.render("Run "+textutil.Line(m.actionConfirm.Name)+" on "+textutil.Line(m.actionConfirm.Invocation.Event.Ref)+"?") + t.muted.render("  y/n")
	case m.archiveConfirm:
		left = m.archivePromptText()
	case m.save.open:
		// tab switches where the view is saved; the other choice is shown.
		destination, other := "user", "project"
		if m.save.project {
			destination, other = "project", "user"
		}
		if m.save.busy {
			left = t.muted.render("Saving view…")
		} else {
			left = t.accent.render("Save view ›") + " " + textutil.Line(m.save.name) + t.accent.render("▏") + "  " + t.muted.render("to "+destination+" · tab "+other)
		}
	case m.editor.open:
		e := m.editor
		left = t.accent.render("/") + " " + textutil.Line(e.text[:e.cursor]) + t.accent.render("▏") + textutil.Line(e.text[e.cursor:])
	case m.queryError != "":
		left = t.red.render("! " + m.queryError)
	case m.notice != "":
		left = textutil.Line(m.notice)
	default:
		left = t.muted.render(textutil.Line(m.query))
		if m.queryWarning != "" {
			left = t.yellow.render("⚠ "+m.queryWarning) + "  " + left
		}
		if layout := m.views[m.view].Config.Layout; layout != "" && layout != "list" && layout != "board" && layout != "tree" {
			left = t.yellow.render(textutil.Line(layout)+" unavailable") + "  " + left
		}
	}
	if m.visual {
		left = t.tab.render(" VISUAL ") + " " + left
		if !t.color {
			left = "-- VISUAL -- " + left
		}
	}
	var right []string
	if m.count > 0 || m.keyPrefix != "" || m.prefix != "" {
		pending := m.keyPrefix
		if pending == "" {
			pending = m.prefix
		}
		count := ""
		if m.count > 0 {
			count = strconv.Itoa(m.count)
		}
		right = append(right, t.accent.render(count+strings.ReplaceAll(pending, " ", "")))
	}
	if n := m.marks.size(); n > 0 {
		right = append(right, t.mark.render(strconv.Itoa(n)+" marked"))
	}
	if m.data.pending > 0 {
		right = append(right, t.pending.render("● "+strconv.Itoa(m.data.pending)))
	}
	if m.data.failed > 0 {
		right = append(right, t.failed.render("! "+strconv.Itoa(m.data.failed)))
	}
	position := ""
	if m.data.items > 0 && m.selected < len(m.data.rows) {
		position = strconv.Itoa(m.data.rows[m.selected].ordinal) + "/" + strconv.Itoa(m.data.items)
	} else if !m.loading {
		position = "0/0"
	}
	spinner := " "
	if m.stale() {
		spinner = t.accent.render("↻")
	}
	right = append(right, spinner+" "+t.muted.render(position))
	rightText := strings.Join(right, "  ") + " "
	room := m.width - ansi.StringWidth(rightText) - 2
	if ansi.StringWidth(left) > room {
		left = ansi.Truncate(left, max(0, room), "…")
	}
	return fit(" "+left+strings.Repeat(" ", max(1, m.width-1-ansi.StringWidth(left)-ansi.StringWidth(rightText)))+rightText, m.width)
}

// sheet is a bottom overlay drawn over the body, above the status bar.
func (m Model) sheet(title string, body []string, width int) []string {
	return m.hintSheet(title, "", body, width)
}

// hintSheet titles its top rule and may name a non-obvious key at the right.
func (m Model) hintSheet(title, hint string, body []string, width int) []string {
	t := m.theme
	label := ""
	if title != "" {
		label = " " + textutil.Line(title) + " "
	}
	right := ""
	if hint != "" {
		right = " " + hint + " "
	}
	fill := max(0, width-1-ansi.StringWidth(label)-ansi.StringWidth(right)-1)
	top := t.faint.render("─") + t.accent.render(label) + t.faint.render(strings.Repeat("─", fill)) + t.muted.render(right) + t.faint.render("─")
	lines := []string{fit(top, width)}
	for _, line := range body {
		lines = append(lines, fit(line, width))
	}
	return lines
}

// modal draws a bordered box centered over already rendered lines.
func (m Model) modal(base []string, title string, body []string, width, height int) []string {
	t := m.theme
	width = max(10, min(width, m.width-2))
	height = max(3, min(height, m.height-2))
	x := (m.width - width) / 2
	y := max(1, (m.height-height)/2)
	inner := width - 4
	box := make([]string, 0, height)
	label := ""
	if title != "" {
		label = ansi.Truncate(" "+title+" ", width-3, "")
	}
	box = append(box, t.faint.render("╭─")+t.accent.render(label)+t.faint.render(strings.Repeat("─", max(0, width-3-ansi.StringWidth(label)))+"╮"))
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(body) {
			line = body[i]
		}
		box = append(box, t.faint.render("│")+" "+fit(line, inner)+" "+t.faint.render("│"))
	}
	box = append(box, t.faint.render("╰"+strings.Repeat("─", width-2)+"╯"))
	out := make([]string, len(base))
	for i, line := range base {
		if i == 0 || i == len(base)-1 {
			out[i] = line
			continue
		}
		out[i] = t.dim(line)
	}
	for i, line := range box {
		row := y + i
		if row <= 0 || row >= len(out)-1 {
			continue
		}
		plain := out[row]
		left := ansi.Truncate(plain, x, "")
		left += strings.Repeat(" ", max(0, x-ansi.StringWidth(left)))
		right := ansi.TruncateLeft(plain, x+width, "")
		out[row] = left + line + right
	}
	return out
}
