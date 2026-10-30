package tui

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// A pull request's pending review is drafted in the change view: c comments
// on the pull request, on the Diff line under the cursor, or in reply to the
// Conversation thread under it, and r submits it (design.md D43).

// reviewer keeps and submits pull request review drafts; *changes.Tracker is
// one.
type reviewer interface {
	Draft(changes.Change) (changes.Draft, error)
	SaveDraft(changes.Draft) error
	Submit(context.Context, changes.Change, string) (changes.Draft, error)
}

// reviewState is the change view's review: the last read and draft, an
// editor open on one part of the draft, the submit prompt, and a submission
// in flight.
type reviewState struct {
	read    *changes.Review
	draft   *changes.Draft
	failed  string // why the draft can't be read
	edit    *reviewEdit
	prompt  bool
	sending bool
}

// reviewEdit is the draft part an editor is open on: the review comment, a
// line comment, or a reply.
type reviewEdit struct {
	change   changes.Change
	comment  *changes.LineComment
	reply    *changes.Reply
	path     string
	original []byte
}

type reviewPreparedMsg struct {
	edit reviewEdit
	err  error
}
type reviewSavedMsg struct {
	message string
	err     error
}
type reviewSubmittedMsg struct{ err error }

// reviewBlocked says why review actions are disabled, or "".
func (m Model) reviewBlocked() string {
	switch {
	case m.change != nil && m.change.IsCommit():
		return "reviews comment on the pull request's head, not one commit"
	case m.change == nil || m.change.Number == 0:
		return "open a pull request in the change view"
	case m.reviewer == nil:
		return "no forge is configured"
	case m.review.failed != "":
		return m.review.failed
	case m.review.read == nil || m.review.draft == nil:
		return "the pull request is still loading"
	case m.review.sending:
		return "a review is being submitted"
	}
	return m.review.read.Blocked
}

// beginReviewDraft opens the editor on the draft part under the cursor: a
// Diff line's comment, a Conversation thread's reply, an Overview draft
// entry, else the review comment.
func (m *Model) beginReviewDraft() tea.Cmd {
	if reason := m.reviewBlocked(); reason != "" {
		m.notice = "Disabled: " + reason
		return nil
	}
	d := *m.review.draft
	edit := reviewEdit{change: m.shownChange()}
	text := d.Body
	id := ""
	if m.detailCurrent() && m.entryCursor >= 0 && m.entryCursor < len(m.detailIDs) {
		id = m.detailIDs[m.entryCursor]
	}
	switch {
	case m.detailTab == tabChangeDiff:
		anchors := m.cursorAnchors()
		if len(anchors) == 0 || anchors[0].line == 0 {
			m.notice = "Move the cursor to a line to comment on it."
			return nil
		}
		a := anchors[0]
		// An uncommitted file's lines aren't the head's, so a comment on one
		// would post onto another line.
		if slices.Contains(m.detailDirty, a.file) {
			m.notice = textutil.Line(a.file) + " has uncommitted changes; commit and push it to comment on it."
			return nil
		}
		edit.comment = &changes.LineComment{Head: a.head, Path: a.file, Old: a.side == sideOld, Line: a.line}
		text = ""
		for _, c := range d.Comments {
			if c.Head == a.head && c.Path == a.file && c.Old == edit.comment.Old && c.Line == a.line {
				text = c.Body
			}
		}
	case m.detailTab == tabChangeConversation:
		entries := m.review.read.Conversation()
		if m.entryCursor >= 0 && m.entryCursor < len(entries) && entries[m.entryCursor].Thread != nil && entries[m.entryCursor].Thread.ID != "" {
			t := entries[m.entryCursor].Thread
			edit.reply = &changes.Reply{Thread: t.ID, Path: t.Path, Line: t.Line}
			text = ""
			for _, r := range d.Replies {
				if r.Thread == t.ID {
					text = r.Body
				}
			}
		}
	case strings.HasPrefix(id, draftComment):
		if i, err := strconv.Atoi(strings.TrimPrefix(id, draftComment)); err == nil && i < len(d.Comments) {
			edit.comment = new(d.Comments[i])
			text = d.Comments[i].Body
		}
	case strings.HasPrefix(id, draftReply):
		if i, err := strconv.Atoi(strings.TrimPrefix(id, draftReply)); err == nil && i < len(d.Replies) {
			edit.reply = new(d.Replies[i])
			text = d.Replies[i].Body
		}
	}
	return func() tea.Msg {
		file, err := os.CreateTemp("", "bl-tui-review-*.md")
		if err != nil {
			return reviewPreparedMsg{err: err}
		}
		edit.path, edit.original = file.Name(), []byte(text+"\n")
		_, err = file.Write(edit.original)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		return reviewPreparedMsg{edit: edit, err: err}
	}
}

