package tui

import (
	"cmp"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/history"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// The change view replaces the item's tabs with one linked change's or
// commit's Overview, Files, Diff, and Commits (design.md D43); backspace
// returns to the Changes, History, or Commits entry it opened from.

// openChange opens the change view on c, from the Changes tab.
func (m *Model) openChange(c changes.Change) tea.Cmd {
	row, _ := m.selectedRow()
	m.showChange(row, c, m.entryCursor, tabChangeOverview)
	return m.showDetail()
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
	c := changes.CommitChange(cmp.Or(m.opts.EditorDir, "."), changes.Commit{SHA: sha})
	m.change, m.changeRef, m.changeTab, m.changeEntry = &c, row.Ref, tabHistory, id
	m.find = findState{}
	return m.switchDetailTab(tabChangeDiff), true
}

// openChangeCommit opens the Commits entry under the cursor on the Diff tab,
// one level under the change; a commit's own Commits don't nest further.
func (m *Model) openChangeCommit() tea.Cmd {
	if m.detailTab != tabChangeCommits || m.change.IsCommit() || !m.detailCurrent() || m.entryCursor < 0 || m.entryCursor >= len(m.detailCommits) {
		return nil
	}
	// The forge's message shows when the checkout lacks the commit.
	c := changes.CommitChange(cmp.Or(m.change.Dir(), m.opts.EditorDir, "."), m.detailCommits[m.entryCursor])
	m.changeParent, m.parentCursor, m.change = m.change, m.entryCursor, &c
	m.find, m.changeReturn = findState{}, nil
	return m.switchDetailTab(tabChangeDiff)
}

// followChangeLink does what enter does on the Overview, Files, or
// Conversation entry under the cursor: toggles the folded checks or resolved
// threads, opens a check's view, opens a review in Conversation, opens the
// Diff at a file or thread, or opens ! narrowed to the change's actions.
func (m Model) followChangeLink() (tea.Model, tea.Cmd) {
	if !m.detailCurrent() || m.entryCursor < 0 || m.entryCursor >= len(m.detailLinks) {
		return m, nil
	}
	link := m.detailLinks[m.entryCursor]
	switch {
	case link == linkFold && m.detailTab == tabChangeConversation:
		return m, m.toggleResolved(!m.resolvedOpen)
	case link == linkFold:
		return m, m.toggleChecks(!m.checksOpen)
	case strings.HasPrefix(link, linkCheck):
		return m, m.openCheck(link)
	case strings.HasPrefix(link, linkThread):
		if t := threadAt(m.review.read, strings.TrimPrefix(link, linkThread)); t != nil && (t.Outdated || t.Line == 0) {
			m.notice = "This thread is outdated; its line isn't in the diff."
			return m, nil
		}
		return m, m.openDiffAt(link)
	case link == linkThreads || strings.HasPrefix(link, linkFile):
		return m, m.openDiffAt(link)
	case strings.HasPrefix(link, linkReview):
		login := strings.TrimPrefix(link, linkReview)
		m.reviewEntry = reviewURL(m.review.read, login)
		// A review without a comment has no Conversation entry, so only a
		// reviewer still requested hasn't reviewed.
		if m.reviewEntry == "" && login != "" && m.review.read != nil && slices.ContainsFunc(m.review.read.Reviewers, func(r changes.Reviewer) bool {
			return r.Login == login && r.State == changes.ReviewRequested
		}) {
			m.notice = textutil.Line(login) + " hasn't reviewed yet."
			return m, nil
		}
		return m, m.switchDetailTab(tabChangeConversation)
	case link == linkActions:
		m.actionSession, m.actionChange = "", new(m.actionTarget(m.shownChange()))
		return m.openConfiguredActions()
	}
	return m, nil
}

// toggleChecks shows or folds the Overview's passing and skipped checks.
func (m *Model) toggleChecks(open bool) tea.Cmd {
	if m.checksOpen == open {
		return nil
	}
	m.checksOpen = open
	return m.showDetail()
}

// toggleResolved shows or folds Conversation's resolved threads.
func (m *Model) toggleResolved(open bool) tea.Cmd {
	if m.resolvedOpen == open {
		return nil
	}
	m.resolvedOpen = open
	return m.showDetail()
}

// openCheck opens the check view, one level under the Overview entry under
// the cursor, on the check link names.
func (m *Model) openCheck(link string) tea.Cmd {
	if m.check != nil || m.review.read == nil {
		return nil
	}
	i := slices.IndexFunc(m.review.read.Checks, func(c changes.Check) bool { return checkLink(c) == link })
	if i < 0 {
		m.notice = "The check is no longer on the head."
		return nil
	}
	check := m.review.read.Checks[i]
	m.check, m.shownCheck, m.checkCursor, m.entryCursor = &check, nil, m.entryCursor, 0
	m.find = findState{}
	return m.showDetail()
}

// checkLink names a check for enter: its node ID, else its name.
func checkLink(c changes.Check) string {
	return linkCheck + cmp.Or(c.ID, c.Name)
}

// openDiffAt opens the Diff at target, a link to a file, a thread, or the
// first unresolved thread; backspace returns to the entry it came from.
func (m *Model) openDiffAt(target string) tea.Cmd {
	from := &tabStop{tab: m.detailTab, cursor: m.entryCursor}
	m.diffTarget = target
	cmd := m.switchDetailTab(tabChangeDiff)
	m.changeReturn = from
	return cmd
}

// landDiff puts the cursor on the Diff's target as the Diff arrives.
func (m *Model) landDiff(target string) {
	n := -1
	switch {
	case target == linkThreads:
		if len(m.detailThreads) == 0 {
			m.notice = "No unresolved threads in this diff."
			return
		}
		n = m.detailThreads[0]
	case strings.HasPrefix(target, linkFile):
		n = diffRowOf(m.detailRows, lineAnchor{file: strings.TrimPrefix(target, linkFile)})
	case strings.HasPrefix(target, linkThread):
		key := strings.TrimPrefix(target, linkThread)
		if n = slices.IndexFunc(m.detailRows, func(r diffRow) bool { return r.thread == key }); n < 0 {
			m.notice = "This thread's line isn't in the diff."
		}
	}
	if n >= 0 && n < len(m.detailEntries) {
		m.showDiffEntry(n, strings.HasPrefix(target, linkFile))
	}
}

// showDiffEntry puts the cursor on entry n, at the top of the view for a
// file and a few lines below it for a thread, so the line it's on shows.
func (m *Model) showDiffEntry(n int, top bool) {
	m.entryCursor = n
	m.detailScroll = m.detailEntries[n]
	if !top {
		m.detailScroll = max(0, m.detailScroll-3)
	}
	m.scrollDetail(0)
}

// stepThread moves the Diff cursor to the next or previous unresolved
// thread, wrapping around the diff.
func (m *Model) stepThread(forward bool) {
	threads := m.detailThreads
	if len(threads) == 0 {
		m.notice = "No unresolved threads in this diff."
		return
	}
	i, found := slices.BinarySearch(threads, m.entryCursor)
	if forward && found {
		i++
	} else if !forward {
		i--
	}
	switch {
	case i >= len(threads):
		i, m.notice = 0, "Wrapped to the first unresolved thread."
	case i < 0:
		i, m.notice = len(threads)-1, "Wrapped to the last unresolved thread."
	}
	m.showDiffEntry(threads[i], false)
}

// cursorThread is the review thread the Diff cursor is on, or "".
func (m Model) cursorThread() string {
	if m.detailTab != tabChangeDiff || !m.detailCurrent() || m.entryCursor < 0 || m.entryCursor >= len(m.detailRows) {
		return ""
	}
	return m.detailRows[m.entryCursor].thread
}

// foldThread shows or folds the Diff thread key: "fold" toggles it, and
// "fold-open" and "fold-close" show and fold it.
func (m *Model) foldThread(key, how string) tea.Cmd {
	t := threadAt(m.review.read, key)
	if t == nil {
		return nil
	}
	shown := threadShown(*t, m.threadsToggled)
	open := how == "fold-open" || how == "fold" && !shown
	if open == shown {
		return nil
	}
	toggled := maps.Clone(m.threadsToggled)
	if toggled == nil {
		toggled = map[string]bool{}
	}
	if open == t.Resolved {
		toggled[key] = true
	} else {
		delete(toggled, key)
	}
	m.threadsToggled = toggled
	return m.showDetail()
}

// closeChange returns to the entry the change view opened from; from a Diff
// opened at a file or thread, or from the check view, to the entry that
// opened it.
func (m *Model) closeChange() tea.Cmd {
	if stop := m.changeReturn; stop != nil && m.detailTab == tabChangeDiff {
		m.changeReturn = nil
		cmd := m.switchDetailTab(stop.tab)
		m.entryCursor = stop.cursor
		return cmd
	}
	m.find, m.changeReturn = findState{}, nil
	if m.check != nil {
		m.check, m.shownCheck = nil, nil
		cmd := m.showDetail()
		m.entryCursor = m.checkCursor
		return cmd
	}
	if m.changeParent != nil {
		cursor := m.parentCursor
		m.change, m.changeParent = m.changeParent, nil
		cmd := m.switchDetailTab(tabChangeCommits)
		m.entryCursor = cursor
		return cmd
	}
	cursor, tab := m.changeCursor, m.changeTab
	m.change = nil
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

// stepTour goes to the tour's next or previous step, in the tour's order.
func (m *Model) stepTour(forward bool) {
	if len(m.detailSteps) == 0 {
		m.notice = cmp.Or(m.detailTour, "No tour for this change.")
		return
	}
	step := m.tourStep - 1
	if forward {
		step = m.tourStep + 1
	}
	m.tourStep = min(max(step, 0), len(m.detailSteps)-1)
	span := m.detailSteps[m.tourStep]
	m.detailScroll = span.callout
	m.diffCursorTo(span.callout)
	if span.outdated {
		// Its line isn't at the head, so the cursor goes to its file's header.
		i, _ := slices.BinarySearch(m.detailEntries, span.callout)
		m.entryCursor = max(i-1, 0)
	}
	// The callout gives way to the range, and the range to its first line.
	if m.entryCursor < len(m.detailEntries) {
		cursor, visible := m.detailEntries[m.entryCursor], m.detailVisible()
		m.detailScroll = min(max(span.callout, span.last-visible+1, cursor-visible+1), cursor)
	}
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
	thread string // the review thread on the row, found first
}

func placeOf(rows []diffRow, i int) diffPlace {
	if i < 0 || i >= len(rows) || rows[i].file == "" {
		return diffPlace{offset: -1}
	}
	if key := rows[i].thread; key != "" {
		// A thread gone since is at the line it was under.
		p := placeOf(rows, i-1)
		p.thread = key
		return p
	}
	numbered := func(j int, oldFirst bool) (lineAnchor, bool) {
		if j < 0 || j >= len(rows) || rows[j].file != rows[i].file || rows[j].thread != "" {
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
	if p.thread != "" {
		if n := slices.IndexFunc(rows, func(r diffRow) bool { return r.thread == p.thread }); n >= 0 {
			return n
		}
	}
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
func changeViewPage(c changes.Change, d changes.Detail, r *changes.Review, draft *changes.Draft, draftErr string, request detailRequest, render, comment func(string, int, string) (string, error)) rendered {
	t, width := request.theme, max(1, request.width)
	now := cmp.Or(request.now, time.Now())
	var page rendered
	switch request.tab {
	case tabChangeFiles:
		if len(d.Files) == 0 {
			page.lines = append(page.lines, t.muted.render("No changed files."))
		}
		unresolved := map[string]int{}
		if r != nil {
			for _, thread := range r.Threads {
				if !thread.Resolved {
					unresolved[thread.Path]++
				}
			}
		}
		for _, f := range d.Files {
			page.entries, page.links = append(page.entries, len(page.lines)), append(page.links, linkFile+f.Path)
			page.lines = append(page.lines, fileLine(t, f, width, fileThreads(unresolved, f)))
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
		page = conversationLines(r, request, comment)
	default:
		page = changeOverview(c, d, r, draft, draftErr, request, render)
	}
	if request.tab != tabChangeConversation && d.Reason != "" {
		page.lines = append(page.lines, "", t.yellow.render(ansi.Truncate(textutil.Line(d.Reason), width, "…")))
	}
	return page
}

// changeOverview is a change's Overview: a pull request's Merge verdict and
// reasons, Checks, Reviews, and pending review, then About and the
// description. Each reason, check, and reviewer is an entry whose link says
// where enter goes, and whose id is the URL O opens.
func changeOverview(c changes.Change, d changes.Detail, r *changes.Review, draft *changes.Draft, draftErr string, request detailRequest, render func(string, int, string) (string, error)) rendered {
	t, width := request.theme, max(1, request.width)
	now := cmp.Or(request.now, time.Now())
	var page rendered
	entry := func(line, id, link string) {
		page.entries, page.ids, page.links = append(page.entries, len(page.lines)), append(page.ids, id), append(page.links, link)
		page.lines = append(page.lines, line)
	}
	section := func(label, value string) {
		if len(page.lines) > 0 {
			page.lines = append(page.lines, "")
		}
		page.lines = append(page.lines, property(t, label, value))
	}
	if r != nil && c.Number > 0 {
		merge := changes.Verdict(c, *r)
		section("Merge", verdictLabel(t, merge.Verdict))
		urls, links := map[string]string{}, map[string]string{}
		// A reason cites a check by name; checks sharing one, such as a
		// job on push and pull_request, are told apart by state.
		for _, check := range r.Checks {
			key := check.State + " " + check.Name
			urls[key] = cmp.Or(urls[key], check.URL)
			links[key] = cmp.Or(links[key], checkLink(check))
		}
		for _, reason := range merge.Reasons {
			id, link := c.URL, ""
			switch reason.Kind {
			case changes.ReasonFailing, changes.ReasonPending:
				key := reason.Kind + " " + reason.Check
				id, link = cmp.Or(urls[key], c.URL), links[key]
			case changes.ReasonChanges:
				id, link = cmp.Or(reviewURL(r, reason.Login), c.URL), linkReview+reason.Login
			case changes.ReasonThreads:
				link = linkThreads
			case changes.ReasonConflicts, changes.ReasonBehind, changes.ReasonReady:
				link = linkActions
			}
			text := reason.Text
			if reason.Kind == changes.ReasonChanges && reason.Login != "" {
				text += "  " + t.muted.render(age(reviewerTime(r, reason.Login), now))
			}
			entry("  "+reasonGlyph(t, reason)+" "+ansi.Truncate(textutil.Line(text), max(1, width-4), "…"), id, link)
		}
		if len(r.Checks) > 0 {
			section("Checks", checkCounts(t, r.Checks))
			checkRows(t, r.Checks, request.checksOpen, now, width, entry)
		}
		if len(r.Reviewers) > 0 || c.Review != "" {
			section("Reviews", changeReview(t, c.Review))
			reviewerRows(t, r, now, width, entry)
		}
		pending := draftLines(t, draft, draftErr, cmp.Or(r.Head, c.Head), width)
		if len(pending.lines) > 0 {
			page.lines = append(page.lines, "")
		}
		for i, line := range pending.entries {
			page.entries, page.ids, page.links = append(page.entries, len(page.lines)+line), append(page.ids, pending.ids[i]), append(page.links, "")
		}
		page.lines = append(page.lines, pending.lines...)
	}
	about := aboutLines(t, c, d, now, width)
	if len(about) > 0 {
		section("About", about[0])
		for _, line := range about[1:] {
			page.lines = append(page.lines, blank(propertyWidth)+line)
		}
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

// Overview entry links: where enter goes from a reason, check, or reviewer.
const (
	linkCheck   = "check:"  // the check view of the check checkLink names
	linkReview  = "review:" // the login's latest review in Conversation
	linkThreads = "threads" // the Diff at its first unresolved thread
	linkThread  = "thread:" // the Diff at the review thread with this key
	linkFile    = "file:"   // the Diff at this file
	linkActions = "actions" // ! narrowed to the change's actions
	linkFold    = "fold"    // passing and skipped checks
)

// foldChecks is how many checks show before passing and skipped ones fold.
const foldChecks = 6

// checkRows draws checks problems first, one entry each: glyph, name, exact
// state word, and duration, which drops first as width shrinks. Past
// foldChecks, passing and skipped checks fold into one summary entry.
func checkRows(t theme, checks []changes.Check, open bool, now time.Time, width int, entry func(line, id, link string)) {
	ordered := changes.Ordered(checks)
	shown, folded := ordered, []changes.Check(nil)
	if len(ordered) > foldChecks {
		i := slices.IndexFunc(ordered, func(c changes.Check) bool {
			return c.State == changes.ChecksPassing || c.State == changes.ChecksSkipped
		})
		if i >= 0 {
			shown, folded = ordered[:i], ordered[i:]
		}
	}
	name := 0
	for _, check := range ordered {
		name = max(name, ansi.StringWidth(textutil.Line(check.Name)))
	}
	row := func(check changes.Check) {
		status := cmp.Or(check.Status, check.State)
		duration := checkDuration(check, now)
		state := cell(status, len(changes.CheckActionRequired))
		room := width - 4 - len(changes.CheckActionRequired) - 2
		if duration != "" && room-2-len(duration) >= min(name, 12) {
			room -= 2 + len(duration)
			duration = "  " + t.muted.render(duration)
		} else {
			duration = ""
		}
		label := ansi.Truncate(textutil.Line(check.Name), max(1, min(name, room)), "…")
		line := "  " + checkGlyph(t, check) + " " + label + blank(min(name, room)-ansi.StringWidth(label)) + "  " + t.muted.render(state) + duration
		entry(ansi.Truncate(strings.TrimRight(line, " "), width, "…"), check.URL, checkLink(check))
	}
	for _, check := range shown {
		row(check)
	}
	if len(folded) == 0 {
		return
	}
	passing, skipped := 0, 0
	for _, check := range folded {
		if check.State == changes.ChecksPassing {
			passing++
		} else {
			skipped++
		}
	}
	var parts []string
	if passing > 0 {
		parts = append(parts, t.green.render("✓")+" "+strconv.Itoa(passing)+" passing")
	}
	if skipped > 0 {
		parts = append(parts, t.muted.render("–")+" "+strconv.Itoa(skipped)+" skipped")
	}
	summary, mark := "  "+strings.Join(parts, t.muted.render(" · ")), "▸"
	if open {
		mark = "▾"
	}
	entry(summary+blank(width-ansi.StringWidth(summary)-1)+t.muted.render(mark), "", linkFold)
	if open {
		for _, check := range folded {
			row(check)
		}
	}
}

// checkCounts is the Checks header: how many fail, run, pass, and skip.
func checkCounts(t theme, checks []changes.Check) string {
	counts := map[string]int{}
	for _, check := range checks {
		counts[check.State]++
	}
	var parts []string
	for _, kind := range []struct{ state, glyph string }{
		{changes.ChecksFailing, t.red.render("✕")}, {changes.ChecksPending, t.yellow.render("●")},
		{changes.ChecksStale, t.muted.render("?")}, {changes.ChecksPassing, t.green.render("✓")}, {changes.ChecksSkipped, t.muted.render("–")},
	} {
		if n := counts[kind.state]; n > 0 {
			parts = append(parts, kind.glyph+" "+strconv.Itoa(n))
		}
	}
	return strings.Join(parts, "  ")
}

// checkGlyph marks a check by its exact state: queued and waiting checks
// haven't started.
func checkGlyph(t theme, check changes.Check) string {
	switch check.Status {
	case changes.CheckQueued, changes.CheckWaiting:
		return t.muted.render("○")
	case changes.CheckSkipped, changes.CheckNeutral:
		return t.muted.render("–")
	}
	return cmp.Or(checksGlyph(t, check.State), t.muted.render("–"))
}

// checkDuration is how long a check ran, or has been running, with … .
func checkDuration(check changes.Check, now time.Time) string {
	if check.StartedAt.IsZero() {
		return ""
	}
	if !check.CompletedAt.IsZero() {
		return duration(check.CompletedAt.Sub(check.StartedAt))
	}
	if check.State == changes.ChecksPending {
		return duration(now.Sub(check.StartedAt)) + "…"
	}
	return ""
}

// duration is d to the second under an hour, such as 4m12s, else 1h02m.
func duration(d time.Duration) string {
	d = max(0, d.Round(time.Second))
	h, m, s := int(d/time.Hour), int(d/time.Minute)%60, int(d/time.Second)%60
	two := func(n int) string { return string(rune('0'+n/10)) + string(rune('0'+n%10)) }
	switch {
	case h > 0:
		return strconv.Itoa(h) + "h" + two(m) + "m"
	case m > 0:
		return strconv.Itoa(m) + "m" + two(s) + "s"
	}
	return strconv.Itoa(s) + "s"
}

// reviewerRows draws one entry per reviewer: login, state, age, and
// outdated when the review isn't on the head.
func reviewerRows(t theme, r *changes.Review, now time.Time, width int, entry func(line, id, link string)) {
	name := 0
	for _, reviewer := range r.Reviewers {
		name = max(name, ansi.StringWidth(textutil.Line(reviewer.Login)))
	}
	name = min(name, max(1, width/3))
	for _, reviewer := range r.Reviewers {
		login := ansi.Truncate(textutil.Line(reviewer.Login), name, "…")
		line := "  " + login + blank(name-ansi.StringWidth(login)) + "  " + reviewerState(t, reviewer.State)
		if a := age(reviewer.SubmittedAt, now); a != "" {
			line += "  " + t.muted.render(a)
		}
		if reviewer.Outdated {
			line += "  " + t.yellow.render("outdated")
		}
		entry(ansi.Truncate(line, width, "…"), reviewURL(r, reviewer.Login), linkReview+reviewer.Login)
	}
}

// reviewURL is the login's latest review in the conversation, or "".
func reviewURL(r *changes.Review, login string) string {
	if c, ok := latestReview(r, login); ok {
		return c.URL
	}
	return ""
}

func latestReview(r *changes.Review, login string) (changes.Comment, bool) {
	if r == nil || login == "" {
		return changes.Comment{}, false
	}
	for i := len(r.Comments) - 1; i >= 0; i-- {
		if c := r.Comments[i]; c.Author == login && c.State != "" && c.URL != "" {
			return c, true
		}
	}
	return changes.Comment{}, false
}

func reviewerTime(r *changes.Review, login string) time.Time {
	for _, reviewer := range r.Reviewers {
		if reviewer.Login == login {
			return reviewer.SubmittedAt
		}
	}
	return time.Time{}
}

// aboutLines compress the change's facts: branch → base, commits, and
// files, then its head, age, worktree, uncommitted files, and URL.
func aboutLines(t theme, c changes.Change, d changes.Detail, now time.Time, width int) []string {
	room := max(1, width-propertyWidth)
	var first []string
	if base := strings.TrimPrefix(strings.TrimPrefix(c.Base(), "refs/heads/"), "refs/remotes/"); c.Branch != "" && base != "" {
		first = append(first, textutil.Line(c.Branch)+" → "+textutil.Line(base))
	} else if c.Branch != "" {
		first = append(first, textutil.Line(c.Branch))
	}
	if n := len(d.Commits); n == 1 {
		first = append(first, "1 commit")
	} else if n > 1 {
		first = append(first, strconv.Itoa(n)+" commits")
	}
	if len(d.Files) > 0 {
		additions, deletions := changes.Size(d.Files)
		first = append(first, strconv.Itoa(len(d.Files))+" files "+t.green.render("+"+strconv.Itoa(additions))+" "+t.red.render("−"+strconv.Itoa(deletions)))
	}
	var second []string
	if c.Head != "" {
		second = append(second, "head "+t.yellow.render(c.Head[:min(7, len(c.Head))]))
	}
	if !c.Updated.IsZero() {
		second = append(second, "updated "+age(c.Updated, now))
	}
	dirty := 0
	for _, f := range d.Files {
		if f.Status == changes.FileUncommitted {
			dirty++
		}
	}
	if dirty > 0 {
		second = append(second, t.yellow.render(strconv.Itoa(dirty)+" uncommitted"))
	}
	var lines []string
	for _, parts := range [][]string{first, second} {
		if len(parts) > 0 {
			lines = append(lines, ansi.Truncate(strings.Join(parts, t.muted.render(" · ")), room, "…"))
		}
	}
	if path := worktreePath(c); path != "" {
		lines = append(lines, ansi.Truncate("worktree "+textutil.Line(path), room, "…"))
	}
	if c.URL != "" {
		lines = append(lines, t.link.render(ansi.Truncate(textutil.Line(c.URL), room, "…")))
	}
	return lines
}

// verdictLabel is a verdict's glyph and word, colored by how close the pull
// request is to landing.
func verdictLabel(t theme, verdict string) string {
	switch verdict {
	case changes.VerdictReady:
		return t.green.render("✓ Ready")
	case changes.VerdictUnstable:
		return t.yellow.render("! Unstable")
	case changes.VerdictRunning:
		return t.yellow.render("● Running")
	case changes.VerdictBlocked:
		return t.red.render("✕ Blocked")
	case changes.VerdictBehind:
		return t.yellow.render("↓ Behind")
	case changes.VerdictConflicts:
		return t.red.render("⚠ Conflicts")
	case changes.VerdictDraft:
		return t.muted.render("Draft")
	case changes.VerdictMerged:
		return t.magenta.render("Merged")
	case changes.VerdictClosed:
		return t.muted.render("Closed")
	}
	return t.muted.render("Checking")
}

func reasonGlyph(t theme, reason changes.Reason) string {
	switch reason.Kind {
	case changes.ReasonFailing, changes.ReasonProtection:
		return t.red.render("✕")
	case changes.ReasonPending, changes.ReasonThreads:
		return t.yellow.render("●")
	case changes.ReasonChanges:
		return t.red.render("✎")
	case changes.ReasonReview:
		return t.yellow.render("○")
	case changes.ReasonConflicts:
		return t.red.render("⚠")
	case changes.ReasonBehind:
		return t.yellow.render("↓")
	case changes.ReasonStale:
		return t.yellow.render("?")
	}
	return t.green.render("✓")
}

// reasonSummary is the strip's short form of the reasons, such as 1 failing
// · changes requested · 3 unresolved.
func reasonSummary(merge changes.Merge) string {
	counts := map[string]int{}
	var kinds []string
	for _, reason := range merge.Reasons {
		if counts[reason.Kind] == 0 {
			kinds = append(kinds, reason.Kind)
		}
		counts[reason.Kind]++
	}
	var parts []string
	for _, kind := range kinds {
		n := strconv.Itoa(counts[kind])
		switch kind {
		case changes.ReasonFailing:
			parts = append(parts, n+" failing")
		case changes.ReasonPending:
			parts = append(parts, n+" running")
		case changes.ReasonChanges:
			parts = append(parts, "changes requested")
		case changes.ReasonThreads:
			for _, reason := range merge.Reasons {
				if reason.Kind == kind {
					parts = append(parts, strings.Fields(reason.Text)[0]+" unresolved")
				}
			}
		case changes.ReasonReview:
			parts = append(parts, "review required")
		case changes.ReasonConflicts:
			parts = append(parts, "conflicts")
		case changes.ReasonBehind:
			parts = append(parts, "behind")
		case changes.ReasonProtection:
			parts = append(parts, "branch protection")
		case changes.ReasonStale:
			parts = append(parts, "out of date")
		}
	}
	return strings.Join(parts, " · ")
}

// changeStrip is the change view's line under its tabs. A pull request shows
// its verdict, the reasons, which truncate, and the age of the last read, or
// stale when it failed; a branch or worktree shows how far it is from its base
// and its uncommitted files.
func changeStrip(t theme, c changes.Change, facts *changes.Change, r *changes.Review, now time.Time, width int) string {
	head := stageGlyph(t, c) + " " + textutil.Line(changes.Label(c))
	if base := strings.TrimPrefix(strings.TrimPrefix(c.Base(), "refs/heads/"), "refs/remotes/"); base != "" && c.Number > 0 {
		head += t.muted.render(" → " + textutil.Line(base))
	}
	if c.Number == 0 || facts == nil || facts.Merge == nil {
		var parts []string
		if facts != nil {
			if facts.Ahead != nil && *facts.Ahead > 0 {
				parts = append(parts, "↑"+strconv.Itoa(*facts.Ahead))
			}
			if facts.Behind != nil && *facts.Behind > 0 {
				parts = append(parts, "↓"+strconv.Itoa(*facts.Behind))
			}
			if facts.Dirty != nil && *facts.Dirty > 0 {
				parts = append(parts, t.yellow.render(strconv.Itoa(*facts.Dirty)+" dirty"))
			}
		}
		if c.Number > 0 {
			parts = append(parts, t.muted.render("Checking"))
		}
		return ansi.Truncate(strings.Join(append([]string{head}, parts...), "   "), width, "…")
	}
	verdict := verdictLabel(t, facts.Merge.Verdict)
	when := ""
	if r != nil {
		when = age(r.Fetched, now)
		if r.Stale != "" {
			when = t.yellow.render("stale")
		}
	}
	head = ansi.Truncate(head, max(1, width-ansi.StringWidth(verdict)-ansi.StringWidth(when)-5), "…") + "   " + verdict
	room := width - ansi.StringWidth(head) - ansi.StringWidth(when) - 2
	summary := ""
	if room > 3 {
		if s := reasonSummary(*facts.Merge); s != "" {
			summary = "  " + t.muted.render(ansi.Truncate(s, room-2, "…"))
		}
	}
	line := head + summary
	return line + blank(width-ansi.StringWidth(line)-ansi.StringWidth(when)) + t.muted.render(when)
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

// threadHunkLines is how many lines of its diff hunk a Conversation thread shows.
const threadHunkLines = 4

// conversation is Conversation's entries: unresolved threads by file and
// line, comments and reviews oldest first, then one entry, a zero Entry,
// folding the resolved threads, followed by them when open.
func conversation(r *changes.Review, open bool) []changes.Entry {
	if r == nil {
		return nil
	}
	entries := r.Conversation()
	i := slices.IndexFunc(entries, func(e changes.Entry) bool { return e.Thread != nil && e.Thread.Resolved })
	if i < 0 {
		return entries
	}
	if !open {
		return append(entries[:i:i], changes.Entry{})
	}
	return slices.Concat(entries[:i], []changes.Entry{{}}, entries[i:])
}

// conversationLines draws one entry per comment or review thread: a thread's
// file, line, state, and the end of its diff hunk, then each comment's
// author, age, and text rendered as Markdown, and resolved threads folded.
func conversationLines(r *changes.Review, request detailRequest, render func(string, int, string) (string, error)) rendered {
	t, width := request.theme, max(1, request.width)
	now := cmp.Or(request.now, time.Now())
	var page rendered
	if r == nil {
		return page
	}
	if r.Stale != "" {
		page.lines = append(page.lines, t.yellow.render(ansi.Truncate(textutil.Line(r.Stale), width, "…")), "")
	}
	entries := conversation(r, request.resolved)
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
		lines := markdownLines(body, max(1, width-len(indent)-2), t, render)
		for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[0])) == "" {
			lines = lines[1:]
		}
		for _, line := range lines {
			page.lines = append(page.lines, indent+"  "+line)
		}
	}
	resolved := 0
	for _, thread := range r.Threads {
		if thread.Resolved && len(thread.Comments) > 0 {
			resolved++
		}
	}
	for i, e := range entries {
		if i > 0 {
			page.lines = append(page.lines, "")
		}
		page.entries, page.ids = append(page.entries, len(page.lines)), append(page.ids, e.Comment.URL)
		switch {
		case e.Thread == nil && e.Comment.Author == "" && e.Comment.URL == "":
			page.links = append(page.links, linkFold)
			label := "✓ " + strconv.Itoa(resolved) + " resolved thread"
			if resolved != 1 {
				label += "s"
			}
			page.lines = append(page.lines, t.muted.render(label))
			continue
		case e.Thread == nil:
			page.links = append(page.links, "")
			comment(e.Comment, "")
			continue
		}
		page.links = append(page.links, linkThread+threadKey(*e.Thread))
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
		page.lines = append(page.lines, hunkTail(t, e.Thread.Hunk, width)...)
		for _, c := range e.Thread.Comments {
			comment(c, "  ")
		}
	}
	return page
}

// hunkTail is the last lines of a thread's diff hunk, which end at its line,
// colored as the Diff colors them.
func hunkTail(t theme, hunk string, width int) []string {
	var lines []string
	for line := range strings.SplitSeq(strings.TrimRight(hunk, "\n"), "\n") {
		if !strings.HasPrefix(line, "@@") {
			lines = append(lines, line)
		}
	}
	lines = lines[max(0, len(lines)-threadHunkLines):]
	for i, line := range lines {
		p := t.muted
		switch {
		case strings.HasPrefix(line, "+"):
			p = t.green
		case strings.HasPrefix(line, "-"):
			p = t.red
		}
		lines[i] = "  " + p.render(ansi.Truncate(expandTabs(textutil.Line(line)), max(1, width-2), "…"))
	}
	return lines
}

// fileLine is a status letter, the path (old → new for a rename) cut from
// the left so the file name stays, and its unresolved review threads and
// line counts at the right.
func fileLine(t theme, f changes.File, width, unresolved int) string {
	glyph := map[string]string{
		changes.FileAdded: t.green.render("A"), changes.FileDeleted: t.red.render("D"),
		changes.FileRenamed: t.magenta.render("R"), changes.FileBinary: t.muted.render("B"),
		changes.FileUncommitted: t.yellow.render("U"), changes.FileUnchanged: t.muted.render("·"),
	}[f.Status]
	if glyph == "" {
		glyph = t.accent.render("M")
	}
	tail := "  " + t.green.render("+"+strconv.Itoa(f.Additions)) + " " + t.red.render("−"+strconv.Itoa(f.Deletions))
	switch f.Status {
	case changes.FileBinary:
		tail = "  " + t.muted.render("binary")
	case changes.FileUnchanged:
		tail = ""
	}
	if unresolved > 0 {
		tail = "  " + t.yellow.render("● "+strconv.Itoa(unresolved)) + tail
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

// shownCheckIn is check as review has it now, so a refresh shows its new
// state; one no longer on the head stays as it was.
func shownCheckIn(r *changes.Review, check changes.Check) changes.Check {
	if r != nil {
		for _, c := range r.Checks {
			if checkLink(c) == checkLink(check) {
				return c
			}
		}
	}
	return check
}

// checkPage draws the check view: the check's name, exact state, duration,
// and start, then its Steps, Annotations, and Log, or a check run's Output or
// a commit status's Description. Each line O can open is an entry whose id is
// its URL; the Log entry opens the job.
func checkPage(check changes.Check, readErr error, request detailRequest, render func(string, int, string) (string, error)) rendered {
	t, width := request.theme, max(1, request.width)
	now := cmp.Or(request.now, time.Now())
	var page rendered
	entry := func(line, id string) {
		page.entries, page.ids, page.links = append(page.entries, len(page.lines)), append(page.ids, id), append(page.links, "")
		page.lines = append(page.lines, ansi.Truncate(strings.TrimRight(line, " "), width, "…"))
	}
	section := func(label string) {
		page.lines = append(page.lines, "", t.bold.render(label))
	}
	// row places right, muted, at the end of a line of left, dropping it when
	// the line has no room for both.
	row := func(left, right string) string {
		room := width - ansi.StringWidth(left) - 2
		if right == "" || room < ansi.StringWidth(right) {
			return left
		}
		return left + blank(width-ansi.StringWidth(left)-ansi.StringWidth(right)) + t.muted.render(right)
	}
	var facts []string
	for _, fact := range []string{cmp.Or(check.Status, check.State), checkDuration(check, now), age(check.StartedAt, now)} {
		if fact != "" {
			facts = append(facts, fact)
		}
	}
	name := ansi.Truncate(textutil.Line(check.Name), max(1, width-4), "…")
	entry(row(checkGlyph(t, check)+" "+name, strings.Join(facts, " · ")), check.URL)
	if readErr != nil {
		page.lines = append(page.lines, "", t.yellow.render(ansi.Truncate("Check detail unavailable: "+textutil.Line(readErr.Error()), width, "…")))
	}
	if len(check.Steps) > 0 {
		section("Steps")
		for _, step := range check.Steps {
			s := changes.Check{Status: step.Status, State: changes.CheckCategory(step.Status), StartedAt: step.StartedAt, CompletedAt: step.CompletedAt}
			label := ansi.Truncate(textutil.Line(step.Name), max(1, width-14), "…")
			entry(row("  "+checkGlyph(t, s)+" "+label, checkDuration(s, now)), cmp.Or(step.URL, check.URL))
		}
	}
	if len(check.Annotations) > 0 {
		section("Annotations")
		for _, a := range check.Annotations {
			glyph := t.muted.render("•")
			switch a.Level {
			case "failure":
				glyph = t.red.render("✕")
			case "warning":
				glyph = t.yellow.render("!")
			}
			place := textutil.Line(a.Path)
			if a.Line > 0 {
				place += ":" + strconv.Itoa(a.Line)
			}
			text := strings.Join(strings.Fields(textutil.Line(cmp.Or(a.Message, a.Title))), " ")
			entry("  "+glyph+" "+place+"  "+t.muted.render(text), cmp.Or(a.URL, check.URL))
		}
	}
	if check.Title != "" || strings.TrimSpace(check.Summary) != "" {
		if check.ID == "" {
			section("Description")
		} else {
			section("Output")
		}
		if check.Title != "" {
			page.lines = append(page.lines, ansi.Truncate(textutil.Line(check.Title), width, "…"))
		}
		if strings.TrimSpace(check.Summary) != "" {
			body := cappedBody{limit: bodyLimit}
			if request.expanded {
				body.limit = bodyMoreLimit
			}
			body.write(check.Summary)
			page.truncated = body.full
			page.lines = append(page.lines, markdownLines(body.builder.String(), width, t, render)...)
		}
	}
	if check.Log != "" || check.LogUnavailable != "" {
		section("Log")
		if check.Log == "" {
			entry("  "+t.muted.render(textutil.Line(check.LogUnavailable)), check.URL)
		} else {
			page.entries, page.ids, page.links = append(page.entries, len(page.lines)), append(page.ids, check.URL), append(page.links, "")
			for _, line := range strings.Split(check.Log, "\n") {
				line = ansi.Truncate("  "+textutil.Line(line), width, "…")
				switch changes.LogLevel(strings.TrimSpace(line)) {
				case "error":
					line = t.red.render(line)
				case "warning":
					line = t.yellow.render(line)
				}
				page.lines = append(page.lines, line)
			}
		}
	}
	return page
}

// furthestPull is the furthest of cs's open pull requests, its index in cs,
// and how many cs has.
func furthestPull(cs []changes.Change) (changes.Change, int, int) {
	var first changes.Change
	at, n := -1, 0
	for i, c := range cs {
		if c.Stage == changes.StagePR && c.Number > 0 && (c.State == changes.StateOpen || c.State == changes.StateDraft) {
			if n == 0 {
				first, at = c, i
			}
			n++
		}
	}
	return first, at, n
}

// openPull is gp: it opens the selected item's furthest open pull request in
// the change view, or its Changes tab when it has several.
func (m *Model) openPull() tea.Cmd {
	m.count = 0
	m.inputSerial++
	row, ok := m.selectedRow()
	c, at, n := furthestPull(row.Changes)
	if !ok || n == 0 {
		m.notice = "Disabled: no open pull request"
		return nil
	}
	m.detailOpen, m.detailFocus = true, true
	m.change, m.changeParent, m.find = nil, nil, findState{}
	if n > 1 {
		return m.switchDetailTab(tabChanges)
	}
	m.showChange(row, c, at, tabChangeOverview)
	return m.showDetail()
}

// showChange shows c, row's change at index at of its Changes, on tab, so
// backspace returns to that entry.
func (m *Model) showChange(row engine.Row, c changes.Change, at int, tab detailTab) {
	m.change, m.changeParent, m.changeRef, m.changeTab, m.changeCursor = &c, nil, row.Ref, tabChanges, at
	m.detailTab, m.detailExpanded, m.entryCursor = tab, false, 0
	m.find, m.checksOpen = findState{}, false
	m.threadsToggled, m.resolvedOpen, m.changeReturn, m.diffTarget = nil, false, nil, ""
}

// pullItem reports whether row is a github-pulls item, which detail opens in
// its own pull request's change view (design.md D44).
func (m Model) pullItem(row engine.Row) bool {
	return m.sourceType(row.Ref.Source) == "github-pulls"
}

// pollPull reads the shown pull request again on a forge refresh: the change
// view's, or each open one the Changes tab lists. Only its review is read
// fresh, on the detail worker; nothing reads a pull request that isn't shown.
func (m *Model) pollPull() {
	row, ok := m.selectedRow()
	if !m.detailOpen || m.detailWorker == nil || !ok {
		return
	}
	open := func(c changes.Change) bool {
		return c.Number > 0 && (c.State == changes.StateOpen || c.State == changes.StateDraft)
	}
	if m.change != nil && !open(m.shownChange()) || m.change == nil && (m.detailTab != tabChanges || !slices.ContainsFunc(row.Changes, open)) {
		return
	}
	request := m.detailRequest(row)
	request.poll = true
	m.detailWorker.submit(request)
}

// pullNotice says what changed on the shown pull request since the last read
// of the same page: a check that finished, or a new verdict. A stale read
// says nothing, since it carries no news.
func (m Model) pullNotice(result detailResult) string {
	if m.detailShownRef != result.identity.Ref || m.detailShownTab != m.detailTab {
		return ""
	}
	if result.facts != nil {
		if m.changeFacts == nil || m.review.read == nil || result.review == nil || m.changeFacts.ID != result.facts.ID {
			return ""
		}
		return pullNotice(result.facts.Number, m.review.read.Checks, result.review.Checks, m.changeFacts.Merge, result.facts.Merge)
	}
	for _, c := range result.changes {
		for _, was := range m.detailChanges {
			if was.ID == c.ID {
				if notice := pullNotice(c.Number, was.CheckRuns, c.CheckRuns, was.Merge, c.Merge); notice != "" {
					return notice
				}
			}
		}
	}
	return ""
}

// pullNotice is the status bar's news between two reads of pull request
// number: the first check that failed, all checks passing, or one that
// finished, then the verdict if it changed, such as ✕ test failed · #45
// blocked.
func pullNotice(number int, before, after []changes.Check, was, now *changes.Merge) string {
	stale := func(m *changes.Merge) bool {
		return slices.ContainsFunc(m.Reasons, func(r changes.Reason) bool { return r.Kind == changes.ReasonStale })
	}
	if was == nil || now == nil || stale(was) || stale(now) {
		return ""
	}
	running := map[string]bool{}
	for _, c := range before {
		running[c.Name] = c.State == changes.ChecksPending
	}
	var finished []changes.Check
	pending, failing := false, false
	for _, c := range after {
		pending = pending || c.State == changes.ChecksPending
		failing = failing || c.State == changes.ChecksFailing
		if running[c.Name] && c.State != changes.ChecksPending && c.State != changes.ChecksStale {
			finished = append(finished, c)
		}
	}
	var parts []string
	if i := slices.IndexFunc(finished, func(c changes.Check) bool { return c.State == changes.ChecksFailing }); i >= 0 {
		parts = append(parts, "✕ "+textutil.Line(finished[i].Name)+" "+cmp.Or(finished[i].Status, changes.CheckFailed))
	} else if len(finished) > 0 && !pending && !failing {
		parts = append(parts, "✓ checks passed")
	} else if len(finished) > 0 {
		parts = append(parts, "✓ "+textutil.Line(finished[0].Name)+" "+cmp.Or(finished[0].Status, changes.CheckPassed))
	}
	if was.Verdict != now.Verdict {
		parts = append(parts, "#"+strconv.Itoa(number)+" "+now.Verdict)
	}
	return strings.Join(parts, " · ")
}
