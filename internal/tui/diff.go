package tui

import (
	"context"
	"hash/fnv"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// The Diff tab draws a change's patches: unified below 120 columns, split at
// 120 or wider, highlighted with chroma and with changed words marked. Each
// file renders on the detail worker, cached by head, file, width, and theme.

const (
	splitDiffWidth = 120
	// maxWordTokens bounds the word diff of a changed line pair.
	maxWordTokens = 200
	tabWidth      = 4
)

// diffPage draws every file of a change's diff, each from the render cache
// when its head, patch, width, and theme are unchanged.
func (worker *detailWorker) diffPage(ctx context.Context, c changes.Change, d changes.Detail, request detailRequest) rendered {
	t, width := request.theme, max(1, request.width)
	var page rendered
	if len(d.Files) == 0 {
		page.lines = append(page.lines, t.muted.render("No changed files."))
	}
	for i, f := range d.Files {
		if ctx.Err() != nil {
			return page
		}
		if i > 0 {
			page.lines = append(page.lines, "")
		}
		page.files = append(page.files, len(page.lines))
		page.entries = append(page.entries, len(page.lines))
		page.rows = append(page.rows, diffRow{file: f.Path})
		page.lines = append(page.lines, fileLine(t, f, width))
		large := len(f.Patch) > bodyLimit
		key := renderKey{Version: c.Head, Width: width, Theme: t.key(), Tab: tabChangeDiff, Expanded: large && request.expanded, Dependency: f.Path + "\x00" + patchDigest(f.Patch)}
		body, ok := worker.diffs.render(key)
		if !ok {
			body = fileDiff(f, t, width, key.Expanded)
			worker.diffs.putRender(key, body)
		}
		for _, h := range body.hunks {
			page.hunks = append(page.hunks, len(page.lines)+h)
		}
		for i := range body.lines {
			page.entries = append(page.entries, len(page.lines)+i)
		}
		page.rows = append(page.rows, body.rows...)
		page.lines = append(page.lines, body.lines...)
		page.truncated = page.truncated || large && !request.expanded
	}
	if d.Reason != "" {
		page.lines = append(page.lines, "", t.yellow.render(ansi.Truncate(textutil.Line(d.Reason), width, "…")))
	}
	return page
}

// diffRow is what a Diff line shows: a file, and the old and new line
// numbers on it, or 0 for a side it doesn't show. A file or hunk header
// shows neither, a unified line one side or both for context, and a split
// row a line on each half.
type diffRow struct {
	file     string
	old, new int
}

type diffSide byte

const (
	sideNew diffSide = iota
	sideOld
)

// lineAnchor is a line on one side of a file in a change's diff at head; line
// 0 is the file itself.
type lineAnchor struct {
	head, file string
	side       diffSide
	line       int
}

// anchors resolves r at head, new side first: one anchor per line it shows,
// or its file.
func (r diffRow) anchors(head string) []lineAnchor {
	var out []lineAnchor
	if r.new > 0 {
		out = append(out, lineAnchor{head: head, file: r.file, side: sideNew, line: r.new})
	}
	if r.old > 0 {
		out = append(out, lineAnchor{head: head, file: r.file, side: sideOld, line: r.old})
	}
	if len(out) == 0 && r.file != "" {
		out = append(out, lineAnchor{head: head, file: r.file})
	}
	return out
}

// shows is whether r shows a's line, or is a's file's first row for line 0.
func (r diffRow) shows(a lineAnchor) bool {
	switch {
	case r.file != a.file:
		return false
	case a.line == 0:
		return r.old == 0 && r.new == 0
	case a.side == sideOld:
		return r.old == a.line
	}
	return r.new == a.line
}

func patchDigest(patch string) string {
	h := fnv.New64a()
	h.Write([]byte(patch))
	return strconv.FormatUint(h.Sum64(), 36)
}

type diffLine struct {
	kind     byte // ' ', '-', '+', or '\\' for "No newline at end of file"
	old, new int
	text     string
}

type diffHunk struct {
	header string
	lines  []diffLine
}

// parsePatch reads a file's hunks, numbering each line on its side.
func parsePatch(patch string) []diffHunk {
	var hunks []diffHunk
	old, new := 0, 0
	for raw := range strings.SplitSeq(strings.TrimSuffix(patch, "\n"), "\n") {
		if strings.HasPrefix(raw, "@@") {
			old, new = hunkStart(raw)
			hunks = append(hunks, diffHunk{header: raw})
			continue
		}
		if len(hunks) == 0 || raw == "" {
			continue
		}
		line := diffLine{kind: raw[0], text: raw[1:]}
		switch line.kind {
		case ' ':
			line.old, line.new = old, new
			old++
			new++
		case '-':
			line.old = old
			old++
		case '+':
			line.new = new
			new++
		case '\\':
		default:
			continue
		}
		hunks[len(hunks)-1].lines = append(hunks[len(hunks)-1].lines, line)
	}
	return hunks
}

// hunkStart reads the first old and new line numbers of "@@ -a,b +c,d @@".
func hunkStart(header string) (int, int) {
	fields := strings.Fields(header)
	start := func(i int, sign string) int {
		if i >= len(fields) {
			return 1
		}
		text, _, _ := strings.Cut(strings.TrimPrefix(fields[i], sign), ",")
		n, _ := strconv.Atoi(text)
		return n
	}
	return start(1, "-"), start(2, "+")
}

// fileDiff renders one file's hunks; hunks holds each header's line. A patch
// over the body limit waits for m, up to the 128 KiB a patch is cut to.
func fileDiff(f changes.File, t theme, width int, expanded bool) rendered {
	var out rendered
	note := func(p paint, text string) {
		out.lines = append(out.lines, p.render(ansi.Truncate(text, width, "…")))
		out.rows = append(out.rows, diffRow{file: f.Path})
	}
	switch {
	case f.Patch == "" && f.Cut != "":
		note(t.yellow, f.Cut)
		return out
	case len(f.Patch) > bodyLimit && !expanded:
		note(t.muted, strconv.Itoa(len(f.Patch)>>10)+" KiB diff")
		return out
	}
	hunks := parsePatch(f.Patch)
	digits := 1
	for _, h := range hunks {
		if n := len(h.lines); n > 0 {
			last := h.lines[n-1]
			digits = max(digits, len(strconv.Itoa(max(last.old, last.new)+n)))
		}
	}
	lexer := lexers.Match(f.Path)
	if lexer != nil {
		lexer = chroma.Coalesce(lexer)
	}
	split := width >= splitDiffWidth
	for _, h := range hunks {
		out.hunks = append(out.hunks, len(out.lines))
		note(t.cyan, textutil.Line(h.header))
		lines, rows := hunkLines(h, lexer, t, width, digits, split)
		out.lines = append(out.lines, lines...)
		for _, r := range rows {
			r.file = f.Path
			out.rows = append(out.rows, r)
		}
	}
	if f.Cut != "" {
		note(t.yellow, f.Cut)
	}
	for _, line := range out.lines {
		out.size += len(line) + 1
	}
	// Each row's path is shared; count its two numbers.
	out.size += 16 * len(out.rows)
	return out
}

// codeLine is one side of a diff line ready to paint.
type codeLine struct {
	line    diffLine
	spans   []codeSpan
	changed []bool // bytes of text a word diff marks, or nil
}

type codeSpan struct {
	text  string
	paint paint
}

// hunkLines highlights each side of a hunk as one text, so a construct that
// spans lines colors correctly, then lays the lines out, with each line's
// old and new line numbers.
func hunkLines(h diffHunk, lexer chroma.Lexer, t theme, width, digits int, split bool) ([]string, []diffRow) {
	lines := make([]codeLine, len(h.lines))
	var oldSide, newSide []int
	for i, l := range h.lines {
		l.text = expandTabs(textutil.Sanitize(l.text))
		lines[i].line = l
		switch l.kind {
		case ' ':
			oldSide, newSide = append(oldSide, i), append(newSide, i)
		case '-':
			oldSide = append(oldSide, i)
		case '+':
			newSide = append(newSide, i)
		}
	}
	for _, side := range [][]int{oldSide, newSide} {
		texts := make([]string, len(side))
		for j, i := range side {
			texts[j] = lines[i].line.text
		}
		for j, spans := range highlight(lexer, texts, t) {
			// A context line is on both sides; either highlight serves.
			lines[side[j]].spans = spans
		}
	}
	for i, l := range lines {
		if l.line.kind == '\\' {
			lines[i].spans = []codeSpan{{text: "\\" + l.line.text, paint: t.muted}}
		}
	}
	// Pair each run of removed lines with the added run after it.
	type block struct{ removed, added []int }
	var rows []any // int for a lone line, block for a change
	for i := 0; i < len(lines); {
		if k := lines[i].line.kind; k != '-' && k != '+' {
			rows = append(rows, i)
			i++
			continue
		}
		var b block
		for ; i < len(lines) && lines[i].line.kind == '-'; i++ {
			b.removed = append(b.removed, i)
		}
		for ; i < len(lines) && lines[i].line.kind == '+'; i++ {
			b.added = append(b.added, i)
		}
		for j := range min(len(b.removed), len(b.added)) {
			lines[b.removed[j]].changed, lines[b.added[j]].changed = wordChanges(lines[b.removed[j]].line.text, lines[b.added[j]].line.text)
		}
		rows = append(rows, b)
	}
	var out []string
	var numbers []diffRow
	if !split {
		code := max(1, width-2*digits-3)
		unified := func(l codeLine) {
			out = append(out, unifiedLine(t, l, digits, code))
			numbers = append(numbers, diffRow{old: l.line.old, new: l.line.new})
		}
		for _, row := range rows {
			indices, ok := row.(block)
			if !ok {
				unified(lines[row.(int)])
				continue
			}
			for _, i := range append(indices.removed, indices.added...) {
				unified(lines[i])
			}
		}
		return out, numbers
	}
	left := (width - 1) / 2
	right := width - 1 - left
	divider := t.faint.render("│")
	for _, row := range rows {
		b, ok := row.(block)
		if !ok {
			l := lines[row.(int)]
			if i := row.(int); l.line.kind == '\\' {
				// It notes the line before it, on that line's side.
				if i > 0 && lines[i-1].line.kind == '+' {
					out = append(out, blank(left)+divider+sideLine(t, l, digits, right, true))
				} else {
					out = append(out, sideLine(t, l, digits, left, false)+divider+blank(right))
				}
				numbers = append(numbers, diffRow{})
				continue
			}
			out = append(out, sideLine(t, l, digits, left, false)+divider+sideLine(t, l, digits, right, true))
			numbers = append(numbers, diffRow{old: l.line.old, new: l.line.new})
			continue
		}
		for j := range max(len(b.removed), len(b.added)) {
			text, number := blank(left), diffRow{}
			if j < len(b.removed) {
				text = sideLine(t, lines[b.removed[j]], digits, left, false)
				number.old = lines[b.removed[j]].line.old
			}
			text += divider
			if j < len(b.added) {
				text += sideLine(t, lines[b.added[j]], digits, right, true)
				number.new = lines[b.added[j]].line.new
			} else {
				text += blank(right)
			}
			out = append(out, text)
			numbers = append(numbers, number)
		}
	}
	return out, numbers
}

// unifiedLine is old and new line numbers, the sign, then the code.
func unifiedLine(t theme, l codeLine, digits, code int) string {
	number := func(n int) string {
		if n == 0 {
			return blank(digits)
		}
		s := strconv.Itoa(n)
		return blank(digits-len(s)) + s
	}
	gutter := t.faint.render(number(l.line.old) + " " + number(l.line.new) + " ")
	return gutter + paintCode(t, l, code+1)
}

// sideLine is one side of a split row: its line number, sign, and code.
func sideLine(t theme, l codeLine, digits, width int, newSide bool) string {
	n := l.line.old
	if newSide {
		n = l.line.new
	}
	number := blank(digits)
	if n > 0 {
		s := strconv.Itoa(n)
		number = blank(digits-len(s)) + s
	}
	return t.faint.render(number+" ") + paintCode(t, l, max(1, width-digits-1))
}

// paintCode draws the sign and code in width cells: the line's tint, each
// token's color, and its changed words marked.
func paintCode(t theme, l codeLine, width int) string {
	var tint string
	var word paint
	sign := " "
	switch l.line.kind {
	case '+':
		tint, word, sign = t.added, t.addedWord, t.green.render("+")
	case '-':
		tint, word, sign = t.removed, t.removedWord, t.red.render("-")
	case '\\':
		sign = ""
	}
	var b strings.Builder
	b.WriteString(tint)
	b.WriteString(sign)
	used := ansi.StringWidth(ansi.Strip(sign))
	offset, marked := 0, false
	cut := false
	for _, s := range l.spans {
		var run strings.Builder
		flush := func() {
			if run.Len() > 0 {
				b.WriteString(s.paint.render(run.String()))
				run.Reset()
			}
		}
		for _, r := range s.text {
			size := utf8.RuneLen(r)
			on := l.changed != nil && offset < len(l.changed) && l.changed[offset]
			offset += max(1, size)
			if cut {
				continue
			}
			w := ansi.StringWidth(string(r))
			if used+w > width {
				cut = true
				continue
			}
			if on != marked {
				flush()
				if on {
					b.WriteString(word.on)
				} else {
					b.WriteString(word.off + tint)
				}
				marked = on
			}
			run.WriteRune(r)
			used += w
		}
		flush()
	}
	if marked {
		b.WriteString(word.off + tint)
	}
	if cut && used >= width {
		// Make room for the ellipsis that says the line goes on.
		line := ansi.Truncate(b.String(), width-1, "")
		b.Reset()
		b.WriteString(line)
		used = ansi.StringWidth(line)
	}
	if cut {
		b.WriteString(t.faint.render("…"))
		used++
	}
	if tint != "" {
		b.WriteString(blank(max(0, width-used)) + resetBg)
	} else if used < width {
		b.WriteString(blank(width - used))
	}
	return b.String()
}

// highlight colors lines as one text with lexer, returning each line's spans;
// with no lexer, each line is one plain span.
func highlight(lexer chroma.Lexer, lines []string, t theme) [][]codeSpan {
	out := make([][]codeSpan, len(lines))
	if lexer == nil || !t.color || t.high {
		for i, line := range lines {
			out[i] = []codeSpan{{text: line}}
		}
		return out
	}
	tokens, err := lexer.Tokenise(nil, strings.Join(lines, "\n")+"\n")
	if err != nil {
		for i, line := range lines {
			out[i] = []codeSpan{{text: line}}
		}
		return out
	}
	i := 0
	for token := tokens(); token != chroma.EOF && i < len(lines); token = tokens() {
		p := tokenPaint(t, token.Type)
		for part, rest := range splitLines(token.Value) {
			if part != "" && i < len(lines) {
				out[i] = append(out[i], codeSpan{text: part, paint: p})
			}
			if rest {
				i++
			}
		}
	}
	for j := range out {
		// The lexer may drop text it can't place; fall back to the plain line.
		joined := ""
		for _, s := range out[j] {
			joined += s.text
		}
		if joined != lines[j] {
			out[j] = []codeSpan{{text: lines[j]}}
		}
	}
	return out
}

// splitLines yields each piece of value between newlines, and whether a
// newline follows it.
func splitLines(value string) func(func(string, bool) bool) {
	return func(yield func(string, bool) bool) {
		for {
			before, after, found := strings.Cut(value, "\n")
			if !yield(before, found) || !found {
				return
			}
			value = after
		}
	}
}

func tokenPaint(t theme, kind chroma.TokenType) paint {
	switch {
	case kind.InCategory(chroma.Comment):
		return t.muted
	case kind.InCategory(chroma.Keyword):
		return t.magenta
	case kind.InSubCategory(chroma.LiteralString):
		return t.cyan
	case kind.InSubCategory(chroma.LiteralNumber):
		return t.yellow
	case kind == chroma.NameFunction || kind == chroma.NameClass || kind == chroma.NameBuiltin:
		return t.blue
	}
	return paint{}
}

// expandTabs replaces tabs with spaces to the next tab stop.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	column := 0
	for _, r := range s {
		if r == '\t' {
			n := tabWidth - column%tabWidth
			b.WriteString(blank(n))
			column += n
			continue
		}
		b.WriteRune(r)
		column += ansi.StringWidth(string(r))
	}
	return b.String()
}

