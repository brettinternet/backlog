package tui

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/history"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// The change view replaces the item's tabs with one linked change's or
// commit's Overview, Files, Diff, and Commits (design.md D43); backspace
// returns to the Changes or History entry it opened from.

// openChange opens the change view on c, from the Changes tab.
func (m *Model) openChange(c changes.Change) tea.Cmd {
	row, _ := m.selectedRow()
	m.change, m.changeRef, m.changeTab, m.changeCursor = &c, row.Ref, tabChanges, m.entryCursor
	m.find = findState{}
	return m.switchDetailTab(tabChangeOverview)
}

// openCommit opens the History commit under the cursor on the Diff tab; ok
// is false when the entry isn't a commit.
func (m *Model) openCommit() (tea.Cmd, bool) {
	if !m.detailCurrent() || m.entryCursor < 0 || m.entryCursor >= len(m.detailIDs) {
		return nil, false
	}
	id := m.detailIDs[m.entryCursor]
	sha, ok := history.CommitOf(id)
	if !ok {
		return nil, false
	}
	row, _ := m.selectedRow()
	c := changes.CommitChange(cmp.Or(m.opts.EditorDir, "."), sha)
	m.change, m.changeRef, m.changeTab, m.changeEntry = &c, row.Ref, tabHistory, id
	m.find = findState{}
	return m.switchDetailTab(tabChangeDiff), true
}

// closeChange returns to the entry the change view opened from.
func (m *Model) closeChange() tea.Cmd {
	cursor, tab := m.changeCursor, m.changeTab
	m.change = nil
	m.find = findState{}
	cmd := m.switchDetailTab(tab)
	if tab == tabChanges {
		m.entryCursor = cursor
	}
	return cmd
}

// jumpDiff moves the cursor to the next or previous of lines, the Diff tab's
// file or hunk headers, and scrolls it to the top.
func (m *Model) jumpDiff(lines []int, forward bool) {
	from := m.detailScroll
	if m.entryCursor >= 0 && m.entryCursor < len(m.detailEntries) {
		from = m.detailEntries[m.entryCursor]
	}
	if forward {
		if i, _ := slices.BinarySearch(lines, from+1); i < len(lines) {
			m.detailScroll = lines[i]
		}
	} else if i, _ := slices.BinarySearch(lines, from); i > 0 {
		m.detailScroll = lines[i-1]
	}
	m.diffCursorTo(m.detailScroll)
	m.scrollDetail(0)
}

// diffCursorTo puts the Diff cursor on the entry at or after line.
func (m *Model) diffCursorTo(line int) {
	if i, _ := slices.BinarySearch(m.detailEntries, line); i < len(m.detailEntries) {
		m.entryCursor = i
	}
}

// diffCursorInView keeps the Diff cursor on screen as the page scrolls under
// it, so j and k continue from what shows.
func (m *Model) diffCursorInView() {
	if m.entryCursor < 0 || m.entryCursor >= len(m.detailEntries) {
		return
	}
	line, bottom := m.detailEntries[m.entryCursor], m.detailScroll+m.detailVisible()
	if line < m.detailScroll {
		m.diffCursorTo(m.detailScroll)
	} else if line >= bottom {
		if i, _ := slices.BinarySearch(m.detailEntries, bottom); i > 0 {
			m.entryCursor = i - 1
		}
	}
}

// cursorAnchors resolves the Diff line under the cursor, new side first; see
// diffRow.anchors.
func (m Model) cursorAnchors() []lineAnchor {
	if m.detailTab != tabChangeDiff || !m.detailCurrent() || m.entryCursor < 0 || m.entryCursor >= len(m.detailRows) {
		return nil
	}
	return m.detailRows[m.entryCursor].anchors(m.detailHead)
}

type anchorStatus byte

const (
	anchorFound   anchorStatus = iota
	anchorStale                // the Diff shows another head
	anchorOutside              // no hunk shows the line, or the diff has no such file
)

// gotoAnchor moves the Diff cursor to a's line, when the Diff shows it.
func (m *Model) gotoAnchor(a lineAnchor) anchorStatus {
	if m.detailTab != tabChangeDiff || !m.detailCurrent() || a.head != m.detailHead {
		return anchorStale
	}
	n := diffRowOf(m.detailRows, a)
	if n < 0 {
		return anchorOutside
	}
	m.entryCursor = n
	m.keepEntryVisible()
	return anchorFound
}

// diffPlace is where a Diff row is, to find again after a re-render: a row
// without a line, such as a hunk header, is next to the line after or before
// it. A hunk's first row shows the same old line, if any, in either layout.
type diffPlace struct {
	anchor lineAnchor
	offset int
}

