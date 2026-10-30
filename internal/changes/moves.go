package changes

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/git"
)

// Moves carry a change's lines from Ref, an earlier head, to its head through
// the diffs between them, as a forge carries review comments forward: New
// diffs Ref to the head, and Old the old side at Ref to the head's, nil when
// it didn't move. OldKnown is false when either old side is unknown. Both
// carry patches.
type Moves struct {
	Ref      string
	New, Old []File
	OldKnown bool
	from     Sides
}

// ReadMoves reads c's moves from ref to its head, from Git objects only; ok
// is false when the checkout lacks either or ref isn't an ancestor of the
// head, as after history is rewritten.
func ReadMoves(ctx context.Context, c Change, ref string) (Moves, bool, error) {
	if c.dir == "" || c.Head == "" {
		return Moves{}, false, nil
	}
	if _, err := git.Run(ctx, c.dir, "merge-base", "--is-ancestor", ref, c.Head); err != nil {
		var exit *exec.ExitError
		if ctx.Err() != nil || !errors.As(err, &exit) {
			return Moves{}, false, err
		}
		return Moves{}, false, nil
	}
	from, ok, err := ReadSides(ctx, c, ref)
	if err != nil || !ok {
		return Moves{}, false, err
	}
	to, ok, err := ReadSides(ctx, c, c.Head)
	if err != nil || !ok {
		return Moves{}, false, err
	}
	m := Moves{Ref: ref, from: from, OldKnown: from.Old != "" && to.Old != ""}
	if m.New, err = gitFiles(ctx, c.dir, true, ref, c.Head); err != nil {
		return Moves{}, false, err
	}
	if m.OldKnown && from.Old != to.Old {
		if m.Old, err = gitFiles(ctx, c.dir, true, from.Old, to.Old); err != nil {
			return Moves{}, false, err
		}
	}
	return m, true, nil
}

// Range carries lines lo through hi of path, on the old side if old is set,
// to the head: the path there and the line lo moved to. ok is false when a
// line of the range was edited or deleted, or a line was added inside it.
// An old-side path follows a rename of its file's new side.
func (m Moves) Range(path string, old bool, lo, hi int) (string, int, bool) {
	files := m.New
	if old {
		if !m.OldKnown {
			return path, 0, false
		}
		for _, f := range m.New {
			if f.Status == FileRenamed && f.From == path {
				path = f.Path
			}
		}
		files = m.Old
	}
	for _, f := range files {
		switch {
		case f.From == path && f.From != "":
			path = f.Path
		case f.Path == path && f.Status != FileAdded:
		default:
			continue
		}
		if f.Status == FileDeleted || f.Status == FileBinary || f.Cut != "" {
			return path, 0, false
		}
		line, ok := moveRange(f.Patch, lo, hi)
		return path, line, ok
	}
	return path, lo, true
}

// File reads path as it was at Ref, on the old side if old is set, as
// Sides.File does.
func (m Moves) File(ctx context.Context, path string, old bool) ([]byte, bool, error) {
	if m.from.dir == "" {
		return nil, false, nil
	}
	return m.from.File(ctx, path, old)
}

// moveRange walks patch's hunks to where old lines lo through hi are on its
// new side, failing if a hunk removes one or adds a line between them.
func moveRange(patch string, lo, hi int) (int, bool) {
	old, new, moved := 1, 1, 0
	for raw := range strings.SplitSeq(strings.TrimSuffix(patch, "\n"), "\n") {
		if strings.HasPrefix(raw, "@@") {
			start, next := hunkStarts(raw)
			// Lines before the hunk are unchanged, shifted by those before.
			if moved == 0 && lo < start {
				moved = lo + new - old
			}
			if hi < start {
				return moved, true
			}
			old, new = start, next
			continue
		}
		if raw == "" {
			continue
		}
		switch raw[0] {
		case ' ':
			if old == lo {
				moved = new
			}
			old, new = old+1, new+1
		case '-':
			if old >= lo && old <= hi {
				return 0, false
			}
			old++
		case '+':
			if old > lo && old <= hi {
				return 0, false
			}
			new++
		}
	}
	if moved == 0 {
		moved = lo + new - old
	}
	return moved, true
}

// hunkStarts reads "@@ -a,b +c,d @@" as the first old and new lines the hunk
// covers; a side with no lines starts after the line it names.
func hunkStarts(header string) (old, new int) {
	fields := strings.Fields(header)
	read := func(i int, sign string) int {
		if i >= len(fields) {
			return 1
		}
		first, count, _ := strings.Cut(strings.TrimPrefix(fields[i], sign), ",")
		n, _ := strconv.Atoi(first)
		if count == "0" {
			n++
		}
		return n
	}
	return read(1, "-"), read(2, "+")
}
