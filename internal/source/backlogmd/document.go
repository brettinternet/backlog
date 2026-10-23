package backlogmd

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
	"go.yaml.in/yaml/v3"
)

type fieldSpan struct {
	key, value *yaml.Node
	start, end int
}
type bodySpan struct {
	heading, start, end int
}
type document struct {
	raw      []byte
	lines    []string
	nl       string
	close    int
	parsed   sourceDocument
	fields   map[string]fieldSpan
	sections map[string]bodySpan
}

var backlogFieldOrder = []string{"id", "title", "status", "assignee", "created_date", "updated_date", "due_date", "labels", "milestone", "dependencies", "references", "documentation", "modified_files", "parent_task_id", "priority", "type", "project", "ordinal"}
var backlogSectionOrder = []string{"description", "criteria", "dod", "plan", "notes", "summary", "comments"}
var checklistLine = regexp.MustCompile(`^([ \t]*[-*+] \[)([ xX])(\][ \t]+)(.*)$`)
var generatedIndex = regexp.MustCompile(`^#[0-9]+[ \t]+`)

func parseDocument(data []byte) (*document, error) {
	parsed, err := parseSourceDocument(data)
	if err != nil {
		return nil, err
	}
	d := &document{raw: bytes.Clone(data), lines: strings.SplitAfter(string(data), "\n"), nl: "\n", parsed: parsed, fields: map[string]fieldSpan{}, sections: map[string]bodySpan{}}
	if d.lines[len(d.lines)-1] == "" {
		d.lines = d.lines[:len(d.lines)-1]
	}
	if strings.HasSuffix(d.lines[0], "\r\n") {
		d.nl = "\r\n"
	}
	for i := 1; i < len(d.lines); i++ {
		if cleanLine(d.lines[i]) == "---" {
			d.close = i
			break
		}
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(d.lines[1:d.close], "")), &root); err != nil {
		return nil, err
	}
	mapping := root.Content[0]
	for i := 0; i < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i], mapping.Content[i+1]
		end := d.close
		if i+2 < len(mapping.Content) {
			end = mapping.Content[i+2].Line
		}
		for end > key.Line+1 {
			line := strings.TrimSpace(d.lines[end-1])
			if line != "" && !strings.HasPrefix(line, "#") {
				break
			}
			end--
		}
		d.fields[key.Value] = fieldSpan{key: key, value: value, start: key.Line, end: end}
	}
	fence := markdownFence{}
	var previous string
	var span bodySpan
	store := func() error {
		if previous == "" || previous == "description" && span.heading == -1 && strings.TrimSpace(strings.Join(d.lines[span.start:span.end], "")) == "" {
			return nil
		}
		if _, exists := d.sections[previous]; exists {
			return source.Refuse("ambiguous-section", "cannot edit repeated "+previous+" sections")
		}
		d.sections[previous] = span
		return nil
	}
	previous, span = "description", bodySpan{-1, d.close + 1, len(d.lines)}
	for i := d.close + 1; i < len(d.lines); i++ {
		line := cleanLine(d.lines[i])
		if fence.consume(line) || !strings.HasPrefix(line, "## ") {
			continue
		}
		category := bodyCategory(strings.TrimPrefix(line, "## "))
		// Unknown sections are boundaries too: editing an adjacent known
		// section must never consume their Markdown.
		if category == "" {
			category = "unknown:" + fmt.Sprint(i)
		}
		span.end = i
		if previous == "description" && span.heading == -1 && strings.TrimSpace(strings.Join(d.lines[span.start:span.end], "")) == "" {
			previous = ""
		}
		if err := store(); err != nil {
			return nil, err
		}
		previous, span = category, bodySpan{i, i + 1, len(d.lines)}
	}
	if err := store(); err != nil {
		return nil, err
	}
	return d, nil
}