func placeOf(rows []diffRow, i int) diffPlace {
	if i < 0 || i >= len(rows) || rows[i].file == "" {
		return diffPlace{offset: -1}
	}
	numbered := func(j int, oldFirst bool) (lineAnchor, bool) {
		if j < 0 || j >= len(rows) || rows[j].file != rows[i].file {
			return lineAnchor{}, false
		}
		a := rows[j].anchors("")
		if oldFirst {
			a[0] = a[len(a)-1]
		}
		return a[0], a[0].line > 0
	}
	if a, ok := numbered(i, false); ok {
		return diffPlace{anchor: a}
	}
	if a, ok := numbered(i+1, true); ok {
		return diffPlace{anchor: a, offset: -1}
	}
	if a, ok := numbered(i-1, false); ok {
		return diffPlace{anchor: a, offset: 1}
	}
	return diffPlace{anchor: lineAnchor{file: rows[i].file}}
}

// find is the place's row in rows, or -1.
func (p diffPlace) find(rows []diffRow) int {
	if p.anchor.file == "" {
		return -1
	}
	n := diffRowOf(rows, p.anchor)
	if n < 0 {
		return -1
	}
	if j := n + p.offset; j >= 0 && j < len(rows) && rows[j].file == p.anchor.file {
		return j
	}
	return n
}

// diffRowOf is the first of rows that shows a, or -1.
func diffRowOf(rows []diffRow, a lineAnchor) int {
	return slices.IndexFunc(rows, func(r diffRow) bool { return r.shows(a) })
}

// shownChange is the change view's change as the row has it now, so a
// refresh shows its new state; one no longer linked stays as it was.
func (m Model) shownChange() changes.Change {
	if row, ok := m.selectedRow(); ok {
		for _, c := range row.Changes {
			if c.ID == m.change.ID {
				return c
			}
		}
	}
	return *m.change
}

// tabRange is the first tab of the tabs on show and how many there are.
func (m Model) tabRange() (detailTab, int) {
	if m.change != nil {
		return tabChangeOverview, len(changeTabNames)
	}
	return tabOverview, len(detailTabNames)
}

// cycleTab is the tab delta places from the current one, wrapping.
func (m Model) cycleTab(delta int) detailTab {
	first, n := m.tabRange()
	return first + detailTab((int(m.detailTab-first)+n+delta)%n)
}

// itemTab is the item tab detail shows, or returns to from the change view.
func (m Model) itemTab() detailTab {
	if m.change != nil {
		return m.changeTab
	}
	return m.detailTab
}

// changeViewPage draws one change view tab: Overview's properties, checks,
// reviewers, and description, one entry per file or commit, or the
// Conversation. Checks and Conversation entries carry the URL O opens.
func changeViewPage(c changes.Change, d changes.Detail, r *changes.Review, draft *changes.Draft, draftErr string, request detailRequest, render func(string, int, string) (string, error)) rendered {
	t, width := request.theme, max(1, request.width)
	now := cmp.Or(request.now, time.Now())
	var page rendered
	switch request.tab {
	case tabChangeFiles:
		if len(d.Files) == 0 {
			page.lines = append(page.lines, t.muted.render("No changed files."))
		}
		for _, f := range d.Files {
			page.entries = append(page.entries, len(page.lines))
			page.lines = append(page.lines, fileLine(t, f, width))
		}
	case tabChangeCommits:
		if len(d.Commits) == 0 {
			page.lines = append(page.lines, t.muted.render("No commits."))
		}
		for _, commit := range d.Commits {
			page.entries, page.ids = append(page.entries, len(page.lines)), append(page.ids, commit.SHA)
			head := t.yellow.render(commit.SHA[:min(7, len(commit.SHA))]) + "  "
			tail := ""
			for _, part := range []string{textutil.Line(commit.Author), age(commit.Time, now)} {
				if part != "" {
					tail += "  " + part
				}
			}
			room := max(1, width-ansi.StringWidth(head)-ansi.StringWidth(tail))
			subject := ansi.Truncate(textutil.Line(commit.Subject), room, "…")
			page.lines = append(page.lines, head+subject+blank(room-ansi.StringWidth(subject))+t.muted.render(tail))
		}
	case tabChangeConversation:
		page = conversationLines(r, request)
	default:
		page = changeOverview(c, d, r, draft, draftErr, request, render)
	}
	if request.tab != tabChangeConversation && d.Reason != "" {
		page.lines = append(page.lines, "", t.yellow.render(ansi.Truncate(textutil.Line(d.Reason), width, "…")))
	}
	return page
}

