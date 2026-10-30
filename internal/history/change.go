package history

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/textutil"
)

// summaryWidth is how long one field's part of a summary may be before the
// summary abbreviates it and the detail carries it in full.
const summaryWidth = 60

// diffLimit bounds the lines one field contributes to an entry's detail.
const diffLimit = 200

// changeEntry summarizes an item.updated event on one line, and puts what that
// line abbreviates in its detail: one indented block per field, under its name
// when there are several, with each changed line marked "+ " or "- " and each
// checked or unchecked criterion "[x] " or "[ ] ".
func changeEntry(changes map[string]hooks.Change) (summary, detail string) {
	if c, ok := changes["archived"]; ok && c.To == false {
		return "unarchived", ""
	}
	_, status := changes["status"]
	keys := make([]string, 0, len(changes))
	for k := range changes {
		// fields and updated change with every edit, so they say nothing, and
		// category follows status.
		if k != "fields" && k != "updated" && (k != "category" || !status) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	var blocks []string
	for _, k := range keys {
		part, lines := fieldChange(k, changes[k])
		parts = append(parts, part)
		if len(lines) > diffLimit {
			lines = append(lines[:diffLimit], fmt.Sprintf("… %d more lines", len(lines)-diffLimit))
		}
		if len(lines) > 0 {
			blocks = append(blocks, k+"\n  "+strings.Join(lines, "\n  "))
		}
	}
	// A lone field is already named by the summary.
	if len(keys) == 1 && len(blocks) == 1 {
		_, blocks[0], _ = strings.Cut(blocks[0], "\n")
	}
	return cmp.Or(strings.Join(parts, "; "), "updated"), strings.Join(blocks, "\n")
}

// fieldChange is one field's part of a summary, and the detail lines it needs
// when the summary can't hold the change.
func fieldChange(k string, c hooks.Change) (string, []string) {
	if from, to, ok := checklists(c); ok {
		return k + " " + progress(from) + " → " + progress(to), checklistDiff(from, to)
	}
	if from, to, ok := lists(c); ok {
		var parts, lines []string
		for _, v := range to {
			if !slices.Contains(from, v) {
				parts, lines = append(parts, "+"+v), append(lines, "+ "+v)
			}
		}
		for _, v := range from {
			if !slices.Contains(to, v) {
				parts, lines = append(parts, "-"+v), append(lines, "- "+v)
			}
		}
		if part := strings.Join(parts, " "); len(part) <= summaryWidth {
			return strings.TrimSpace(k + " " + cmp.Or(part, "reordered")), nil
		}
		added := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(line, "- ") })
		if added < 0 {
			added = len(lines)
		}
		return strings.TrimSpace(fmt.Sprintf("%s %s %s", k, count("+", added), count("-", len(lines)-added))), lines
	}
	from, to := textutil.Sanitize(display(c.From)), textutil.Sanitize(display(c.To))
	if len(from)+len(to) <= summaryWidth && !strings.ContainsAny(from+to, "\n") {
		return fmt.Sprintf("%s %s → %s", k, cmp.Or(from, "∅"), cmp.Or(to, "∅")), nil
	}
	// Text that appears or goes whole needs no markers.
	switch {
	case strings.TrimSpace(from) == "":
		return k + " added", splitLines(to)
	case strings.TrimSpace(to) == "":
		return k + " cleared", splitLines(from)
	}
	return k + " changed", lineDiff(from, to)
}

type check struct {
	text    string
	checked bool
}

// checklists reads a change to criteria or definition of done, lists of
// {text, checked}.
func checklists(c hooks.Change) (from, to []check, ok bool) {
	read := func(v any) ([]check, bool) {
		list, isList := v.([]any)
		if v != nil && !isList {
			return nil, false
		}
		out := make([]check, 0, len(list))
		for _, item := range list {
			m, isMap := item.(map[string]any)
			text, hasText := m["text"].(string)
			if !isMap || !hasText {
				return nil, false
			}
			checked, _ := m["checked"].(bool)
			out = append(out, check{textutil.Line(text), checked})
		}
		return out, true
	}
	from, okFrom := read(c.From)
	to, okTo := read(c.To)
	return from, to, okFrom && okTo && len(from)+len(to) > 0
}

