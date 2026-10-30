package tui

import (
	"cmp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// The change view replaces the item's tabs with one linked change's
// Overview, Files, and Commits (design.md D43); backspace returns to the
// Changes entry it opened from.

// openChange opens the change view on c, from the Changes tab.
func (m *Model) openChange(c changes.Change) tea.Cmd {
	row, _ := m.selectedRow()
	m.change, m.changeRef, m.changeCursor = &c, row.Ref, m.entryCursor
	m.find = findState{}
	return m.switchDetailTab(tabChangeOverview)
}

// closeChange returns to the Changes entry the change view opened from.
func (m *Model) closeChange() tea.Cmd {
	cursor := m.changeCursor
	m.change = nil
	m.find = findState{}
	cmd := m.switchDetailTab(tabChanges)
	m.entryCursor = cursor
	return cmd
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
		return tabChanges
	}
	return m.detailTab
}

// changeViewPage draws one change view tab: Overview's properties, one
// entry per file, or one entry per commit.
func changeViewPage(c changes.Change, d changes.Detail, request detailRequest) rendered {
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
	default:
		page.lines = changeOverview(c, d, request)
	}
	if d.Reason != "" {
		page.lines = append(page.lines, "", t.yellow.render(ansi.Truncate(textutil.Line(d.Reason), width, "…")))
	}
	return page
}

// changeOverview is the change's property sheet.
func changeOverview(c changes.Change, d changes.Detail, request detailRequest) []string {
	t := request.theme
	var lines []string
	add := func(label, value string) {
		if value != "" {
			lines = append(lines, property(t, label, value))
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
	add("Review", changeReview(t, c.Review))
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
	if c.Reason != "" {
		lines = append(lines, "", t.yellow.render(ansi.Truncate(textutil.Line(c.Reason), max(1, request.width), "…")))
	}
	return lines
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