func (m Model) reviewPrepared(msg reviewPreparedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.notice = "Editor: " + boundedLine(msg.err.Error())
		return m, nil
	}
	m.review.edit = &msg.edit
	return m, m.editorProcess(msg.edit.path)
}

// reviewEditorFinished saves the edited part into the draft as it is on
// disk now; a blank part is removed.
func (m Model) reviewEditorFinished(msg editFinishedMsg) (tea.Model, tea.Cmd) {
	edit := *m.review.edit
	m.review.edit = nil
	if msg.err != nil {
		m.notice = "Editor failed; buffer retained at " + edit.path + ": " + boundedLine(msg.err.Error())
		return m, nil
	}
	store := m.reviewer
	return m, func() tea.Msg {
		text, err := os.ReadFile(edit.path)
		if err != nil {
			return reviewSavedMsg{err: err}
		}
		if bytes.Equal(text, edit.original) {
			_ = os.Remove(edit.path)
			return reviewSavedMsg{message: "Draft unchanged."}
		}
		d, err := store.Draft(edit.change)
		if err == nil {
			body := string(text)
			switch {
			case edit.comment != nil:
				c := *edit.comment
				c.Body = body
				d = d.WithComment(c)
			case edit.reply != nil:
				r := *edit.reply
				r.Body = body
				d = d.WithReply(r)
			default:
				d = d.WithBody(body)
			}
			err = store.SaveDraft(d)
		}
		if err != nil {
			return reviewSavedMsg{err: &retainedError{path: edit.path, err: err}}
		}
		_ = os.Remove(edit.path)
		return reviewSavedMsg{message: "Draft saved."}
	}
}

// retainedError names the editor buffer kept after a failed save.
type retainedError struct {
	path string
	err  error
}

func (e *retainedError) Error() string {
	return e.err.Error() + "; buffer retained at " + e.path
}

// beginSubmitReview opens the submit prompt.
func (m *Model) beginSubmitReview() tea.Cmd {
	if reason := m.reviewBlocked(); reason != "" {
		m.notice = "Disabled: " + reason
		return nil
	}
	m.review.prompt = true
	return nil
}

// updateReviewPrompt submits the draft as a comment, approval, or request
// for changes, or discards it. A draft whose last outcome is unknown can
// only be discarded.
func (m *Model) updateReviewPrompt(key string) tea.Cmd {
	event := map[string]string{"c": changes.EventComment, "a": changes.EventApprove, "r": changes.EventRequestChanges}[key]
	switch {
	case key == "esc" || key == "q":
		m.review.prompt = false
		return nil
	case key == "d":
		m.review.prompt = false
		store, d := m.reviewer, changes.Draft{URL: m.review.draft.URL}
		return func() tea.Msg {
			if err := store.SaveDraft(d); err != nil {
				return reviewSavedMsg{err: err}
			}
			return reviewSavedMsg{message: "Draft discarded."}
		}
	case event == "" || m.review.draft.Unknown != "":
		return nil
	}
	m.review.prompt, m.review.sending = false, true
	m.notice = "Submitting review…"
	store, ctx, c := m.reviewer, m.opts.Context, m.shownChange()
	return func() tea.Msg {
		_, err := store.Submit(ctx, c, event)
		return reviewSubmittedMsg{err: err}
	}
}