// changeOverview is the change's property sheet, with a pull request's
// checks as entries, its reviewers, its pending review, and its description.
func changeOverview(c changes.Change, d changes.Detail, r *changes.Review, draft *changes.Draft, draftErr string, request detailRequest, render func(string, int, string) (string, error)) rendered {
	t, width := request.theme, max(1, request.width)
	var page rendered
	add := func(label, value string) {
		if value != "" {
			page.lines = append(page.lines, property(t, label, value))
		}
	}
	add("State", stageGlyph(t, c)+" "+changeState(t, c))
	add("Branch", textutil.Line(c.Branch))
	add("Worktree", textutil.Line(worktreePath(c)))
	if base := c.Base(); base != "" {
		add("Base", textutil.Line(strings.TrimPrefix(strings.TrimPrefix(base, "refs/heads/"), "refs/remotes/")))
	}
	if c.Head != "" {
		add("Head", t.yellow.render(c.Head[:min(7, len(c.Head))]))
	}
	if checks := checksGlyph(t, c.Checks); checks != "" {
		add("Checks", checks+" "+c.Checks)
	}
	if r != nil {
		if c.Checks == "" && len(r.Checks) > 0 {
			page.lines = append(page.lines, property(t, "Checks", ""))
		}
		for _, check := range r.Checks {
			page.entries, page.ids = append(page.entries, len(page.lines)), append(page.ids, check.URL)
			head := blank(propertyWidth) + cmp.Or(checksGlyph(t, check.State), t.muted.render("–")) + " "
			tail := "  " + t.muted.render(check.State)
			room := max(1, width-ansi.StringWidth(head)-ansi.StringWidth(tail))
			name := ansi.Truncate(textutil.Line(check.Name), room, "…")
			page.lines = append(page.lines, head+name+blank(room-ansi.StringWidth(name))+tail)
		}
	}
	review := changeReview(t, c.Review)
	if r != nil && r.Unresolved() > 0 {
		if review != "" {
			review += t.muted.render(" · ")
		}
		review += t.yellow.render(strconv.Itoa(r.Unresolved()) + " unresolved")
	}
	add("Review", review)
	if r != nil {
		for i, reviewer := range r.Reviewers {
			label := ""
			if i == 0 {
				label = "Reviewers"
			}
			page.lines = append(page.lines, property(t, label, ansi.Truncate(textutil.Line(reviewer.Login)+"  "+reviewerState(t, reviewer.State), max(1, width-propertyWidth), "…")))
		}
		pending := draftLines(t, draft, draftErr, cmp.Or(r.Head, c.Head), width)
		for i, entry := range pending.entries {
			page.entries, page.ids = append(page.entries, len(page.lines)+entry), append(page.ids, pending.ids[i])
		}
		page.lines = append(page.lines, pending.lines...)
	}
	additions, deletions := changes.Size(d.Files)
	if len(d.Files) > 0 {
		add("Files", strconv.Itoa(len(d.Files))+"  "+t.green.render("+"+strconv.Itoa(additions))+" "+t.red.render("−"+strconv.Itoa(deletions)))
	}
	if len(d.Commits) > 0 {
		add("Commits", strconv.Itoa(len(d.Commits)))
	}
	dirty := 0
	for _, f := range d.Files {
		if f.Status == changes.FileUncommitted {
			dirty++
		}
	}
	if dirty > 0 {
		add("Dirty", t.yellow.render(strconv.Itoa(dirty)+" uncommitted"))
	}
	if !c.Updated.IsZero() {
		add("Updated", age(c.Updated, request.now)+t.muted.render(" · "+c.Updated.Local().Format("2006-01-02 15:04")))
	}
	if c.URL != "" {
		add("URL", t.link.render(textutil.Line(c.URL)))
	}
	for _, reason := range []string{c.Reason, reviewStale(r)} {
		if reason != "" {
			page.lines = append(page.lines, "", t.yellow.render(ansi.Truncate(textutil.Line(reason), width, "…")))
		}
	}
	if r != nil && strings.TrimSpace(r.Body) != "" {
		limit := bodyLimit
		if request.expanded {
			limit = bodyMoreLimit
		}
		body := cappedBody{limit: limit}
		body.write(r.Body)
		page.truncated = body.full
		page.lines = append(page.lines, "")
		page.lines = append(page.lines, markdownLines(body.builder.String(), width, t, render)...)
	}
	return page
}

func reviewStale(r *changes.Review) string {
	if r == nil {
		return ""
	}
	return r.Stale
}

func reviewerState(t theme, state string) string {
	switch state {
	case changes.ReviewApproved:
		return t.green.render("approved")
	case changes.ReviewChanges:
		return t.red.render("changes requested")
	case changes.ReviewRequested:
		return t.yellow.render("requested")
	}
	return t.muted.render(state)
}

