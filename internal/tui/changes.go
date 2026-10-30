package tui

import (
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/history"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// stageGlyph marks a change's furthest stage, or ? when its state is unknown
// or stale (design.md D43).
func stageGlyph(t theme, c changes.Change) string {
	switch {
	case c.State == changes.StateUnknown || c.State == changes.StateStale:
		return t.yellow.render("?")
	case c.Stage == changes.StageLanded:
		return t.green.render("◆")
	case c.Stage == changes.StagePR && c.State == changes.StateMerged:
		return t.magenta.render("⇡")
	case c.Stage == changes.StagePR && c.State == changes.StateClosed:
		return t.red.render("⇡")
	case c.Stage == changes.StagePR && c.State == changes.StateDraft:
		return t.muted.render("⇡")
	case c.Stage == changes.StagePR:
		return t.accent.render("⇡")
	case c.Stage == changes.StageBranch:
		return t.accent.render("⎇")
	}
	return t.accent.render("◇")
}

// checksGlyph is empty without checks. Stale checks never read as passing.
func checksGlyph(t theme, checks string) string {
	switch checks {
	case changes.ChecksPassing:
		return t.green.render("✓")
	case changes.ChecksFailing:
		return t.red.render("✕")
	case changes.ChecksPending:
		return t.yellow.render("●")
	case changes.ChecksStale:
		return t.muted.render("?")
	}
	return ""
}

// changeCell is the list column: the most advanced change's stage, label, and
// checks, such as ⇡#45 ✓, padded to width.
func changeCell(t theme, cs []changes.Change, width int) string {
	c, ok := changes.Furthest(cs)
	if !ok {
		return blank(width)
	}
	checks := checksGlyph(t, c.Checks)
	room := width - 1
	if checks != "" {
		room -= 2
		checks = " " + checks
	}
	return stageGlyph(t, c) + t.muted.render(cell(textutil.Line(changes.Label(c)), max(0, room))) + checks
}

// changeWidth is the width changeCell needs for cs.
func changeWidth(cs []changes.Change) int {
	c, ok := changes.Furthest(cs)
	if !ok {
		return 0
	}
	n := 1 + textWidth(changes.Label(c))
	if c.Checks != "" {
		n += 2
	}
	return n
}

// changeLines draws one entry per change, furthest first: glyph, label, title,
// and state, checks, review, size, and age; then its branch, worktree, and how
// far it is from the default branch; then why it is unknown or stale.
func changeLines(cs []changes.Change, t theme, now time.Time, width int) rendered {
	var page rendered
	if len(cs) == 0 {
		page.lines = []string{t.muted.render("No linked changes.")}
		return page
	}
	for i, c := range cs {
		if i > 0 {
			page.lines = append(page.lines, "")
		}
		label := textutil.Line(changes.Label(c))
		head := stageGlyph(t, c) + " " + t.bold.render(label) + "  "
		tail := "  " + changeState(t, c)
		for _, part := range []string{checksGlyph(t, c.Checks), changeReview(t, c.Review), changeSize(t, c), t.muted.render(age(c.Updated, now))} {
			if part != "" {
				tail += "  " + part
			}
		}
		title := ""
		if c.Title != label {
			title = textutil.Line(c.Title)
		}
		room := max(1, width-ansi.StringWidth(head)-ansi.StringWidth(tail))
		page.entries, page.changes = append(page.entries, len(page.lines)), append(page.changes, c)
		title = ansi.Truncate(title, room, "…")
		page.lines = append(page.lines, head+title+blank(room-ansi.StringWidth(title))+tail)
		var facts []string
		if c.Branch != "" && c.Branch != label {
			facts = append(facts, textutil.Line(c.Branch))
		}
		if c.Worktree != "" {
			facts = append(facts, textutil.Line(worktreePath(c)))
		}
		if c.Ahead != nil && *c.Ahead > 0 {
			facts = append(facts, "↑"+strconv.Itoa(*c.Ahead))
		}
		if c.Behind != nil && *c.Behind > 0 {
			facts = append(facts, "↓"+strconv.Itoa(*c.Behind))
		}
		if c.Dirty != nil && *c.Dirty > 0 {
			facts = append(facts, strconv.Itoa(*c.Dirty)+" dirty")
		}
		if len(facts) > 0 {
			page.lines = append(page.lines, t.muted.render(ansi.Truncate("  "+strings.Join(facts, " · "), width, "…")))
		}
		if c.Reason != "" {
			page.lines = append(page.lines, t.yellow.render(ansi.Truncate("  "+textutil.Line(c.Reason), width, "…")))
		}
	}
	return page
}

func changeState(t theme, c changes.Change) string {
	switch {
	case c.State == changes.StateUnknown || c.State == changes.StateStale:
		return t.yellow.render(c.State)
	case c.Stage == changes.StageLanded:
		return t.green.render("landed")
	case c.Stage != changes.StagePR:
		return t.muted.render(c.Stage)
	case c.State == changes.StateMerged:
		return t.magenta.render(c.State)
	case c.State == changes.StateClosed:
		return t.red.render(c.State)
	case c.State == changes.StateDraft:
		return t.muted.render(c.State)
	}
	return t.accent.render(c.State)
}

func changeReview(t theme, review string) string {
	switch review {
	case changes.ReviewApproved:
		return t.green.render("approved")
	case changes.ReviewChanges:
		return t.red.render("changes requested")
	case changes.ReviewRequested:
		return t.yellow.render("review requested")
	}
	return ""
}

func changeSize(t theme, c changes.Change) string {
	if c.Additions == 0 && c.Deletions == 0 {
		return ""
	}
	return t.green.render("+"+strconv.Itoa(c.Additions)) + " " + t.red.render("−"+strconv.Itoa(c.Deletions))
}

// changesVersion changes when anything the Changes tab draws from the row
// does, so a refresh redraws the open tab.
func changesVersion(cs []changes.Change) string {
	if len(cs) == 0 {
		return ""
	}
	h := fnv.New64a()
	for _, c := range cs {
		for _, s := range []string{c.ID, c.Stage, c.State, c.Reason, c.Checks, c.Review, c.Head, c.Title, c.URL, c.Worktree, c.Updated.String()} {
			h.Write([]byte(s))
			h.Write([]byte{0})
		}
		for _, n := range []*int{&c.Additions, &c.Deletions, c.Ahead, c.Behind, c.Dirty} {
			if n != nil {
				h.Write(strconv.AppendInt(nil, int64(*n), 10))
			}
			h.Write([]byte{0})
		}
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

// linkedChange is what an action run on c may name as {change.*}.
func linkedChange(c changes.Change) hooks.LinkedChange {
	l := hooks.LinkedChange{URL: c.URL, Branch: c.Branch, Head: c.Head, Base: c.Base(), Path: c.Worktree}
	if c.Number > 0 {
		l.Number = strconv.Itoa(c.Number)
	}
	return l
}

// selectedChange is the change the change view shows, or the Changes entry
// under the detail cursor.
func (m Model) selectedChange() (changes.Change, bool) {
	if m.detailOpen && m.change != nil {
		return m.shownChange(), true
	}
	if !m.detailOpen || m.detailTab != tabChanges || !m.detailCurrent() || m.entryCursor < 0 || m.entryCursor >= len(m.detailChanges) {
		return changes.Change{}, false
	}
	return m.detailChanges[m.entryCursor], true
}

// yankChange is the change yp copies: the one under the Changes cursor, else
// the item's most advanced.
func (m Model) yankChange() changes.Change {
	if c, ok := m.selectedChange(); ok {
		return c
	}
	row, _ := m.selectedRow()
	c, _ := changes.Furthest(row.Changes)
	return c
}

// yankCommit is the SHA ys copies: the History or change view commit, or the
// change under the cursor.
func (m Model) yankCommit() string {
	if m.detailOpen && m.detailCurrent() && m.detailTab == tabHistory && m.entryCursor >= 0 && m.entryCursor < len(m.detailIDs) {
		sha, _ := history.CommitOf(m.detailIDs[m.entryCursor])
		return sha
	}
	if m.detailOpen && m.detailCurrent() && m.detailTab == tabChangeCommits && m.entryCursor >= 0 && m.entryCursor < len(m.detailIDs) {
		return m.detailIDs[m.entryCursor]
	}
	c, _ := m.selectedChange()
	return c.Head
}
