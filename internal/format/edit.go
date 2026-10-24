package format

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/model"
	"go.yaml.in/yaml/v3"
)

func (d *Document) splice(start, end int, text string) []byte {
	return []byte(strings.Join(d.lines[:start], "") + text + strings.Join(d.lines[end:], ""))
}
func encode(n *yaml.Node) string {
	preferDoubleQuotes(n, false)
	return rawEncode(n)
}
func rawEncode(n *yaml.Node) string {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	_ = e.Encode(n)
	_ = e.Close()
	return strings.TrimSuffix(b.String(), "\n")
}

// preferDoubleQuotes double-quotes each unstyled string that YAML must quote,
// as Prettier does, unless it contains a double quote. YAML would single-quote
// it, and Prettier would rewrite that.
func preferDoubleQuotes(n *yaml.Node, flow bool) {
	flow = flow || n.Style&yaml.FlowStyle != 0
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode, yaml.MappingNode:
		for _, child := range n.Content {
			preferDoubleQuotes(child, flow)
		}
	case yaml.ScalarNode:
		if n.Style != 0 || strings.Contains(n.Value, `"`) {
			return
		}
		probe := &yaml.Node{Kind: yaml.ScalarNode, Tag: n.Tag, Value: n.Value}
		text := rawEncode(probe)
		if flow {
			text = strings.TrimPrefix(rawEncode(&yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle, Content: []*yaml.Node{probe}}), "[")
		}
		if strings.HasPrefix(text, "'") {
			n.Style = yaml.DoubleQuotedStyle
		}
	}
}

// printWidth is Prettier's default. Flow lists that would exceed it are written
// in block style, so bl-written frontmatter is stable under Prettier.
const printWidth = 80

