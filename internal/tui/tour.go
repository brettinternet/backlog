package tui

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/brettinternet/backlog/internal/tour"
	"github.com/charmbracelet/x/ansi"
)

// A tour an agent wrote for a change (design.md D43) shows on its Diff tab:
// each step's narrative and cited criterion sit above the step's line, and a
// step on a line outside every hunk brings that line in with context. A tour
// written for an earlier head is carried forward to the current one, its
// steps on lines changed since marked outdated; one whose head is no longer an
// ancestor shows only that it is stale.

// tourContext is how many lines show on each side of a step outside every
// hunk.
const tourContext = 3

// tourView is the tour the Diff draws, read on the detail worker.
type tourView struct {
	steps    []tour.Step
	criteria []model.Criterion
	// stale says why the steps are hidden: the tour is for another head.
	stale string
	// carried names the heads a tour was carried between; outdated holds,
	// by step, the lines of a step changed since, as of the tour's ref.
	ref, carried string
	outdated     map[int][]string
	// unread says why a step's file couldn't be read, by path.
	unread map[string]string
}

// movesCache keeps the last tour's moves and the files read as of its ref.
type movesCache struct {
	key   string
	moves changes.Moves
	ok    bool
	files map[string][]byte
}

// loadTour reads the change's tour, the item's criteria if a step cites
// one, and the files of steps outside every hunk, on the worker.
func (worker *detailWorker) loadTour(ctx context.Context, request detailRequest, c changes.Change, d *changes.Detail) *tourView {
	if worker.tour == nil {
		return nil
	}
	stored, ok, err := worker.tour(request.row.Ref, c.ID)
	if err != nil {
		return &tourView{stale: "tour unavailable: " + err.Error()}
	}
	if !ok {
		return nil
	}
	t := stored.Tour
	view := &tourView{steps: t.Steps, unread: map[string]string{}}
	if t.Ref != c.Head {
		if err := worker.carry(ctx, c, t, view); err != nil {
			return &tourView{stale: "Tour is stale: written for " + short(t.Ref) + ", the change is now at " + cmp.Or(short(c.Head), "no head") + "; " + err.Error() + "."}
		}
	}
	for _, f := range d.Files {
		if f.Status == changes.FileUncommitted && slices.ContainsFunc(view.current(), func(s tour.Step) bool { return s.File == f.Path }) {
			return &tourView{stale: "Tour is stale: " + textutil.Line(f.Path) + " has uncommitted changes since " + short(t.Ref) + "."}
		}
	}
	if slices.ContainsFunc(t.Steps, func(s tour.Step) bool { return s.Criterion > 0 }) {
		if item, err := worker.item(ctx, request.row); err == nil {
			view.criteria = item.Criteria
		}
	}
	d.Files = worker.tourFiles(ctx, c, d.Files, view)
	return view
}

// carry maps t's steps from its ref to c's head through the diff between
// them, cached by both, keeping each step whose lines are unchanged at its
// moved line and holding the lines of the rest as of the ref.
func (worker *detailWorker) carry(ctx context.Context, c changes.Change, t tour.Tour, view *tourView) error {
	key := c.Dir() + "\x00" + t.Ref + "\x00" + c.Head
	if worker.recentMoves.key != key {
		moves, ok, err := worker.moves(ctx, c, t.Ref)
		if err != nil {
			return err
		}
		worker.recentMoves = movesCache{key: key, moves: moves, ok: ok, files: map[string][]byte{}}
	}
	cache := worker.recentMoves
	if !cache.ok {
		return errors.New("the head doesn't descend from it in this checkout")
	}
	view.ref, view.steps, view.outdated = t.Ref, slices.Clone(t.Steps), map[int][]string{}
	for i, s := range view.steps {
		old := s.Side == tour.SideOld
		path, line, ok := cache.moves.Range(s.File, old, s.Line, s.End())
		view.steps[i].File = path
		if ok {
			if s.Selection != nil {
				sel := *s.Selection
				sel.Start.Line, sel.End.Line = line, s.End()+line-s.Line
				view.steps[i].Selection = &sel
			}
			view.steps[i].Line = line
			continue
		}
		view.outdated[i] = nil
		fileKey := strconv.FormatBool(old) + "\x00" + s.File
		content, read := cache.files[fileKey]
		if !read {
			if data, ok, err := worker.movedFile(cache.moves, ctx, s.File, old); err == nil && ok {
				content = data
			}
			cache.files[fileKey] = content
		}
		if content == nil {
			continue
		}
		lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
		for n := s.Line; n <= min(s.End(), len(lines)); n++ {
			view.outdated[i] = append(view.outdated[i], expandTabs(textutil.Sanitize(lines[n-1])))
		}
	}
	view.carried = "Tour carried forward from " + short(t.Ref) + " to " + short(c.Head) + ": " + strconv.Itoa(len(view.outdated)) + " of " + strconv.Itoa(len(view.steps)) + " steps outdated."
	return nil
}

