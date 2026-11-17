package tui

import (
	"cmp"
	"errors"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// The Diff shows a pull request's review threads and drafted line comments
// under the line they're on, on its side in a split diff. An unresolved
// thread shows whole and a resolved one folds to a line; enter or za toggles
// one. An outdated thread, or one on a line the diff lacks, shows only in
// Conversation.

// noteLines caps each comment's body in the Diff.
const noteLines = 10

// threadKey names a thread across reads: its forge ID, or its place.
func threadKey(t changes.Thread) string {
	side := ""
	if t.Old {
		side = "−"
	}
	return cmp.Or(t.ID, t.Path+":"+side+strconv.Itoa(t.Line))
}

// threadShown is whether a thread shows whole: unresolved unless toggled.
func threadShown(t changes.Thread, toggled map[string]bool) bool {
	return !t.Resolved != toggled[threadKey(t)]
}

// toggledSet reads a request's toggled thread keys, one per line.
func toggledSet(keys string) map[string]bool {
	set := map[string]bool{}
	for key := range strings.SplitSeq(keys, "\n") {
		if key != "" {
			set[key] = true
		}
	}
	return set
}

// diffNotes are what the Diff shows of a pull request's review: its threads
// at head that aren't outdated, the drafted line comments and replies, and
// unresolved threads by path.
type diffNotes struct {
	threads    []changes.Thread
	toggled    map[string]bool
	pending    map[string]bool // unresolved thread keys
	unresolved map[string]int
	drafts     []changes.LineComment
	replies    map[string][]string
	marks      map[string]string // x's pending or refused write by thread ID
}

func reviewNotes(c changes.Change, r *changes.Review, draft *changes.Draft, toggled string, marks map[string]string) diffNotes {
	notes := diffNotes{toggled: toggledSet(toggled), pending: map[string]bool{}, unresolved: map[string]int{}, replies: map[string][]string{}, marks: marks}
	if r != nil {
		for _, t := range r.Threads {
			if !t.Resolved {
				notes.unresolved[t.Path]++
			}
			// A review read for another head numbers other lines.
			if len(t.Comments) == 0 || t.Outdated || t.Line == 0 || r.Head != "" && r.Head != c.Head {
				continue
			}
			notes.threads = append(notes.threads, t)
			if !t.Resolved {
				notes.pending[threadKey(t)] = true
			}
		}
	}
	if draft != nil {
		for _, comment := range draft.Comments {
			if comment.Head == c.Head {
				notes.drafts = append(notes.drafts, comment)
			}
		}
		for _, reply := range draft.Replies {
			notes.replies[reply.Thread] = append(notes.replies[reply.Thread], reply.Body)
		}
	}
	return notes
}

// fileThreads counts by path the threads on f, by its new or old path.
func fileThreads(byPath map[string]int, f changes.File) int {
	n := byPath[f.Path]
	if f.From != "" && f.From != f.Path {
		n += byPath[f.From]
	}
	return n
}

// fileNotes are the notes on one file's lines.
type fileNotes struct {
	threads []changes.Thread
	shown   []bool
	replies [][]string
	marks   []string
	drafts  []changes.LineComment
}

// on is the notes on f, which a thread names by its new or old path.
func (n diffNotes) on(f changes.File) fileNotes {
	var out fileNotes
	for _, t := range n.threads {
		if t.Path == f.Path || f.From != "" && t.Path == f.From {
			out.threads = append(out.threads, t)
			out.shown = append(out.shown, threadShown(t, n.toggled))
			out.replies = append(out.replies, n.replies[t.ID])
			out.marks = append(out.marks, n.marks[t.ID])
		}
	}
	for _, d := range n.drafts {
		if d.Path == f.Path {
			out.drafts = append(out.drafts, d)
		}
	}
	return out
}

// digest keys a file's render to its notes as drawn.
func (n fileNotes) digest() string {
	if len(n.threads) == 0 && len(n.drafts) == 0 {
		return ""
	}
	h := fnv.New64a()
	write := func(parts ...string) {
		for _, part := range parts {
			h.Write([]byte(part))
			h.Write([]byte{0})
		}
	}
	for i, t := range n.threads {
		write(threadKey(t), strconv.FormatBool(t.Old), strconv.Itoa(t.Line), strconv.FormatBool(t.Resolved), strconv.FormatBool(n.shown[i]), n.marks[i])
		for _, c := range t.Comments {
			write(c.Author, c.Body)
		}
		write(n.replies[i]...)
	}
	for _, d := range n.drafts {
		write(strconv.FormatBool(d.Old), strconv.Itoa(d.Line), d.Body)
	}
	return "\x00notes:" + strconv.FormatUint(h.Sum64(), 36)
}

// placeNotes puts each thread and drafted comment under the row showing its
// line on its side. A thread is one entry, its first line; a draft is none.
func placeNotes(in rendered, on fileNotes, t theme, width, digits int, split bool) rendered {
	type place struct {
		old  bool
		line int
	}
	threads, drafts := map[place][]int{}, map[place][]int{}
	for i, th := range on.threads {
		threads[place{th.Old, th.Line}] = append(threads[place{th.Old, th.Line}], i)
	}
	for i, d := range on.drafts {
		drafts[place{d.Old, d.Line}] = append(drafts[place{d.Old, d.Line}], i)
	}
	// A note's text starts under the code, in its half of a split diff.
	left := (width - 1) / 2
	block := func(old bool) (int, int) {
		switch {
		case !split:
			return 2*digits + 2, width - 2*digits - 2
		case old:
			return digits + 1, left - digits - 1
		}
		return left + digits + 2, width - left - digits - 2
	}
	out := rendered{entries: make([]int, 0, len(in.rows)+len(on.threads)), hunks: make([]int, len(in.hunks))}
	moved := make([]int, len(in.lines))
	for j, line := range in.lines {
		moved[j] = len(out.lines)
		out.entries = append(out.entries, len(out.lines))
		out.lines = append(out.lines, line)
		r := in.rows[j]
		out.rows = append(out.rows, r)
		for _, at := range []place{{true, r.old}, {false, r.new}} {
			if at.line == 0 {
				continue
			}
			indent, room := block(at.old)
			for _, i := range threads[at] {
				out.entries = append(out.entries, len(out.lines))
				out.rows = append(out.rows, diffRow{file: r.file, thread: threadKey(on.threads[i])})
				for _, l := range threadBlock(t, on.threads[i], on.shown[i], on.replies[i], on.marks[i], max(1, room)) {
					out.lines = append(out.lines, blank(indent)+l)
				}
			}
			for _, i := range drafts[at] {
				for _, l := range draftBlock(t, on.drafts[i].Body, max(1, room)) {
					out.lines = append(out.lines, blank(indent)+l)
				}
			}
			delete(threads, at)
			delete(drafts, at)
		}
	}
	for i, h := range in.hunks {
		out.hunks[i] = moved[h]
	}
	return out
}

// threadMark is a thread's state as drawn: state, or x's write instead, a
// refusal in red with the forge's reason.
func threadMark(t theme, write, state string) string {
	switch {
	case write == "":
		return state
	case strings.HasPrefix(write, "✕"):
		return t.red.render(write)
	}
	return t.accent.render(write)
}

// threadBlock draws a thread behind a bar: whole, each comment's author then
// its text, or folded to its first comment on one line with how many there
// are, and x's pending or refused write at the end of its first line.
// Drafted replies follow it.
func threadBlock(t theme, th changes.Thread, shown bool, replies []string, write string, room int) []string {
	bar, mark := t.yellow.render("┃")+" ", t.yellow.render("●")
	if th.Resolved {
		bar, mark = t.faint.render("┃")+" ", t.green.render("✓")
	}
	inner := max(1, room-2)
	first := th.Comments[0]
	if write != "" {
		write = "  " + threadMark(t, write, "")
	}
	if !shown {
		tail := ""
		if n := len(th.Comments) + len(replies); n > 1 {
			tail = "  " + t.muted.render(strconv.Itoa(n))
		}
		tail += write
		head := mark + " " + t.bold.render(textutil.Line(first.Author)) + " " + t.muted.render(strings.TrimSpace(textutil.Line(first.Body)))
		return []string{bar + ansi.Truncate(head, max(1, inner-ansi.StringWidth(tail)), "…") + tail}
	}
	var out []string
	for i, c := range th.Comments {
		head := t.bold.render(textutil.Line(c.Author))
		if i == 0 {
			head = ansi.Truncate(mark+" "+head, max(1, inner-ansi.StringWidth(write)), "…") + write
		} else {
			head = "  " + head
		}
		out = append(out, bar+ansi.Truncate(head, inner, "…"))
		for _, line := range noteBody(c.Body, inner-2) {
			out = append(out, bar+"  "+line)
		}
	}
	for _, body := range replies {
		out = append(out, bar+"  "+t.magenta.render("✎ draft"))
		for _, line := range noteBody(body, inner-2) {
			out = append(out, bar+"  "+line)
		}
	}
	return out
}

// draftBlock draws a drafted line comment, marked draft.
func draftBlock(t theme, body string, room int) []string {
	bar := t.magenta.render("┃") + " "
	inner := max(1, room-2)
	out := []string{bar + t.magenta.render("✎ draft")}
	for _, line := range noteBody(body, inner-2) {
		out = append(out, bar+"  "+line)
	}
	return out
}

// noteBody wraps a comment to width, cut after noteLines lines.
func noteBody(body string, width int) []string {
	text := strings.TrimSpace(expandTabs(textutil.Sanitize(body)))
	if text == "" {
		return nil
	}
	lines := strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
	if len(lines) > noteLines {
		lines = append(lines[:noteLines-1], "…")
	}
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return lines
}

// threadAt is the review thread key names, or nil.
func threadAt(r *changes.Review, key string) *changes.Thread {
	if r == nil || key == "" {
		return nil
	}
	i := slices.IndexFunc(r.Threads, func(t changes.Thread) bool { return threadKey(t) == key })
	if i < 0 {
		return nil
	}
	return &r.Threads[i]
}

// commentRender renders a comment's Markdown as the description renders,
// keeping each body's render with the Diff's files.
func (worker *detailWorker) commentRender(body string, width int, theme string) (string, error) {
	if worker.render == nil {
		return "", errors.New("no renderer")
	}
	key := renderKey{Width: width, Theme: theme, Tab: tabChangeConversation, Dependency: patchDigest(body)}
	if page, ok := worker.diffs.render(key); ok {
		return page.lines[0], nil
	}
	out, err := worker.render(body, width, theme)
	if err == nil {
		worker.diffs.putRender(key, rendered{lines: []string{out}, size: len(out)})
	}
	return out, err
}
