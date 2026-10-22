package format

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"go.yaml.in/yaml/v3"
)

func (d *Document) splice(start, end int, text string) []byte {
	return []byte(strings.Join(d.lines[:start], "") + text + strings.Join(d.lines[end:], ""))
}
func encode(n *yaml.Node) string {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	_ = e.Encode(n)
	_ = e.Close()
	return strings.TrimSuffix(b.String(), "\n")
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
		text := encode(scalar(key, old.key.Style)) + ":"
		if n.Kind == yaml.SequenceNode && n.Style&yaml.FlowStyle == 0 {
			if comment != "" {
				text += " " + comment
			}
			text += d.nl
			for _, child := range n.Content {
				text += "  - " + encode(child) + d.nl
			}
		} else {
			text += " " + encode(n)
			if comment != "" {
				text += " " + comment
			}
			text += d.nl
		}
		return d.splice(old.span.start, old.span.end, normalize(text, d.nl)), nil
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
	return d.splice(at, at, normalize(encode(scalar(key, 0))+": "+encode(n), d.nl)+d.nl), nil
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
	if s, ok := d.sections[name]; ok {
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
		if name == "Description" {
			prefix = d.nl
		}
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
	return d.splice(at, at, prefix+"## "+name+d.nl+d.nl+text+d.nl+d.nl), nil
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
		data, err = d.fieldEdit(m.Field, m.Value, m.Op == model.Unset)
	case model.Add, model.Remove:
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
		data, err = d.sectionEdit(m.Section, m.Text, false)
	case model.AppendNote:
		data, err = d.sectionEdit("Notes", m.Text, true)
	case model.AppendComment:
		c := m.Comment
		if strings.TrimSpace(c.Author) == "" || strings.TrimSpace(c.Author) != c.Author || strings.ContainsAny(c.Author, "\r\n") {
			return fail(fmt.Errorf("invalid-value: comment author"))
		}
		text := "### " + c.Time.UTC().Truncate(time.Second).Format(time.RFC3339) + " " + c.Author + d.nl + d.nl + c.Body
		data, err = d.sectionEdit("Comments", text, true)
	case model.CheckCriterion, model.UncheckCriterion, model.AddCriterion, model.RemoveCriterion:
		name := sectionName(m.Section)
		if name != "Acceptance criteria" && name != "Definition of done" {
			return fail(fmt.Errorf("invalid-value: expected criteria section"))
		}
		if m.Op == model.AddCriterion {
			if strings.TrimSpace(m.Text) == "" {
				return fail(fmt.Errorf("invalid-value: empty criterion"))
			}
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
		if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
			n.Style = yaml.FlowStyle
		}
		if k == "created" || k == "updated" {
			n.Tag = "!!timestamp"
		}
		b.WriteString(encode(scalar(k, 0)) + ": " + encode(n) + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.Trim(normalize(item.Description, "\n"), "\r\n"))
	b.WriteString("\n")
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
	for _, name := range sectionOrder {
		if text := strings.Trim(normalize(sections[name], "\n"), "\r\n"); text != "" {
			b.WriteString("\n## " + name + "\n\n" + text + "\n")
		}
	}
	data := []byte(b.String())
	if _, err := Parse(data); err != nil {
		return nil, err
	}
	return data, nil
}