func cleanLine(s string) string { return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r") }
func normalizeLines(s, nl string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", nl)
}
func (d *document) splice(start, end int, text string) []byte {
	return []byte(strings.Join(d.lines[:start], "") + text + strings.Join(d.lines[end:], ""))
}
func yamlText(node *yaml.Node) string {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	_ = encoder.Encode(node)
	_ = encoder.Close()
	return strings.TrimSuffix(out.String(), "\n")
}
func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func (d *document) fieldEdit(key string, value any, unset bool) ([]byte, error) {
	old, exists := d.fields[key]
	if unset {
		if !exists {
			return bytes.Clone(d.raw), nil
		}
		return d.splice(old.start, old.end, ""), nil
	}
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return nil, err
	}
	if exists {
		var prior any
		if err := old.value.Decode(&prior); err == nil && reflect.DeepEqual(prior, value) {
			return bytes.Clone(d.raw), nil
		}
		if old.value.Kind == node.Kind {
			node.Style = old.value.Style & (yaml.SingleQuotedStyle | yaml.DoubleQuotedStyle | yaml.FlowStyle)
			if node.Kind == yaml.SequenceNode {
				for _, next := range node.Content {
					for _, previous := range old.value.Content {
						if next.Value == previous.Value {
							next.Style, next.LineComment = previous.Style, previous.LineComment
							break
						}
					}
				}
			}
		}
	}
	// Backlog.md quotes dates with minute precision and string-like dates.
	if slices.Contains([]string{"created_date", "updated_date", "due_date"}, key) {
		node.Style = yaml.SingleQuotedStyle
	}
	comment := ""
	keyNode := stringNode(key)
	if exists {
		keyNode.Style = old.key.Style
		comment = old.value.LineComment
		if comment == "" {
			comment = old.key.LineComment
		}
	}
	text := yamlText(keyNode) + ":"
	if node.Kind == yaml.SequenceNode && len(node.Content) > 0 && node.Style&yaml.FlowStyle == 0 {
		if comment != "" {
			text += " " + comment
		}
		text += d.nl
		// Keep standalone YAML comments even when the list changes.
		if exists {
			for _, line := range d.lines[old.start+1 : old.end] {
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					text += line
				}
			}
		}
		for _, child := range node.Content {
			text += "  - " + yamlText(child) + d.nl
		}
	} else {
		node.Style |= yaml.FlowStyle
		text += " " + yamlText(&node)
		if comment != "" {
			text += " " + comment
		}
		text += d.nl
	}
	text = normalizeLines(text, d.nl)
	if exists {
		return d.splice(old.start, old.end, text), nil
	}
	at := d.close
	if index := slices.Index(backlogFieldOrder, key); index >= 0 {
		for _, successor := range backlogFieldOrder[index+1:] {
			if next, ok := d.fields[successor]; ok {
				at = next.start
				break
			}
		}
	}
	return d.splice(at, at, text), nil
}

func (d *document) listEdit(key string, values []string, removed int) ([]byte, error) {
	old, exists := d.fields[key]
	if exists && old.value.Kind == yaml.SequenceNode && old.value.Style&yaml.FlowStyle == 0 && len(old.value.Content) > 0 && len(values) > 0 {
		if removed < 0 {
			indent := "  "
			line := d.lines[old.value.Content[0].Line]
			if at := strings.Index(line, "- "); at >= 0 {
				indent = line[:at]
			}
			return d.splice(old.end, old.end, indent+"- "+yamlText(stringNode(values[len(values)-1]))+d.nl), nil
		}
		node := old.value.Content[removed]
		if !strings.Contains(node.Value, "\n") {
			return d.splice(node.Line, node.Line+1, ""), nil
		}
	}
	return d.fieldEdit(key, values, false)
}

func sectionConvention(key string) (heading, marker string) {
	switch key {
	case "description":
		return "Description", "SECTION:DESCRIPTION"
	case "criteria":
		return "Acceptance Criteria", "AC"
	case "dod":
		return "Definition of Done", "DOD"
	case "plan":
		return "Implementation Plan", "SECTION:PLAN"
	case "notes":
		return "Implementation Notes", "SECTION:NOTES"
	case "summary":
		return "Final Summary", "SECTION:FINAL_SUMMARY"
	case "comments":
		return "Comments", "COMMENTS"
	}
	return "", ""
}

func sectionKey(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "criteria":
		return "criteria"
	case "dod":
		return "dod"
	default:
		return bodyCategory(value)
	}
}