func (m Model) reviewSaved(msg reviewSavedMsg) (tea.Model, tea.Cmd) {
	m.notice = msg.message
	if msg.err != nil {
		m.notice = "Draft: " + boundedLine(msg.err.Error())
	}
	return m, m.showDetail()
}

func (m Model) reviewSubmitted(msg reviewSubmittedMsg) (tea.Model, tea.Cmd) {
	m.review.sending = false
	m.notice = "Review submitted."
	if msg.err != nil {
		m.notice = "Review: " + boundedLine(msg.err.Error())
	}
	cmd := m.scheduleDetail()
	if m.detailOpen {
		m.submitDetail(true)
	}
	if m.opts.Changes != nil {
		m.opts.Changes.Refresh()
	}
	return m, cmd
}

// reviewOverlay asks how to submit the draft.
func (m Model) reviewOverlay(lines []string) []string {
	t := m.theme
	d := *m.review.draft
	choice := func(key, label string) string { return t.accent.render(key) + "  " + label }
	width := min(64, m.width-2)
	var body []string
	if d.Unknown != "" {
		body = append(body, t.red.render(ansi.Truncate(textutil.Line(d.Unknown), width-4, "…")), "", choice("d", "Discard draft"))
		return m.modal(lines, "Review", body, width, len(body)+2)
	}
	body = append(body, d.Summary())
	if n := d.Stale(m.review.read.Head); n > 0 {
		body = append(body, t.yellow.render(strconv.Itoa(n)+" stale: the head moved"))
	}
	body = append(body, "", choice("c", "Comment"), choice("a", "Approve"), choice("r", "Request changes"), choice("d", "Discard draft"))
	return m.modal(lines, "Submit review", body, width, len(body)+2)
}

// Overview draft entry IDs: the review comment, and each line comment and
// reply by index.
const (
	draftBody    = "draft:body"
	draftComment = "draft:comment:"
	draftReply   = "draft:reply:"
)

// draftLines lists the pending review on Overview, one entry per part, with
// stale line comments and the last outcome marked.
func draftLines(t theme, d *changes.Draft, failed, head string, width int) rendered {
	var page rendered
	if failed != "" {
		page.lines = append(page.lines, property(t, "Draft", t.yellow.render(ansi.Truncate(textutil.Line(failed), max(1, width-propertyWidth), "…"))))
		return page
	}
	if d == nil || d.Empty() && d.Unknown == "" && d.Error == "" {
		return page
	}
	page.lines = append(page.lines, property(t, "Draft", d.Summary()))
	entry := func(id, where, body, mark string) {
		page.entries, page.ids = append(page.entries, len(page.lines)), append(page.ids, id)
		head := blank(propertyWidth) + where
		tail := ""
		if mark != "" {
			tail = "  " + t.yellow.render(mark)
		}
		room := max(1, width-ansi.StringWidth(head)-ansi.StringWidth(tail)-2)
		text := ansi.Truncate(textutil.Line(body), room, "…")
		page.lines = append(page.lines, head+"  "+t.muted.render(text)+blank(room-ansi.StringWidth(text))+tail)
	}
	if d.Body != "" {
		entry(draftBody, "Comment", d.Body, "")
	}
	for i, c := range d.Comments {
		mark := ""
		if c.Head != head {
			mark = "stale"
		}
		entry(draftComment+strconv.Itoa(i), t.accent.render(textutil.Line(c.Where())), c.Body, mark)
	}
	for i, r := range d.Replies {
		where := textutil.Line(r.Path)
		if r.Line > 0 {
			where += ":" + strconv.Itoa(r.Line)
		}
		entry(draftReply+strconv.Itoa(i), "↳ "+t.accent.render(where), r.Body, "")
	}
	for _, reason := range []string{d.Unknown, d.Error} {
		if reason != "" {
			page.lines = append(page.lines, blank(propertyWidth)+t.red.render(ansi.Truncate(textutil.Line(reason), max(1, width-propertyWidth), "…")))
		}
	}
	return page
}