// renderField renders one frontmatter line, or a block when the value is a nonempty
// map or a list too long for one line. A comment follows the key.
func renderField(key, n *yaml.Node, comment, nl string) string {
	text := encode(key) + ":"
	if n.Kind == yaml.SequenceNode && n.Style&yaml.FlowStyle != 0 && utf8.RuneCountInString(text+" "+encode(n)) > printWidth {
		block := *n
		block.Style &^= yaml.FlowStyle
		n = &block
	}
	if (n.Kind == yaml.SequenceNode || n.Kind == yaml.MappingNode) && n.Style&yaml.FlowStyle == 0 && len(n.Content) > 0 {
		if comment != "" {
			text += " " + comment
		}
		for _, line := range strings.Split(encode(n), "\n") {
			text += nl + "  " + line
		}
		return text + nl
	}
	text += " " + encode(n)
	if comment != "" {
		text += " " + comment
	}
	return text + nl
}
func scalar(value string, style yaml.Style) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style & (yaml.SingleQuotedStyle | yaml.DoubleQuotedStyle)}
	return n
}
func asNode(value any) (*yaml.Node, error) {
	var n yaml.Node
	switch v := value.(type) {
	case string:
		n = *scalar(v, 0)
	case []string:
		n.Kind = yaml.SequenceNode
		n.Tag = "!!seq"
		n.Style = yaml.FlowStyle
		for _, s := range v {
			n.Content = append(n.Content, scalar(s, 0))
		}
	default:
		return nil, fmt.Errorf("invalid-value: expected string or []string")
	}
	return &n, nil
}
func optional(key string) bool {
	return !slices.Contains([]string{"id", "title", "status", "created", "updated"}, key)
}
func (d *Document) fieldEdit(key string, value any, unset bool) ([]byte, error) {
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return nil, fmt.Errorf("invalid-value: field name")
	}
	if unset && !optional(key) {
		return nil, fmt.Errorf("missing-required: cannot unset %s", key)
	}
	old, exists := d.fields[key]
	if unset {
		if !exists {
			return d.Bytes(), nil
		}
		return d.splice(old.span.start, old.span.end, ""), nil
	}
	n, err := asNode(value)
	if err != nil {
		return nil, err
	}
	if (n.Kind == yaml.SequenceNode && len(n.Content) == 0) || (n.Kind == yaml.ScalarNode && n.Value == "" && optional(key)) {
		return d.fieldEdit(key, nil, true)
	}
	if exists {
		if old.value.Kind == yaml.ScalarNode && n.Kind == yaml.ScalarNode && old.value.Value == n.Value {
			return d.Bytes(), nil
		}
		if old.value.Kind == n.Kind {
			n.Style = old.value.Style
			if n.Kind == yaml.ScalarNode {
				n = scalar(n.Value, n.Style)
			} else {
				for _, child := range n.Content {
					for _, prev := range old.value.Content {
						if prev.Value == child.Value {
							child.Style = prev.Style
							break
						}
					}
				}
			}
		}
		if (key == "created" || key == "updated") && validTime(n.Value) {
			n.Tag = "!!timestamp"
		}
		comment := old.value.LineComment
		// Retain comments on the changed key itself, too.
		if comment == "" {
			comment = old.key.LineComment
		}
		return d.splice(old.span.start, old.span.end, normalize(renderField(scalar(key, old.key.Style), n, comment, d.nl), d.nl)), nil
	}
	// Insert known keys immediately before the next canonical successor, without
	// moving existing keys or their comments. Unknown keys append at the end.
	at := d.close
	index := slices.Index(keyOrder, key)
	if index >= 0 {
		for _, next := range keyOrder[index+1:] {
			if f, ok := d.fields[next]; ok {
				at = f.span.start
				break
			}
		}
	}
	return d.splice(at, at, normalize(renderField(scalar(key, 0), n, "", d.nl), d.nl)), nil
}
func normalize(text, nl string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", nl)
}
func (d *Document) sectionEdit(name, text string, appendText bool) ([]byte, error) {
	name = sectionName(name)
	if name == "" {
		return nil, fmt.Errorf("invalid-value: unknown section")
	}
	text = strings.Trim(normalize(text, d.nl), "\r\n")
	s, ok := d.sections[name]
	if text == "" && !appendText {
		// Empty replacement removes a section rather than leaving an empty heading.
		switch {
		case !ok:
			return d.Bytes(), nil
		case name == "Description":
			if s.end == len(d.lines) {
				return d.splice(s.start, s.end, ""), nil
			}
			return d.splice(s.start, s.end, d.nl), nil
		case s.end == len(d.lines):
			start := s.heading
			for start > d.close+1 && strings.TrimSpace(d.lines[start-1]) == "" {
				start--
			}
			return d.splice(start, s.end, ""), nil
		default:
			return d.splice(s.heading, s.end, ""), nil
		}
	}
	if ok {
		if appendText {
			at := s.end
			for at > s.start && strings.TrimSpace(d.lines[at-1]) == "" {
				at--
			}
			prefix := ""
			if at > s.start {
				prefix = d.nl
				if !strings.HasSuffix(d.lines[at-1], "\n") {
					prefix += d.nl
				}
			} else {
				prefix = d.nl
			}
			return d.splice(at, at, prefix+text+d.nl), nil
		}
		prefix := d.nl
		suffix := d.nl
		if s.end < len(d.lines) {
			suffix += d.nl
		}
		return d.splice(s.start, s.end, prefix+text+suffix), nil
	}
	index := slices.Index(sectionOrder, name)
	at := len(d.lines)
	for _, next := range sectionOrder[index+1:] {
		if s, ok := d.sections[next]; ok {
			at = s.heading
			break
		}
	}
	prefix := ""
	if at > 0 {
		if !strings.HasSuffix(d.lines[at-1], "\n") {
			prefix += d.nl
		}
		if strings.TrimSpace(d.lines[at-1]) != "" {
			prefix += d.nl
		}
	}
	suffix := d.nl
	if at < len(d.lines) {
		suffix += d.nl
	}
	return d.splice(at, at, prefix+"## "+name+d.nl+d.nl+text+suffix), nil
}

// checkRefValues names a written reference value that isn't an item ID, which
// parsing would otherwise report only by frontmatter line.
func checkRefValues(field string, values []string) error {
	if !slices.Contains([]string{"parent", "duplicate_of", "blocked_by", "related"}, field) {
		return nil
	}
	for _, v := range values {
		if !validRef(parseRef(v)) {
			return fmt.Errorf("invalid-value: %s value %q is not an item ID", field, v)
		}
	}
	return nil
}

