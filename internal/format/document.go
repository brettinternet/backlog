// Package format implements the native v1 codec without reserializing untouched text.
package format

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/model"
	"go.yaml.in/yaml/v3"
)

var keyOrder = []string{"id", "title", "status", "type", "priority", "assignees", "labels", "milestone", "parent", "blocked_by", "related", "duplicate_of", "rank", "due", "refs", "files", "created", "updated"}
var sectionOrder = []string{"Acceptance criteria", "Definition of done", "Plan", "Notes", "Summary", "Comments"}
var idPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+(\.[0-9]+)*$`)
var taskPattern = regexp.MustCompile(`^[-*] \[([ xX])\] (.*)$`)
var commentPattern = regexp.MustCompile(`^### (\S+) (\S.*)$`)

type span struct{ start, end int }
type field struct {
	key, value *yaml.Node
	span       span
}
type section struct{ heading, start, end int }
type entry struct {
	span
	text    string
	checked bool
}

// Document owns a copy of the input. Bytes and Item return detached views.
type Document struct {
	raw      []byte
	lines    []string
	nl       string
	close    int
	fields   map[string]field
	sections map[string]section
	values   map[string]any
}

func version(b []byte) string       { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func (d *Document) Version() string { return version(d.raw) }
func (d *Document) Bytes() []byte   { return bytes.Clone(d.raw) }
func clean(s string) string         { return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r") }
func sectionName(s string) string {
	s = strings.TrimSpace(s)
	for _, name := range sectionOrder {
		if strings.EqualFold(s, name) {
			return name
		}
	}
	if strings.EqualFold(s, "Description") {
		return "Description"
	}
	return ""
}

// Parse rejects invalid native structure. Project status and graph validation
// are deliberately left to the caller. No partial document is returned.
func Parse(data []byte) (*Document, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid-value: expected UTF-8")
	}
	d := &Document{raw: bytes.Clone(data), nl: "\n", fields: map[string]field{}, sections: map[string]section{}, values: map[string]any{}}
	d.lines = strings.SplitAfter(string(data), "\n")
	if d.lines[len(d.lines)-1] == "" {
		d.lines = d.lines[:len(d.lines)-1]
	}
	if len(d.lines) < 3 || clean(d.lines[0]) != "---" {
		return nil, fmt.Errorf("invalid-frontmatter: expected opening ---")
	}
	if strings.HasSuffix(d.lines[0], "\r\n") {
		d.nl = "\r\n"
	}
	for i := 1; i < len(d.lines); i++ {
		if clean(d.lines[i]) == "---" {
			d.close = i
			break
		}
	}
	if d.close == 0 {
		return nil, fmt.Errorf("invalid-frontmatter: missing closing ---")
	}
	frontmatter := strings.Join(d.lines[1:d.close], "")
	if strings.ContainsAny(strings.ReplaceAll(frontmatter, "\r\n", "\n"), "\r\u0085\u2028\u2029") {
		return nil, fmt.Errorf("invalid-frontmatter: line endings must be LF or CRLF")
	}
	var root yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(frontmatter))
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("invalid-frontmatter: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("invalid-frontmatter: expected one YAML document")
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode || root.Content[0].Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("invalid-frontmatter: expected mapping")
	}
	mapping := root.Content[0]
	if err := mapping.Decode(&d.values); err != nil {
		return nil, fmt.Errorf("invalid-frontmatter: %w", err)
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		k, v := mapping.Content[i], mapping.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" {
			return nil, fmt.Errorf("invalid-frontmatter: expected literal string keys")
		}
		end := d.close
		if i+2 < len(mapping.Content) {
			end = mapping.Content[i+2].Line
		}
		// Include multiline flow delimiters, but not separating blank lines or
		// standalone YAML comments belonging to neither value.
		for end > k.Line+1 {
			line := clean(d.lines[end-1])
			if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "#") {
				break
			}
			end--
		}
		d.fields[k.Value] = field{k, v, span{k.Line, end}}
	}
	for _, key := range []string{"id", "title", "status", "created", "updated"} {
		if _, ok := d.fields[key]; !ok {
			return nil, fmt.Errorf("missing-required: %s", key)
		}
	}
	for _, key := range keyOrder {
		f, ok := d.fields[key]
		if !ok {
			continue
		}
		switch key {
		case "assignees", "labels", "blocked_by", "related", "refs", "files":
			if f.value.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("invalid-value: %s must be a list", key)
			}
			for _, n := range f.value.Content {
				if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
					return nil, fmt.Errorf("invalid-value: %s must contain strings", key)
				}
			}
		default:
			if f.value.Kind != yaml.ScalarNode || (f.value.Tag != "!!str" && f.value.Tag != "!!timestamp") {
				return nil, fmt.Errorf("invalid-value: %s must be a string", key)
			}
		}
	}
	for _, key := range []string{"id", "title", "status"} {
		v := d.fields[key].value.Value
		if strings.TrimSpace(v) == "" || strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("invalid-value: %s", key)
		}
	}
	if !idPattern.MatchString(d.fields["id"].value.Value) {
		return nil, fmt.Errorf("invalid-value: id")
	}
	for _, key := range []string{"created", "updated"} {
		if !validTime(d.fields[key].value.Value) {
			return nil, fmt.Errorf("invalid-value: %s", key)
		}
	}
	if f, ok := d.fields["priority"]; ok && f.value.Value != "urgent" && f.value.Value != "high" && f.value.Value != "medium" && f.value.Value != "low" {
		return nil, fmt.Errorf("invalid-value: priority")
	}
	if f, ok := d.fields["due"]; ok {
		if _, err := time.Parse("2006-01-02", f.value.Value); err != nil {
			return nil, fmt.Errorf("invalid-value: due")
		}
	}
	for _, key := range []string{"parent", "duplicate_of", "blocked_by", "related"} {
		if f, ok := d.fields[key]; ok {
			nodes := []*yaml.Node{f.value}
			if f.value.Kind == yaml.SequenceNode {
				nodes = f.value.Content
			}
			for _, n := range nodes {
				r := parseRef(n.Value)
				if !idPattern.MatchString(r.ID) {
					return nil, fmt.Errorf("invalid-value: %s reference", key)
				}
			}
		}
	}
	current := "Description"
	start := d.close + 1
	heading := -1
	fence := fenceState{}
	for i := start; i < len(d.lines); i++ {
		line := clean(d.lines[i])
		if fence.consume(line) {
			continue
		}
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		name := sectionName(strings.TrimPrefix(line, "## "))
		if name == "" || name == "Description" {
			continue
		}
		d.sections[current] = section{heading, start, i}
		if _, ok := d.sections[name]; ok || name == current {
			return nil, fmt.Errorf("duplicate-section: %s at line %d", name, i+1)
		}
		current = name
		heading = i
		start = i + 1
	}
	d.sections[current] = section{heading, start, len(d.lines)}
	return d, nil
}
func nodeEnd(n *yaml.Node) int {
	end := n.Line
	if n.Kind == yaml.ScalarNode && (n.Style == yaml.LiteralStyle || n.Style == yaml.FoldedStyle) {
		end += strings.Count(n.Value, "\n")
		if !strings.HasSuffix(n.Value, "\n") {
			end++
		}
	}
	for _, child := range n.Content {
		if e := nodeEnd(child); e > end {
			end = e
		}
	}
	return end
}
func validTime(s string) bool {
	t, e := time.Parse(time.RFC3339, s)
	return e == nil && t.Format("2006-01-02T15:04:05Z") == s
}