// contentRange excludes generated markers and their surrounding whitespace.
func (d *document) contentRange(span bodySpan) (int, int) {
	start, end := span.start, span.end
	for i := start; i < end; i++ {
		line := strings.TrimSpace(d.lines[i])
		if markerLine.MatchString(line) && strings.HasSuffix(strings.ToUpper(line), ":BEGIN -->") {
			start = i + 1
			break
		}
	}
	for i := end - 1; i >= start; i-- {
		line := strings.TrimSpace(d.lines[i])
		if markerLine.MatchString(line) && strings.HasSuffix(strings.ToUpper(line), ":END -->") {
			end = i
			break
		}
	}
	for start < end && strings.TrimSpace(d.lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(d.lines[end-1]) == "" {
		end--
	}
	return start, end
}

func (d *document) sectionEdit(key, text string, appendText bool) ([]byte, error) {
	heading, marker := sectionConvention(key)
	if heading == "" {
		return nil, source.Refuse("unsupported", "unknown section")
	}
	text = strings.Trim(normalizeLines(text, d.nl), "\r\n")
	if old, ok := d.sections[key]; ok {
		start, end := d.contentRange(old)
		if appendText {
			prefix := ""
			if end > start {
				prefix = d.nl
				if !strings.HasSuffix(d.lines[end-1], "\n") {
					prefix += d.nl
				}
			}
			return d.splice(end, end, prefix+text+d.nl), nil
		}
		if text != "" {
			text += d.nl
		}
		return d.splice(start, end, text), nil
	}
	if text == "" {
		return bytes.Clone(d.raw), nil
	}
	at := len(d.lines)
	index := slices.Index(backlogSectionOrder, key)
	for _, successor := range backlogSectionOrder[index+1:] {
		if next, ok := d.sections[successor]; ok {
			at = next.heading
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
	block := "## " + heading + d.nl
	if key != "criteria" && key != "dod" {
		block += d.nl
	}
	block += "<!-- " + marker + ":BEGIN -->" + d.nl + text + d.nl + "<!-- " + marker + ":END -->" + d.nl
	if at < len(d.lines) {
		block += d.nl
	}
	return d.splice(at, at, prefix+block), nil
}

func (d *document) criterionLines(key string) []int {
	span, ok := d.sections[key]
	if !ok {
		return nil
	}
	var result []int
	fence := markdownFence{}
	for i := span.start; i < span.end; i++ {
		line := cleanLine(d.lines[i])
		if !fence.consume(line) && checklistLine.MatchString(line) {
			result = append(result, i)
		}
	}
	return result
}

// numberCriteria only changes generated indexes, never fenced examples or
// continuation lines. Existing generated indexes are removed exactly once.
func (d *document) numberCriteria() []byte {
	lines := slices.Clone(d.lines)
	for _, key := range []string{"criteria", "dod"} {
		for n, line := range d.criterionLines(key) {
			parts := checklistLine.FindStringSubmatch(cleanLine(lines[line]))
			ending := strings.TrimPrefix(lines[line], cleanLine(lines[line]))
			lines[line] = parts[1] + parts[2] + parts[3] + fmt.Sprintf("#%d ", n+1) + generatedIndex.ReplaceAllString(parts[4], "") + ending
		}
	}
	return []byte(strings.Join(lines, ""))
}

func indexedText(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	n := 0
	fence := markdownFence{}
	for i, line := range lines {
		if fence.consume(line) {
			continue
		}
		if parts := checklistLine.FindStringSubmatch(line); parts != nil {
			n++
			lines[i] = parts[1] + parts[2] + parts[3] + fmt.Sprintf("#%d ", n) + parts[4]
		}
	}
	return strings.Join(lines, "\n")
}

func readCriteria(text string) []model.Criterion {
	lines := strings.Split(text, "\n")
	var result []model.Criterion
	fence := markdownFence{}
	for i := 0; i < len(lines); i++ {
		if fence.consume(lines[i]) {
			continue
		}
		parts := checklistLine.FindStringSubmatch(lines[i])
		if parts == nil {
			continue
		}
		criterion := model.Criterion{Text: parts[4], Checked: parts[2] != " "}
		for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], "  ") || strings.HasPrefix(lines[i+1], "\t")) {
			i++
			if value, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "Evidence:"); ok {
				criterion.Evidence = strings.TrimSpace(value)
			} else {
				criterion.Text += "\n" + lines[i]
			}
		}
		result = append(result, criterion)
	}
	return result
}

func (d *document) criterionEdit(m model.Mutation) ([]byte, error) {
	key := sectionKey(m.Section)
	if key != "criteria" && key != "dod" {
		return nil, source.Refuse("invalid-value", "expected criteria or dod section")
	}
	entries := d.criterionLines(key)
	if m.Op == model.AddCriterion {
		if strings.TrimSpace(m.Text) == "" || strings.ContainsAny(m.Text, "\r\n") {
			return nil, source.Refuse("invalid-value", "criterion must be nonempty and single-line")
		}
		return d.sectionEdit(key, fmt.Sprintf("- [ ] #%d %s", len(entries)+1, m.Text), true)
	}
	if m.Index < 1 || m.Index > len(entries) {
		return nil, source.Refuse("invalid-value", "criterion index")
	}
	start := entries[m.Index-1]
	end := start + 1
	span := d.sections[key]
	for end < span.end && (strings.HasPrefix(d.lines[end], "  ") || strings.HasPrefix(d.lines[end], "\t")) && !markerLine.MatchString(strings.TrimSpace(d.lines[end])) {
		end++
	}
	if m.Op == model.RemoveCriterion {
		return d.splice(start, end, ""), nil
	}
	parts := checklistLine.FindStringSubmatch(cleanLine(d.lines[start]))
	mark := "x"
	if m.Op == model.UncheckCriterion {
		mark = " "
	}
	text := parts[1] + mark + parts[3] + parts[4] + d.nl
	for _, line := range d.lines[start+1 : end] {
		if strings.HasPrefix(strings.TrimSpace(line), "Evidence:") && (m.Op == model.UncheckCriterion || m.Evidence != nil) {
			continue
		}
		text += line
	}
	if m.Op == model.CheckCriterion && m.Evidence != nil {
		if strings.TrimSpace(*m.Evidence) == "" || strings.ContainsAny(*m.Evidence, "\r\n") {
			return nil, source.Refuse("invalid-value", "evidence must be nonempty and single-line")
		}
		text += "  Evidence: " + *m.Evidence + d.nl
	}
	return d.splice(start, end, text), nil
}