// structureChanged reports edits whose text would add, remove, or swallow a
// recognized section (for example an unclosed fence or a pasted heading).
func structureChanged(before, after *Document, target string, removable bool) bool {
	for name := range after.sections {
		if _, ok := before.sections[name]; !ok && name != target {
			return true
		}
	}
	for name := range before.sections {
		if _, ok := after.sections[name]; !ok && !(removable && name == target) {
			return true
		}
	}
	return false
}

// Apply is pure: it returns a newly parsed document and a representation-exact
// inverse. The caller owns atomic disk writes and supplies the clock. A version
// mismatch fails before editing. Restore is also guarded by an exact version.
func (d *Document) Apply(m model.Mutation, now time.Time) (*Document, model.Mutation, error) {
	fail := func(err error) (*Document, model.Mutation, error) { return nil, model.Mutation{}, err }
	if m.ExpectedVersion != "" && m.ExpectedVersion != d.Version() {
		return fail(fmt.Errorf("version-conflict"))
	}
	if m.Ref.ID != "" && m.Ref.ID != d.Item().Ref.ID {
		return fail(fmt.Errorf("item-mismatch"))
	}
	var data []byte
	var err error
	target := ""
	switch m.Op {
	case model.Batch:
		next := d
		for _, change := range m.Changes {
			if change.Op == model.Batch || change.Op == model.Create || change.Op == model.Archive || change.Op == model.Unarchive {
				return fail(fmt.Errorf("unsupported mutation in batch: %s", change.Op))
			}
			change.ExpectedVersion = next.Version()
			next, _, err = next.Apply(change, now)
			if err != nil {
				return fail(err)
			}
		}
		data = next.Bytes()
	case model.Set, model.Unset:
		values, _ := m.Value.([]string)
		if text, ok := m.Value.(string); ok && text != "" {
			values = []string{text}
		}
		if m.Op == model.Set {
			if err := checkRefValues(m.Field, values); err != nil {
				return fail(err)
			}
		}
		data, err = d.fieldEdit(m.Field, m.Value, m.Op == model.Unset)
	case model.Add, model.Remove:
		if text, ok := m.Value.(string); ok && m.Op == model.Add {
			if err := checkRefValues(m.Field, []string{text}); err != nil {
				return fail(err)
			}
		}
		var values []string
		f, ok := d.fields[m.Field]
		if ok {
			if f.value.Kind != yaml.SequenceNode {
				return fail(fmt.Errorf("invalid-value: expected list"))
			}
			for _, n := range f.value.Content {
				values = append(values, n.Value)
			}
		}
		value, ok := m.Value.(string)
		if !ok {
			return fail(fmt.Errorf("invalid-value: expected string"))
		}
		index := slices.Index(values, value)
		if m.Op == model.Add && index < 0 {
			values = append(values, value)
		} else if m.Op == model.Remove && index >= 0 {
			values = slices.Delete(values, index, index+1)
		} else {
			data = d.Bytes()
			break
		}
		// Block lists retain every unchanged entry and any intervening comments.
		if f.value != nil && f.value.Style&yaml.FlowStyle == 0 && len(values) > 0 {
			if m.Op == model.Add {
				at := f.span.end
				indent := "  "
				if len(f.value.Content) > 0 {
					line := clean(d.lines[f.value.Content[0].Line])
					if pos := strings.Index(line, "- "); pos >= 0 {
						indent = line[:pos]
					}
				}
				data = d.splice(at, at, indent+"- "+encode(scalar(value, 0))+d.nl)
			} else {
				n := f.value.Content[index]
				data = d.splice(n.Line, nodeEnd(n)+1, "")
			}
		} else {
			data, err = d.fieldEdit(m.Field, values, false)
		}
	case model.ReplaceSection:
		if sectionName(m.Section) == "Comments" {
			return fail(fmt.Errorf("unsupported mutation: comments are append-only"))
		}
		target = sectionName(m.Section)
		data, err = d.sectionEdit(m.Section, m.Text, false)
	case model.AppendNote:
		target = "Notes"
		data, err = d.sectionEdit("Notes", m.Text, true)
	case model.AppendComment:
		c := m.Comment
		if strings.TrimSpace(c.Author) == "" || strings.TrimSpace(c.Author) != c.Author || strings.ContainsAny(c.Author, "\r\n") {
			return fail(fmt.Errorf("invalid-value: comment author"))
		}
		text := "### " + c.Time.UTC().Truncate(time.Second).Format(time.RFC3339) + " " + c.Author + d.nl + d.nl + c.Body
		target = "Comments"
		data, err = d.sectionEdit("Comments", text, true)
	case model.CheckCriterion, model.UncheckCriterion, model.AddCriterion, model.RemoveCriterion, model.SetCriterion:
		name := sectionName(m.Section)
		if name != "Acceptance criteria" && name != "Definition of done" {
			return fail(fmt.Errorf("invalid-value: expected criteria section"))
		}
		if (m.Op == model.AddCriterion || m.Op == model.SetCriterion) && (strings.TrimSpace(m.Text) == "" || strings.ContainsAny(m.Text, "\r\n")) {
			return fail(fmt.Errorf("invalid-value: criterion must be nonempty and single-line"))
		}
		if m.Op == model.AddCriterion {
			target = name
			text := "- [ ] " + normalize(m.Text, d.nl)
			data, err = d.sectionEdit(name, text, true)
			break
		}
		entries := d.entries(name)
		if m.Index < 1 || m.Index > len(entries) {
			return fail(fmt.Errorf("invalid-value: criterion index"))
		}
		e := entries[m.Index-1]
		if m.Op == model.RemoveCriterion {
			data = d.splice(e.start, e.end, "")
		} else if m.Op == model.SetCriterion {
			line := d.lines[e.start]
			prefix := line[:len(line)-len(strings.TrimLeft(line[5:], " \t"))]
			text := prefix + m.Text + d.nl + strings.Join(d.lines[e.start+1:e.end], "")
			data = d.splice(e.start, e.end, text)
		} else {
			line := d.lines[e.start]
			mark := "x"
			if m.Op == model.UncheckCriterion {
				mark = " "
			}
			text := line[:3] + mark + line[4:]
			for j := e.start + 1; j < e.end; j++ {
				if strings.HasPrefix(strings.TrimSpace(d.lines[j]), "Evidence:") && (m.Op == model.UncheckCriterion || m.Evidence != nil) {
					continue
				}
				text += d.lines[j]
			}
			if m.Op == model.CheckCriterion && m.Evidence != nil {
				if strings.ContainsAny(*m.Evidence, "\r\n") || strings.TrimSpace(*m.Evidence) == "" {
					return fail(fmt.Errorf("invalid-value: evidence must be nonempty and single-line"))
				}
				if !strings.HasSuffix(text, "\n") {
					text += d.nl
				}
				text += "  Evidence: " + *m.Evidence + d.nl
			}
			data = d.splice(e.start, e.end, text)
		}
	case model.Restore:
		if m.ExpectedVersion == "" {
			return fail(fmt.Errorf("version-required: restore"))
		}
		data = bytes.Clone(m.Original)
	default:
		return fail(fmt.Errorf("unsupported mutation: %s", m.Op))
	}
	if err != nil {
		return fail(err)
	}
	next, err := Parse(data)
	if err != nil {
		return fail(err)
	}
	if target != "" && structureChanged(d, next, target, m.Op == model.ReplaceSection) {
		return fail(fmt.Errorf("invalid-value: text must not add, remove, or hide sections; fence code and avoid level-2 section headings"))
	}
	if m.Op == model.AppendComment && len(next.Item().Comments) != len(d.Item().Comments)+1 {
		return fail(fmt.Errorf("invalid-value: comment body must not contain comment headers"))
	}
	data, err = next.fieldEdit("updated", now.UTC().Truncate(time.Second).Format(time.RFC3339), false)
	if err != nil {
		return fail(err)
	}
	next, err = Parse(data)
	if err != nil {
		return fail(err)
	}
	inverse := model.Mutation{Op: model.Restore, Ref: next.Item().Ref, ExpectedVersion: next.Version(), Original: d.Bytes()}
	return next, inverse, nil
}