// count is n with its sign, or nothing when n is zero.
func count(sign string, n int) string {
	if n == 0 {
		return ""
	}
	return sign + strconv.Itoa(n)
}

func progress(list []check) string {
	if len(list) == 0 {
		return "∅"
	}
	done := 0
	for _, c := range list {
		if c.checked {
			done++
		}
	}
	return strconv.Itoa(done) + "/" + strconv.Itoa(len(list))
}

// checklistDiff matches criteria by text: a new text is added, a missing one
// removed, and a kept one whose box changed is listed with its new box.
func checklistDiff(from, to []check) []string {
	box := func(c check) string {
		if c.checked {
			return "[x] " + c.text
		}
		return "[ ] " + c.text
	}
	index := func(list []check, text string) int {
		return slices.IndexFunc(list, func(c check) bool { return c.text == text })
	}
	var lines []string
	for _, c := range to {
		switch i := index(from, c.text); {
		case i < 0:
			lines = append(lines, "+ "+box(c))
		case from[i].checked != c.checked:
			lines = append(lines, box(c))
		}
	}
	for _, c := range from {
		if index(to, c.text) < 0 {
			lines = append(lines, "- "+box(c))
		}
	}
	return lines
}

// lists reads a change between lists of plain values or references.
func lists(c hooks.Change) (from, to []string, ok bool) {
	read := func(v any) ([]string, bool) {
		list, isList := v.([]any)
		if v != nil && !isList {
			return nil, false
		}
		out := make([]string, 0, len(list))
		for _, item := range list {
			out = append(out, textutil.Line(display(item)))
		}
		return out, true
	}
	from, okFrom := read(c.From)
	to, okTo := read(c.To)
	return from, to, okFrom && okTo
}

// lineDiff lists the lines removed from and added to a text, with "…" where
// it skips unchanged lines. A rewrite too large to compare cheaply lists every
// removed line, then every added one.
func lineDiff(from, to string) []string {
	a, b := splitLines(from), splitLines(to)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	a, b = a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	var lines []string
	if prefix > 0 {
		lines = append(lines, "…")
	}
	if len(a)*len(b) > 1<<16 {
		for _, line := range a {
			lines = append(lines, "- "+line)
		}
		for _, line := range b {
			lines = append(lines, "+ "+line)
		}
	} else {
		lines = append(lines, lcsDiff(a, b)...)
	}
	if suffix > 0 {
		lines = append(lines, "…")
	}
	return lines
}

// lcsDiff marks the lines outside a longest common subsequence of a and b.
func lcsDiff(a, b []string) []string {
	// length[i][j] is the LCS length of a[i:] and b[j:].
	length := make([][]int, len(a)+1)
	for i := range length {
		length[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				length[i][j] = length[i+1][j+1] + 1
			} else {
				length[i][j] = max(length[i+1][j], length[i][j+1])
			}
		}
	}
	var lines []string
	skipped := false
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			i, j, skipped = i+1, j+1, true
			continue
		case j == len(b) || i < len(a) && length[i+1][j] >= length[i][j+1]:
			if skipped && len(lines) > 0 {
				lines = append(lines, "…")
			}
			lines, i = append(lines, "- "+a[i]), i+1
		default:
			if skipped && len(lines) > 0 {
				lines = append(lines, "…")
			}
			lines, j = append(lines, "+ "+b[j]), j+1
		}
		skipped = false
	}
	return lines
}

func splitLines(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// display renders a decoded event value: lists as comma-separated items, refs
// as their IDs, and readiness reasons as "code: refs".
func display(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s := display(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		if code, ok := v["code"].(string); ok {
			if s := display(cmp.Or(v["refs"], v["values"])); s != "" {
				return code + ": " + s
			}
			return code
		}
		if id, ok := v["id"].(string); ok {
			return id
		}
		keys := slices.Sorted(maps.Keys(v))
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+display(v[k]))
		}
		return strings.Join(parts, " ")
	}
	return fmt.Sprint(v)
}