type fenceState struct {
	char  byte
	count int
}

func (f *fenceState) consume(line string) bool {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 || len(s) < 3 {
		return f.count > 0
	}
	c := s[0]
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	if f.count > 0 {
		if c == f.char && n >= f.count && strings.TrimSpace(s[n:]) == "" {
			f.count = 0
		}
		return true
	}
	if (c == '`' || c == '~') && n >= 3 && (c != '`' || !strings.Contains(s[n:], "`")) {
		f.char = c
		f.count = n
		return true
	}
	return false
}
func parseRef(s string) model.Ref {
	source, id, ok := strings.Cut(s, ":")
	if !ok {
		return model.Ref{ID: s}
	}
	return model.Ref{Source: source, ID: id}
}
func (d *Document) content(name string) string {
	s, ok := d.sections[name]
	if !ok {
		return ""
	}
	start, end := s.start, s.end
	for start < end && strings.TrimSpace(d.lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(d.lines[end-1]) == "" {
		end--
	}
	return strings.TrimSuffix(normalize(strings.Join(d.lines[start:end], ""), "\n"), "\n")
}
func (d *Document) entries(name string) []entry {
	s, ok := d.sections[name]
	if !ok {
		return nil
	}
	var result []entry
	fence := fenceState{}
	for i := s.start; i < s.end; i++ {
		line := clean(d.lines[i])
		if fence.consume(line) {
			continue
		}
		m := taskPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		end := i + 1
		for end < s.end && (strings.HasPrefix(d.lines[end], "  ") || strings.HasPrefix(d.lines[end], "\t")) {
			end++
		}
		text := m[2]
		for j := i + 1; j < end; j++ {
			text += "\n" + clean(d.lines[j])
		}
		result = append(result, entry{span{i, end}, text, m[1] != " "})
		i = end - 1
	}
	return result
}
func (d *Document) Item() model.Item {
	str := func(k string) string { return d.fields[k].valueString() }
	list := func(k string) []string {
		var out []string
		if f, ok := d.fields[k]; ok {
			for _, n := range f.value.Content {
				out = append(out, n.Value)
			}
		}
		return out
	}
	item := model.Item{Ref: model.Ref{ID: str("id")}, Title: str("title"), Type: str("type"), Status: str("status"), Priority: model.Priority(str("priority")), Rank: str("rank"), Assignees: list("assignees"), Labels: list("labels"), Milestone: str("milestone"), Due: str("due"), Refs: list("refs"), Files: list("files"), Description: d.content("Description"), Plan: d.content("Plan"), Notes: d.content("Notes"), Summary: d.content("Summary"), Version: d.Version(), Fields: map[string]any{}}
	item.Created, _ = time.Parse(time.RFC3339, str("created"))
	item.Updated, _ = time.Parse(time.RFC3339, str("updated"))
	for _, key := range []string{"parent", "blocked_by", "related", "duplicate_of"} {
		values := list(key)
		if key == "parent" || key == "duplicate_of" {
			values = nil
			if str(key) != "" {
				values = []string{str(key)}
			}
		}
		for _, v := range values {
			r := parseRef(v)
			item.Edges = append(item.Edges, model.Edge{Kind: model.EdgeKind(key), Target: r})
			switch key {
			case "parent":
				item.Parent = &r
			case "blocked_by":
				item.BlockedBy = append(item.BlockedBy, r)
			case "related":
				item.Related = append(item.Related, r)
			case "duplicate_of":
				item.DuplicateOf = &r
			}
		}
	}
	for _, e := range d.entries("Acceptance criteria") {
		item.Criteria = append(item.Criteria, model.Criterion{Text: e.text, Checked: e.checked})
	}
	for _, e := range d.entries("Definition of done") {
		item.DoD = append(item.DoD, model.Criterion{Text: e.text, Checked: e.checked})
	}
	if s, ok := d.sections["Comments"]; ok {
		fence := fenceState{}
		var current *model.Comment
		for i := s.start; i < s.end; i++ {
			line := clean(d.lines[i])
			m := []string(nil)
			if !fence.consume(line) {
				m = commentPattern.FindStringSubmatch(line)
			}
			if m != nil && validTime(m[1]) {
				if current != nil {
					current.Body = strings.Trim(current.Body, "\r\n")
					item.Comments = append(item.Comments, *current)
				}
				t, _ := time.Parse(time.RFC3339, m[1])
				current = &model.Comment{Time: t, Author: m[2]}
			} else if current != nil {
				current.Body += line + "\n"
			}
		}
		if current != nil {
			current.Body = strings.Trim(current.Body, "\r\n")
			item.Comments = append(item.Comments, *current)
		}
	}
	// Decode again so callers cannot mutate the document's raw-field view.
	_ = yaml.Unmarshal([]byte(strings.Join(d.lines[1:d.close], "")), &item.Fields)
	return item
}
func (f field) valueString() string {
	if f.value == nil {
		return ""
	}
	return f.value.Value
}