// current are the tour's steps whose lines show at the head.
func (view *tourView) current() []tour.Step {
	var out []tour.Step
	for i, s := range view.steps {
		if _, outdated := view.outdated[i]; !outdated {
			out = append(out, s)
		}
	}
	return out
}

// item reads the row's item through the detail cache.
func (worker *detailWorker) item(ctx context.Context, row engine.Row) (model.Item, error) {
	key := itemKey{Ref: row.Ref, Version: row.Version}
	if item, ok := worker.cache.item(key); ok {
		return item, nil
	}
	if worker.read == nil {
		return model.Item{}, errors.New("detail reading is unavailable")
	}
	item, err := worker.read(ctx, row.Ref)
	if err == nil {
		worker.cache.putItem(key, item)
	}
	return item, err
}

// tourFiles adds context hunks around steps outside every hunk, and the files
// of steps the change doesn't touch, in path order. The files' patches are
// copies; the change's detail is left as it was read.
func (worker *detailWorker) tourFiles(ctx context.Context, c changes.Change, files []changes.File, view *tourView) []changes.File {
	byFile := map[string][]tour.Step{}
	for _, s := range view.current() {
		byFile[s.File] = append(byFile[s.File], s)
	}
	out := slices.Clone(files)
	// An outdated step sits under its file's header, which shows even when
	// none of its lines do.
	for i := range view.outdated {
		s := view.steps[i]
		if _, listed := byFile[s.File]; !listed && !slices.ContainsFunc(out, func(f changes.File) bool { return stepIn(s, f) }) {
			out = append(out, changes.File{Path: s.File, Status: changes.FileUnchanged})
		}
	}
	for i, f := range out {
		steps := append(slices.Clone(byFile[f.Path]), oldSteps(byFile[f.From])...)
		delete(byFile, f.Path)
		if f.From != "" {
			// Its old path's other steps stay, to say why they don't show.
			if rest := slices.DeleteFunc(byFile[f.From], func(s tour.Step) bool { return s.Side == tour.SideOld }); len(rest) > 0 {
				byFile[f.From] = rest
			} else {
				delete(byFile, f.From)
			}
		}
		if f.Patch == "" || f.Status == changes.FileBinary || len(f.Patch) > bodyLimit {
			continue
		}
		if outside := outsideSteps(parsePatch(f.Patch), steps); len(outside) > 0 {
			content, err := worker.fileAt(ctx, c, f.Path)
			if err != nil {
				view.unread[f.Path] = textutil.Line(err.Error())
				continue
			}
			out[i].Patch = withContext(f.Patch, outside, content)
		}
	}
	for path, steps := range byFile {
		f := changes.File{Path: path, Status: changes.FileUnchanged}
		content, err := worker.fileAt(ctx, c, path)
		if err != nil {
			view.unread[path] = textutil.Line(err.Error())
		} else {
			f.Patch = withContext("", outsideSteps(nil, steps), content)
		}
		out = append(out, f)
	}
	slices.SortStableFunc(out, func(a, b changes.File) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// stepIn says whether s is on f: its path, or a renamed file's old path on
// the old side.
func stepIn(s tour.Step, f changes.File) bool {
	return s.File == f.Path || s.File == f.From && f.From != "" && s.Side == tour.SideOld
}

// oldSteps are a renamed file's steps on its old path, on the old side.
func oldSteps(steps []tour.Step) []tour.Step {
	return slices.DeleteFunc(slices.Clone(steps), func(s tour.Step) bool { return s.Side != tour.SideOld })
}

// stepAnchor is where a step's first line is in a file's diff.
func stepAnchor(path string, s tour.Step) lineAnchor {
	a := lineAnchor{file: path, line: s.Line}
	if s.Side == tour.SideOld {
		a.side = sideOld
	}
	return a
}

// outsideSteps are the lines of steps' ranges no hunk line shows, each as a
// one-line step on its side.
func outsideSteps(hunks []diffHunk, steps []tour.Step) []tour.Step {
	shown := [2]map[int]bool{{}, {}}
	for _, h := range hunks {
		for _, l := range h.lines {
			shown[sideNew][l.new], shown[sideOld][l.old] = true, true
		}
	}
	var out []tour.Step
	for _, s := range steps {
		side := stepAnchor("", s).side
		for n := s.Line; n <= s.End(); n++ {
			if !shown[side][n] {
				out = append(out, tour.Step{Line: n, Side: s.Side})
			}
		}
	}
	return out
}

// hunkSpan is a hunk's text and the new lines it covers, [lo, hi], with the
// old and new line numbers after it; key orders it, a hunk that adds no lines
// sitting between line lo-1 and lo.
type hunkSpan struct {
	text             string
	key, lo, hi      int
	oldNext, newNext int
}

// withContext adds a context hunk to patch for each run of lines around
// steps, read from content, the file's new side. A line outside every hunk is
// the same on both sides, its numbers apart by what the hunks before it add
// and remove.
func withContext(patch string, steps []tour.Step, content []byte) string {
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	var spans []hunkSpan
	after := map[int]bool{} // new lines a hunk adding none follows
	for _, text := range splitHunks(patch) {
		a, b, c, d := hunkRange(text)
		h := hunkSpan{text: text, key: 2 * c, lo: c, hi: c + d - 1, oldNext: nextLine(a, b), newNext: nextLine(c, d)}
		if d == 0 {
			h.key, h.lo, after[c] = 2*c+1, c+1, true
		}
		spans = append(spans, h)
	}
	real := slices.Clone(spans)
	inside := func(n int) bool {
		return slices.ContainsFunc(real, func(h hunkSpan) bool { return n >= h.lo && n <= h.hi })
	}
	// shift is new minus old numbering past the hunks before line, on the
	// old side or the new.
	shift := func(line int, old bool) int {
		n := 0
		for _, h := range real {
			next := h.newNext
			if old {
				next = h.oldNext
			}
			if next <= line {
				n = h.newNext - h.oldNext
			}
		}
		return n
	}
	wanted := map[int]bool{}
	for _, s := range steps {
		line := s.Line
		if s.Side == tour.SideOld {
			line += shift(s.Line, true)
		}
		for n := line - tourContext; n <= line+tourContext; n++ {
			if n >= 1 && n <= len(lines) && !inside(n) {
				wanted[n] = true
			}
		}
	}
	numbers := slices.Sorted(maps.Keys(wanted))
	for i := 0; i < len(numbers); {
		j := i + 1
		for j < len(numbers) && numbers[j] == numbers[j-1]+1 && !after[numbers[j-1]] {
			j++
		}
		lo, hi := numbers[i], numbers[j-1]
		n := strconv.Itoa(hi - lo + 1)
		var b strings.Builder
		b.WriteString("@@ -" + strconv.Itoa(lo-shift(lo, false)) + "," + n + " +" + strconv.Itoa(lo) + "," + n + " @@\n")
		for k := lo; k <= hi; k++ {
			b.WriteString(" " + lines[k-1] + "\n")
		}
		spans = append(spans, hunkSpan{text: b.String(), key: 2 * lo})
		i = j
	}
	slices.SortStableFunc(spans, func(a, b hunkSpan) int { return a.key - b.key })
	var b strings.Builder
	for _, h := range spans {
		b.WriteString(h.text)
	}
	return b.String()
}

// nextLine is the line after a hunk side starting at start with count
// lines; a side with none names the line before it.
func nextLine(start, count int) int {
	if count == 0 {
		return start + 1
	}
	return start + count
}

// splitHunks cuts a patch into its hunks, each from its @@ header.
func splitHunks(patch string) []string {
	var out []string
	for line := range strings.SplitAfterSeq(patch, "\n") {
		if strings.HasPrefix(line, "@@") || len(out) == 0 {
			out = append(out, "")
		}
		out[len(out)-1] += line
	}
	return slices.DeleteFunc(out, func(h string) bool { return h == "" })
}

// hunkRange reads "@@ -a,b +c,d @@", where a missing count is 1.
func hunkRange(header string) (old, oldCount, start, count int) {
	fields := strings.Fields(header)
	read := func(i int, sign string) (int, int) {
		if i >= len(fields) {
			return 1, 1
		}
		first, n, ok := strings.Cut(strings.TrimPrefix(fields[i], sign), ",")
		a, _ := strconv.Atoi(first)
		b := 1
		if ok {
			b, _ = strconv.Atoi(n)
		}
		return a, b
	}
	old, oldCount = read(1, "-")
	start, count = read(2, "+")
	return old, oldCount, start, count
}

// callout draws a step above its line: its number, title, and cited
// criterion, then its narrative, in width cells; note says why its line
// doesn't show.
func callout(t theme, view *tourView, index int, width int, note string, render func(string, int, string) (string, error)) []string {
	s := view.steps[index]
	head := t.accent.render("Step " + strconv.Itoa(index+1) + "/" + strconv.Itoa(len(view.steps)))
	if title := textutil.Line(s.Title); title != "" {
		head += " " + t.bold.render(title)
	}
	if s.Criterion > 0 {
		head += t.muted.render(" · ") + t.yellow.render("AC "+strconv.Itoa(s.Criterion))
		if s.Criterion <= len(view.criteria) {
			c := view.criteria[s.Criterion-1]
			mark := "[ ] "
			if c.Checked {
				mark = "[x] "
			}
			head += " " + mark + textutil.Line(c.Text)
		}
	}
	bar := t.accent.render("│") + " "
	out := []string{ansi.Truncate(head, width, "…")}
	for _, line := range markdownLines(s.Description, max(1, width-2), t, render) {
		out = append(out, bar+line)
	}
	if note != "" {
		out = append(out, bar+t.yellow.render(ansi.Truncate(note, max(1, width-2), "…")))
	}
	if lines, outdated := view.outdated[index]; outdated {
		ref := short(view.ref)
		if lines == nil {
			out = append(out, bar+t.yellow.render(ansi.Truncate("Outdated: changed since "+ref+", whose lines can't be read.", max(1, width-2), "…")))
		} else {
			out = append(out, bar+t.yellow.render(ansi.Truncate("Outdated: changed since "+ref+", where it read:", max(1, width-2), "…")))
		}
		digits := len(strconv.Itoa(s.Line + len(lines)))
		for n, line := range lines {
			number := strconv.Itoa(s.Line + n)
			out = append(out, bar+ansi.Truncate(t.muted.render(blank(digits-len(number))+number+" ")+line, max(1, width-2), "…"))
		}
	}
	return out
}

// stepSpan is where a step shows on the Diff: its callout's first line and
// the line of its range's last row. An outdated step's line isn't at the head.
type stepSpan struct {
	callout, last int
	outdated      bool
}

// lineMarks are the lines of one file's tour ranges, by side.
type lineMarks [2]map[int]bool

// mark is the gutter cell after a line number: a bar when a tour step
// covers the line.
func (m lineMarks) mark(t theme, side diffSide, line int) string {
	if line > 0 && m[side][line] {
		return t.accent.render("▎")
	}
	return " "
}

// lineRuns names lines as runs, such as "lines 4–6, 9", or "line 4".
func lineRuns(lines []int) string {
	var runs []string
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j] == lines[j-1]+1 {
			j++
		}
		run := strconv.Itoa(lines[i])
		if j-1 > i {
			run += "–" + strconv.Itoa(lines[j-1])
		}
		runs = append(runs, run)
		i = j
	}
	if len(lines) == 1 {
		return "line " + runs[0]
	}
	return "lines " + strings.Join(runs, ", ")
}

// short is a commit's first seven characters.
func short(sha string) string { return sha[:min(7, len(sha))] }