// markdownLines renders text, or shows it plain when rendering fails.
func markdownLines(text string, width int, t theme, render func(string, int, string) (string, error)) []string {
	out, err := "", errors.New("no renderer")
	if render != nil {
		out, err = render(text, width, t.name)
	}
	if err != nil {
		out = ansi.Wrap(textutil.Sanitize(text), width, "")
	}
	if len(out) > renderLimit {
		out = truncateUTF8(out, renderLimit) + "\n… rendered output capped"
	}
	var lines []string
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n "), "\n") {
		lines = append(lines, strings.TrimRight(line, " "))
	}
	return lines
}

// conversationLines draws one entry per comment or review thread, unresolved
// threads first: a thread's file and line or a comment's author, then each
// comment's author, age, and wrapped text.
func conversationLines(r *changes.Review, request detailRequest) rendered {
	t, width := request.theme, max(1, request.width)
	now := cmp.Or(request.now, time.Now())
	var page rendered
	if r == nil {
		return page
	}
	if r.Stale != "" {
		page.lines = append(page.lines, t.yellow.render(ansi.Truncate(textutil.Line(r.Stale), width, "…")), "")
	}
	entries := r.Conversation()
	if len(entries) == 0 {
		page.lines = append(page.lines, t.muted.render("No conversation."))
		return page
	}
	limit := bodyLimit
	if request.expanded {
		limit = bodyMoreLimit
	}
	used := 0
	comment := func(c changes.Comment, indent string) {
		head := indent + t.bold.render(textutil.Line(c.Author))
		if c.State != "" {
			head += "  " + reviewerState(t, c.State)
		}
		if a := age(c.Time, now); a != "" {
			head += t.muted.render("  " + a)
		}
		page.lines = append(page.lines, ansi.Truncate(head, width, "…"))
		body := strings.TrimSpace(textutil.Sanitize(c.Body))
		if body == "" || page.truncated {
			return
		}
		if used+len(body) > limit {
			body, page.truncated = truncateUTF8(body, max(0, limit-used))+"…", true
		}
		used += len(body)
		for line := range strings.SplitSeq(ansi.Wrap(body, max(1, width-len(indent)-2), ""), "\n") {
			page.lines = append(page.lines, indent+"  "+strings.TrimRight(line, " "))
		}
	}
	for i, e := range entries {
		if i > 0 {
			page.lines = append(page.lines, "")
		}
		page.entries, page.ids = append(page.entries, len(page.lines)), append(page.ids, e.Comment.URL)
		if e.Thread == nil {
			comment(e.Comment, "")
			continue
		}
		state := t.yellow.render("● unresolved")
		if e.Thread.Resolved {
			state = t.muted.render("✓ resolved")
		}
		if e.Thread.Outdated {
			state += t.muted.render(" · outdated")
		}
		where := textutil.Line(e.Thread.Path)
		if e.Thread.Line > 0 {
			where += ":" + strconv.Itoa(e.Thread.Line)
		}
		room := max(1, width-ansi.StringWidth(state)-2)
		where = ansi.Truncate(where, room, "…")
		page.lines = append(page.lines, t.accent.render(where)+blank(room-ansi.StringWidth(where)+2)+state)
		for _, c := range e.Thread.Comments {
			comment(c, "  ")
		}
	}
	return page
}

// fileLine is a status letter, the path (old → new for a rename) cut from
// the left so the file name stays, and its line counts at the right.
func fileLine(t theme, f changes.File, width int) string {
	glyph := map[string]string{
		changes.FileAdded: t.green.render("A"), changes.FileDeleted: t.red.render("D"),
		changes.FileRenamed: t.magenta.render("R"), changes.FileBinary: t.muted.render("B"),
		changes.FileUncommitted: t.yellow.render("U"),
	}[f.Status]
	if glyph == "" {
		glyph = t.accent.render("M")
	}
	tail := "  " + t.green.render("+"+strconv.Itoa(f.Additions)) + " " + t.red.render("−"+strconv.Itoa(f.Deletions))
	if f.Status == changes.FileBinary {
		tail = "  " + t.muted.render("binary")
	}
	path := textutil.Line(f.Path)
	if f.From != "" {
		path = textutil.Line(f.From) + " → " + path
	}
	room := max(1, width-2-ansi.StringWidth(tail))
	if w := ansi.StringWidth(path); w > room {
		path = "…" + ansi.TruncateLeft(path, w-room+1, "")
	}
	return glyph + " " + path + blank(room-ansi.StringWidth(path)) + tail
}

// worktreePath is a change's worktree relative to its checkout root, or
// under ~, or as it is.
func worktreePath(c changes.Change) string {
	if c.Worktree == "" {
		return ""
	}
	if dir := c.Dir(); dir != "" {
		if rel, err := filepath.Rel(dir, c.Worktree); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, c.Worktree); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return c.Worktree
}
