package format

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"time"
)

var updatedLine = regexp.MustCompile(`^([ \t]*updated:[ \t]*)(?:"([^"]+)"|'([^']+)'|([^ \t#]+))([ \t]*(?:#.*)?)$`)

type conflictBlock struct {
	end                int
	ours, base, theirs []string
	diff3              bool
}

func cleanConflictLine(line string) string {
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
}

func markerPrefix(line string, marker byte) bool {
	n := 0
	for n < len(line) && line[n] == marker {
		n++
	}
	return n >= 7 && (n == len(line) || line[n] == ' ' || line[n] == '\t')
}
func isStartMarker(line string) bool { return markerPrefix(line, '<') }
func isBaseMarker(line string) bool  { return markerPrefix(line, '|') }
func isEndMarker(line string) bool   { return markerPrefix(line, '>') }
func isDivider(line string) bool {
	trimmed := strings.TrimSpace(line)
	return len(trimmed) >= 7 && strings.Trim(trimmed, "=") == ""
}

func conflictMarkerLine(data []byte) int {
	for index, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if isStartMarker(line) {
			return index + 1
		}
	}
	return 0
}

func parseConflict(lines []string, start int) (conflictBlock, bool) {
	block := conflictBlock{}
	divider, baseAt := -1, -1
	for i := start + 1; i < len(lines); i++ {
		line := cleanConflictLine(lines[i])
		if isStartMarker(line) {
			return conflictBlock{}, false
		}
		if isBaseMarker(line) {
			if baseAt >= 0 || divider >= 0 {
				return conflictBlock{}, false
			}
			baseAt = i
			block.diff3 = true
			continue
		}
		if isDivider(line) {
			if divider >= 0 {
				return conflictBlock{}, false
			}
			divider = i
			continue
		}
		if isEndMarker(line) {
			if divider < 0 {
				return conflictBlock{}, false
			}
			block.end = i + 1
			if baseAt >= 0 {
				block.ours = lines[start+1 : baseAt]
				block.base = lines[baseAt+1 : divider]
			} else {
				block.ours = lines[start+1 : divider]
			}
			block.theirs = lines[divider+1 : i]
			return block, true
		}
	}
	return conflictBlock{}, false
}

type updatedAlternative struct {
	line   string
	stamp  time.Time
	prefix string
	suffix string
	style  int
}