// Serialize creates a canonical new file. Unknown fields follow the known keys
// in lexical order. Parse(...).Bytes() is the lossless serialization of existing
// documents; Serialize(Item) is intentionally canonical, not lossless.
func Serialize(item model.Item) ([]byte, error) {
	values := map[string]any{}
	for k, v := range item.Fields {
		if !slices.Contains(keyOrder, k) {
			values[k] = v
		}
	}
	values["id"] = item.Ref.ID
	values["title"] = item.Title
	values["status"] = item.Status
	for k, v := range map[string]string{"type": item.Type, "priority": string(item.Priority), "milestone": item.Milestone, "rank": item.Rank, "due": item.Due} {
		if v != "" {
			values[k] = v
		}
	}
	for k, v := range map[string][]string{"assignees": item.Assignees, "labels": item.Labels, "refs": item.Refs, "files": item.Files} {
		if len(v) > 0 {
			values[k] = v
		}
	}
	refString := func(r model.Ref) string {
		if r.Source != "" {
			return r.Source + ":" + r.ID
		}
		return r.ID
	}
	if item.Parent != nil {
		values["parent"] = refString(*item.Parent)
	}
	if item.DuplicateOf != nil {
		values["duplicate_of"] = refString(*item.DuplicateOf)
	}
	for k, refs := range map[string][]model.Ref{"blocked_by": item.BlockedBy, "related": item.Related} {
		if len(refs) > 0 {
			var list []string
			for _, r := range refs {
				list = append(list, refString(r))
			}
			values[k] = list
		}
	}
	values["created"] = item.Created.UTC().Truncate(time.Second).Format(time.RFC3339)
	values["updated"] = item.Updated.UTC().Truncate(time.Second).Format(time.RFC3339)
	keys := slices.Clone(keyOrder)
	var extra []string
	for k := range values {
		if !slices.Contains(keyOrder, k) {
			extra = append(extra, k)
		}
	}
	slices.Sort(extra)
	keys = append(keys, extra...)
	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range keys {
		v, ok := values[k]
		if !ok {
			continue
		}
		n, err := asNode(v)
		if err != nil {
			n = &yaml.Node{}
			if err := n.Encode(v); err != nil {
				return nil, err
			}
		}
		if n.Kind == yaml.SequenceNode {
			n.Style = yaml.FlowStyle
		}
		if k == "created" || k == "updated" {
			n.Tag = "!!timestamp"
		}
		b.WriteString(renderField(scalar(k, 0), n, "", "\n"))
	}
	b.WriteString("---\n")
	if description := strings.Trim(normalize(item.Description, "\n"), "\r\n"); description != "" {
		b.WriteString("\n" + description + "\n")
	}
	sections := map[string]string{"Plan": item.Plan, "Notes": item.Notes, "Summary": item.Summary}
	for name, criteria := range map[string][]model.Criterion{"Acceptance criteria": item.Criteria, "Definition of done": item.DoD} {
		var lines []string
		for _, c := range criteria {
			mark := " "
			if c.Checked {
				mark = "x"
			}
			line := "- [" + mark + "] " + c.Text
			if c.Evidence != "" {
				line += "\n  Evidence: " + c.Evidence
			}
			lines = append(lines, line)
		}
		sections[name] = strings.Join(lines, "\n")
	}
	var comments []string
	for _, c := range item.Comments {
		comments = append(comments, "### "+c.Time.UTC().Truncate(time.Second).Format(time.RFC3339)+" "+c.Author+"\n\n"+c.Body)
	}
	sections["Comments"] = strings.Join(comments, "\n\n")
	written := 1 // Description is always present in a parsed document.
	for _, name := range sectionOrder {
		if text := strings.Trim(normalize(sections[name], "\n"), "\r\n"); text != "" {
			b.WriteString("\n## " + name + "\n\n" + text + "\n")
			written++
		}
	}
	data := []byte(b.String())
	d, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if len(d.sections) != written || len(d.Item().Comments) != len(item.Comments) {
		return nil, fmt.Errorf("invalid-value: text must not add, remove, or hide sections or comments; fence code and avoid level-2 section headings")
	}
	return data, nil
}

// MarshalConfig encodes project config with two-space indentation and
// Prettier's quoting, so a bl-written backlog.yaml is stable under Prettier.
func MarshalConfig(v any) ([]byte, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	preferDoubleQuotes(&n, false)
	return []byte(rawEncode(&n) + "\n"), nil
}
