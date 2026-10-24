package backlogmd

import (
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// referenceRewriter updates text for the move from Backlog.md files to native
// items: mentions of renumbered IDs, relative Markdown links, and refs paths.
// The native project is assumed to replace the source project in place, so a
// link to any other file keeps naming the same file. Fenced code is untouched.
type referenceRewriter struct {
	project    string
	items      map[string]string // canonical source file → output path
	partitions []string
	renamed    map[string]string // refKey of a renumbered ID → native ID, "" if ambiguous
	mentions   *regexp.Regexp
	reported   map[UnresolvedReference]bool
	report     *Report
}

var (
	inlineLink          = regexp.MustCompile(`\]\(\s*(<[^<>\n]*>|[^\s()<>]+)`)
	referenceDefinition = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:\s*(<[^<>\n]*>|\S+)`)
)

func newReferenceRewriter(source sourceConfig, tasks []taskInput, refs map[string]string, report *Report) *referenceRewriter {
	r := &referenceRewriter{project: source.projectRoot, items: map[string]string{}, renamed: map[string]string{}, reported: map[UnresolvedReference]bool{}, report: report}
	for _, partition := range []string{"tasks", "completed", "drafts", "milestones", "archive"} {
		r.partitions = append(r.partitions, canonical(filepath.Join(source.root, partition)))
	}
	var alternatives []string
	holders := map[string]int{}
	for _, task := range tasks {
		if task.legacyID != "" {
			holders[refKey(task.legacyID)]++
		}
	}
	for _, task := range tasks {
		r.items[canonical(task.path)] = task.outputPath
		if task.legacyID == "" {
			continue
		}
		key := refKey(task.legacyID)
		// Prose can't say which holder it meant, unlike a dependency.
		if r.renamed[key] = refs[key]; holders[key] > 1 {
			r.renamed[key] = ""
		}
		alternatives = append(alternatives, regexp.QuoteMeta(strings.TrimSpace(task.legacyID)), regexp.QuoteMeta(key))
	}
	if len(alternatives) > 0 {
		// Longest first, so DRAFT-12 isn't read as DRAFT-1.
		slices.SortFunc(alternatives, func(a, b string) int { return len(b) - len(a) })
		r.mentions = regexp.MustCompile(`(?i)\b(?:` + strings.Join(slices.Compact(alternatives), "|") + `)\b`)
	}
	return r
}

func (r *referenceRewriter) rewrite(task *taskInput) error {
	refs, err := mergedLists(task.doc.fields, "references", "documentation", "refs")
	if err != nil {
		return err
	}
	for i, ref := range refs {
		if !external(ref) {
			if output, ok := r.items[canonical(filepath.Join(r.project, filepath.FromSlash(ref)))]; ok {
				refs[i] = output
			}
		}
	}
	task.refs = refs
	body := &task.doc.body
	for key, text := range body.sections {
		body.sections[key] = r.text(task, text)
	}
	body.comments = r.text(task, body.comments)
	for i := range body.unknown {
		body.unknown[i].content = r.text(task, body.unknown[i].content)
	}
	return nil
}

func (r *referenceRewriter) text(task *taskInput, text string) string {
	if text == "" {
		return text
	}
	var out strings.Builder
	fence := markdownFence{}
	for _, line := range strings.SplitAfter(text, "\n") {
		if fence.consume(strings.TrimRight(line, "\r\n")) {
			out.WriteString(line)
			continue
		}
		var spans [][]int
		for _, match := range inlineLink.FindAllStringSubmatchIndex(line, -1) {
			spans = append(spans, match[2:4])
		}
		if match := referenceDefinition.FindStringSubmatchIndex(line); match != nil {
			spans = append(spans, match[2:4])
		}
		slices.SortFunc(spans, func(a, b []int) int { return a[0] - b[0] })
		position := 0
		for _, span := range spans {
			if span[0] < position {
				continue
			}
			out.WriteString(r.mention(task, line[position:span[0]]))
			out.WriteString(r.link(task, line[span[0]:span[1]]))
			position = span[1]
		}
		out.WriteString(r.mention(task, line[position:]))
	}
	return out.String()
}

// mention replaces renumbered IDs that stand alone, not inside a longer ID,
// slug, or path such as DRAFT-1.2, draft-1-notes, or drafts/draft-1.md.
func (r *referenceRewriter) mention(task *taskInput, text string) string {
	if r.mentions == nil {
		return text
	}
	var out strings.Builder
	position := 0
	for _, match := range r.mentions.FindAllStringIndex(text, -1) {
		start, end := match[0], match[1]
		if start > 0 && strings.ContainsRune("-_/", rune(text[start-1])) || end+1 < len(text) && strings.ContainsRune("-_./", rune(text[end])) && wordByte(text[end+1]) || inURL(text, start) {
			continue
		}
		value := text[start:end]
		target := r.renamed[refKey(value)]
		if target == "" {
			r.unresolved(task, value, "several items had this ID; left as written")
			continue
		}
		out.WriteString(text[position:start])
		out.WriteString(target)
		position = end
	}
	out.WriteString(text[position:])
	return out.String()
}

func (r *referenceRewriter) link(task *taskInput, destination string) string {
	raw := destination
	angle := strings.HasPrefix(raw, "<")
	if angle {
		raw = raw[1 : len(raw)-1]
	}
	path, fragment, hasFragment := strings.Cut(raw, "#")
	if path == "" || external(path) {
		return destination
	}
	unescaped, err := url.PathUnescape(path)
	if err != nil {
		unescaped = path
	}
	// Resolve symlinks only to compare paths; the rewritten link keeps the
	// names the author wrote below the file's real directory.
	target := filepath.Join(canonical(filepath.Dir(task.file)), filepath.FromSlash(unescaped))
	project := canonical(r.project)
	if output, ok := r.items[canonical(target)]; ok {
		target = filepath.Join(project, filepath.FromSlash(output))
	} else if slices.ContainsFunc(r.partitions, func(partition string) bool { return within(partition, canonical(target)) }) {
		r.unresolved(task, destination, "links into a Backlog.md folder the native project doesn't keep; left as written")
		return destination
	}
	from := filepath.Dir(filepath.Join(project, filepath.FromSlash(task.outputPath)))
	relative, err := filepath.Rel(from, target)
	if err != nil {
		return destination
	}
	relative = filepath.ToSlash(relative)
	if strings.Contains(path, "%") {
		relative = (&url.URL{Path: relative}).EscapedPath()
	}
	if hasFragment {
		relative += "#" + fragment
	}
	if angle || strings.ContainsAny(relative, " \t") {
		relative = "<" + relative + ">"
	}
	return relative
}

func (r *referenceRewriter) unresolved(task *taskInput, value, reason string) {
	entry := UnresolvedReference{SourcePath: task.sourcePath, Value: value, Reason: reason}
	if !r.reported[entry] {
		r.reported[entry] = true
		r.report.UnresolvedReferences = append(r.report.UnresolvedReferences, entry)
	}
}

// canonical resolves symbolic links in the longest existing prefix of path, so
// a link read through a followed symlink compares with paths in the project.
func canonical(path string) string {
	rest := ""
	for dir := path; ; {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// external reports a URL or an absolute path, which don't resolve against the
// source file.
func external(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || filepath.IsAbs(path) {
		return true
	}
	parsed, err := url.Parse(path)
	return err == nil && parsed.Scheme != ""
}

// inURL reports whether position falls in a whitespace-delimited token that
// holds a URL, such as a query or fragment naming an ID.
func inURL(text string, position int) bool {
	start := strings.LastIndexAny(text[:position], " \t\n") + 1
	end := strings.IndexAny(text[position:], " \t\n")
	if end < 0 {
		end = len(text) - position
	}
	return strings.Contains(text[start:position+end], "://")
}

func wordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