func sideUpdated(lines []string) (updatedAlternative, bool) {
	var candidate updatedAlternative
	for _, raw := range lines {
		line := cleanConflictLine(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		match := updatedLine.FindStringSubmatch(line)
		if match == nil || candidate.line != "" {
			return updatedAlternative{}, false
		}
		value, style := match[2], 2
		if value == "" {
			value, style = match[3], 1
		}
		if value == "" {
			value, style = match[4], 0
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil || parsed.UTC().Format(time.RFC3339) != value {
			return updatedAlternative{}, false
		}
		candidate = updatedAlternative{line: raw, stamp: parsed, prefix: match[1], suffix: match[5], style: style}
	}
	return candidate, candidate.line != ""
}

func sameUpdatedField(a, b updatedAlternative) bool {
	return a.prefix == b.prefix && a.suffix == b.suffix && a.style == b.style
}

func resolveUpdated(block conflictBlock) (string, bool) {
	ours, ok := sideUpdated(block.ours)
	if !ok {
		return "", false
	}
	theirs, ok := sideUpdated(block.theirs)
	if !ok || !sameUpdatedField(ours, theirs) {
		return "", false
	}
	if block.diff3 {
		base, ok := sideUpdated(block.base)
		if !ok || !sameUpdatedField(ours, base) {
			return "", false
		}
	}
	if theirs.stamp.After(ours.stamp) {
		return ensureLineEnding(theirs.line), true
	}
	return ensureLineEnding(ours.line), true
}

func ensureLineEnding(line string) string {
	if strings.HasSuffix(line, "\n") {
		return line
	}
	return line + "\n"
}

type mergedComment struct {
	time time.Time
	text string
}

func commentsOnly(lines []string) ([]mergedComment, bool) {
	var result []mergedComment
	var current strings.Builder
	var currentTime time.Time
	fence := fenceState{}
	started := false
	flush := func() {
		if !started {
			return
		}
		text := strings.Trim(current.String(), "\r\n")
		if text != "" {
			result = append(result, mergedComment{time: currentTime, text: text})
		}
		current.Reset()
	}
	for _, raw := range lines {
		line := cleanConflictLine(raw)
		comment := []string(nil)
		if !fence.consume(line) {
			comment = commentPattern.FindStringSubmatch(line)
		}
		if comment != nil && validTime(comment[1]) {
			flush()
			currentTime, _ = time.Parse(time.RFC3339, comment[1])
			started = true
			current.WriteString(line)
			current.WriteByte('\n')
			continue
		}
		if !started {
			if strings.TrimSpace(line) != "" {
				return nil, false
			}
			continue
		}
		current.WriteString(line)
		if strings.HasSuffix(raw, "\n") {
			current.WriteByte('\n')
		}
	}
	flush()
	return result, len(result) > 0 || strings.TrimSpace(strings.Join(lines, "")) == ""
}

func resolveComments(block conflictBlock) (string, bool) {
	ours, ok := commentsOnly(block.ours)
	if !ok {
		return "", false
	}
	theirs, ok := commentsOnly(block.theirs)
	if !ok {
		return "", false
	}
	if block.diff3 {
		base, ok := commentsOnly(block.base)
		if !ok {
			return "", false
		}
		// Both sides must retain every base comment byte-for-byte: changes
		// or deletions are not the append-only merge we can safely repair.
		for _, original := range base {
			for _, side := range [][]mergedComment{ours, theirs} {
				found := false
				for _, comment := range side {
					if comment.text == original.text {
						found = true
						break
					}
				}
				if !found {
					return "", false
				}
			}
		}
	}
	all := append(ours, theirs...)
	if len(all) == 0 {
		return "", false
	}
	seen := map[string]bool{}
	unique := all[:0]
	for _, comment := range all {
		if seen[comment.text] {
			continue
		}
		seen[comment.text] = true
		unique = append(unique, comment)
	}
	sort.SliceStable(unique, func(i, j int) bool {
		if unique[i].time.Equal(unique[j].time) {
			return unique[i].text < unique[j].text
		}
		return unique[i].time.Before(unique[j].time)
	})
	var text strings.Builder
	for i, comment := range unique {
		if i > 0 {
			text.WriteString("\n\n")
		}
		text.WriteString(comment.text)
	}
	text.WriteByte('\n')
	return text.String(), true
}

// ResolveConflicts repairs only complete updated-only and whole-comment hunks.
// It returns the original hunk unchanged when its contents are not understood.
func ResolveConflicts(data []byte) ([]byte, bool) {
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out strings.Builder
	changed := false
	inFrontmatter := len(lines) > 0 && cleanConflictLine(lines[0]) == "---"
	frontmatterClosed := false
	section := "Description"
	fence := fenceState{}
	for i := 0; i < len(lines); {
		line := cleanConflictLine(lines[i])
		if !isStartMarker(line) {
			out.WriteString(lines[i])
			if inFrontmatter && i > 0 && line == "---" {
				inFrontmatter = false
				frontmatterClosed = true
			} else if !inFrontmatter && frontmatterClosed {
				if !fence.consume(line) && strings.HasPrefix(line, "## ") {
					if name := sectionName(strings.TrimPrefix(line, "## ")); name != "" {
						section = name
					}
				}
			}
			i++
			continue
		}
		block, ok := parseConflict(lines, i)
		if !ok {
			end := i + 1
			for end < len(lines) && !isEndMarker(cleanConflictLine(lines[end])) {
				end++
			}
			if end < len(lines) {
				end++
			}
			for _, original := range lines[i:end] {
				out.WriteString(original)
			}
			i = end
			continue
		}
		resolved, supported := "", false
		if inFrontmatter {
			resolved, supported = resolveUpdated(block)
		} else if frontmatterClosed && section == "Comments" && fence.count == 0 {
			resolved, supported = resolveComments(block)
		}
		if supported {
			out.WriteString(resolved)
			changed = true
			i = block.end
			continue
		}
		for _, original := range lines[i:block.end] {
			out.WriteString(original)
		}
		i = block.end
	}
	result := []byte(out.String())
	if !bytes.Equal(data, result) {
		return result, changed
	}
	return bytes.Clone(data), false
}

// NormalizeLineEndings returns the documented noncanonical repair: LF endings
// and a final newline, without trimming Markdown content.
func NormalizeLineEndings(data []byte) []byte {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
	if !bytes.HasSuffix(data, []byte("\n")) {
		data = append(bytes.Clone(data), '\n')
	}
	return data
}

// NoncanonicalLine reports whether line endings or the final newline need repair.
func NoncanonicalLine(data []byte) int {
	if bytes.Contains(data, []byte("\r")) {
		return 1
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		return bytes.Count(data, []byte("\n")) + 1
	}
	return 0
}