// wordChanges marks the bytes of a and b outside their longest common word
// sequence, or returns nil when the lines share too little for marks to help.
func wordChanges(a, b string) ([]bool, []bool) {
	ta, tb := words(a), words(b)
	if len(ta) == 0 || len(tb) == 0 || len(ta) > maxWordTokens || len(tb) > maxWordTokens {
		return nil, nil
	}
	// lcs[i][j] is the common length of ta[i:] and tb[j:].
	lcs := make([][]int, len(ta)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(tb)+1)
	}
	for i := len(ta) - 1; i >= 0; i-- {
		for j := len(tb) - 1; j >= 0; j-- {
			if a[ta[i][0]:ta[i][1]] == b[tb[j][0]:tb[j][1]] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	ca, cb := make([]bool, len(a)), make([]bool, len(b))
	same := 0
	i, j := 0, 0
	for i < len(ta) && j < len(tb) {
		switch {
		case a[ta[i][0]:ta[i][1]] == b[tb[j][0]:tb[j][1]]:
			same += ta[i][1] - ta[i][0]
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			mark(ca, ta[i])
			i++
		default:
			mark(cb, tb[j])
			j++
		}
	}
	for ; i < len(ta); i++ {
		mark(ca, ta[i])
	}
	for ; j < len(tb); j++ {
		mark(cb, tb[j])
	}
	// Mostly rewritten lines read better whole than speckled.
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" || 2*same < max(len(a), len(b)) {
		return nil, nil
	}
	return ca, cb
}

func mark(flags []bool, token [2]int) {
	for k := token[0]; k < token[1]; k++ {
		flags[k] = true
	}
}

// words splits s into runs of letters and digits, runs of spaces, and single
// other runes, as byte ranges.
func words(s string) [][2]int {
	var out [][2]int
	class := func(r rune) int {
		switch {
		case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			return 1
		case unicode.IsSpace(r):
			return 2
		}
		return 0
	}
	start, last := 0, -1
	for i, r := range s {
		c := class(r)
		if i > 0 && (c != last || c == 0) {
			out = append(out, [2]int{start, i})
			start = i
		}
		last = c
	}
	if len(s) > 0 {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}
