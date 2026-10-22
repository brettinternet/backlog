package backlogmd

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

var markerLine = regexp.MustCompile(`(?i)^<!--\s*(?:SECTION:[A-Z0-9_]+:(?:BEGIN|END)|(?:AC|DOD|COMMENTS):(?:BEGIN|END))\s*-->$`)

// sourceDocument is the parsed, source-faithful view used to construct the
// import plan. Values are decoded only after duplicate keys and YAML aliases
// have been rejected.
type sourceDocument struct {
	fields map[string]any
	body   parsedBody
}

type parsedBody struct {
	sections  map[string]string
	originals map[string]string
	unknown   []unknownSection
	comments  string
}

type unknownSection struct {
	heading string
	content string
}

func parseSourceDocument(data []byte) (sourceDocument, error) {
	if !utf8.Valid(data) {
		return sourceDocument{}, fmt.Errorf("invalid UTF-8")
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSuffix(lines[0], "\r") != "---" {
		return sourceDocument{}, fmt.Errorf("expected opening YAML frontmatter")
	}
	close := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSuffix(lines[i], "\r") == "---" {
			close = i
			break
		}
	}
	if close < 0 {
		return sourceDocument{}, fmt.Errorf("missing closing YAML frontmatter")
	}
	var root yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(strings.Join(lines[1:close], "\n")))
	if err := decoder.Decode(&root); err != nil {
		return sourceDocument{}, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return sourceDocument{}, fmt.Errorf("expected one YAML frontmatter document")
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode || root.Content[0].Style&yaml.FlowStyle != 0 {
		return sourceDocument{}, fmt.Errorf("expected YAML frontmatter mapping")
	}
	if err := rejectYAMLAliases(root.Content[0]); err != nil {
		return sourceDocument{}, err
	}
	fields := make(map[string]any, len(root.Content[0].Content)/2)
	if err := root.Content[0].Decode(&fields); err != nil {
		return sourceDocument{}, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	dateFields := map[string]bool{"created_date": true, "updated_date": true, "created": true, "updated": true, "due": true}
	for i := 0; i < len(root.Content[0].Content); i += 2 {
		key, value := root.Content[0].Content[i], root.Content[0].Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return sourceDocument{}, fmt.Errorf("frontmatter keys must be strings at line %d", key.Line)
		}
		if dateFields[key.Value] && value.Kind == yaml.ScalarNode {
			// YAML decoders may interpret unquoted, timezone-less timestamps in
			// the process local zone. Keep their lexical form so the importer
			// can consistently interpret them as UTC.
			fields[key.Value] = value.Value
		}
	}
	body := strings.Join(lines[close+1:], "\n")
	return sourceDocument{fields: fields, body: parseBody(body)}, nil
}

func rejectYAMLAliases(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fmt.Errorf("YAML anchors and aliases are not supported at line %d", node.Line)
	}
	for _, child := range node.Content {
		if err := rejectYAMLAliases(child); err != nil {
			return err
		}
	}
	return nil
}

func parseBody(text string) parsedBody {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var names []string
	var parts [][]string
	name := ""
	current := []string{}
	fence := markdownFence{}
	for _, line := range lines {
		if !fence.consume(line) {
			if strings.HasPrefix(line, "## ") {
				names = append(names, name)
				parts = append(parts, current)
				name = strings.TrimSpace(strings.TrimPrefix(line, "## "))
				current = nil
				continue
			}
		}
		current = append(current, line)
	}
	names = append(names, name)
	parts = append(parts, current)

	result := parsedBody{sections: map[string]string{}, originals: map[string]string{}}
	var descriptions []string
	for i, heading := range names {
		original := strings.Join(parts[i], "\n")
		content := stripStructuralMarkers(original)
		category := bodyCategory(heading)
		if category == "" {
			if heading == "" {
				category = "description"
			} else {
				result.unknown = append(result.unknown, unknownSection{heading: heading, content: content})
				descriptions = append(descriptions, "## "+heading+"\n\n"+content)
				result.originals["unknown:"+heading] = original
				continue
			}
		}
		result.originals[category] = joinSection(result.originals[category], original)
		result.sections[category] = joinSection(result.sections[category], content)
	}
	if result.sections["description"] != "" {
		descriptions = append([]string{result.sections["description"]}, descriptions...)
	}
	result.sections["description"] = strings.Trim(strings.Join(descriptions, "\n\n"), "\n")
	result.comments = result.sections["comments"]
	return result
}

func bodyCategory(heading string) string {
	normalized := strings.ToLower(strings.TrimSpace(heading))
	switch normalized {
	case "description":
		return "description"
	case "acceptance criteria":
		return "criteria"
	case "definition of done":
		return "dod"
	case "plan", "implementation plan":
		return "plan"
	case "notes", "implementation notes":
		return "notes"
	case "summary", "final summary":
		return "summary"
	case "comments":
		return "comments"
	default:
		return ""
	}
}

func stripStructuralMarkers(text string) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	fence := markdownFence{}
	for _, line := range lines {
		if !fence.consume(line) && markerLine.MatchString(strings.TrimSpace(line)) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Trim(strings.Join(kept, "\n"), "\n")
}

func joinSection(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}

type markdownFence struct {
	char  byte
	count int
}

func (f *markdownFence) consume(line string) bool {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 || len(s) < 3 {
		return f.count > 0
	}
	c, n := s[0], 0
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
		f.char, f.count = c, n
		return true
	}
	return false
}

func parseLegacyTime(value any) (time.Time, error) {
	if instant, ok := value.(time.Time); ok {
		return instant.UTC().Truncate(time.Second), nil
	}
	text, ok := value.(string)
	if !ok {
		return time.Time{}, fmt.Errorf("expected date or timestamp string, got %T", value)
	}
	text = strings.TrimSpace(text)
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC().Truncate(time.Second), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date or timestamp %q", text)
}

func decodeYAMLMapping(data []byte) (map[string]any, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected YAML mapping")
	}
	if err := rejectYAMLAliases(root.Content[0]); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected one YAML document")
	}
	result := map[string]any{}
	if err := root.Content[0].Decode(&result); err != nil {
		return nil, err
	}
	for i := 0; i < len(root.Content[0].Content); i += 2 {
		key := root.Content[0].Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, fmt.Errorf("YAML keys must be strings at line %d", key.Line)
		}
	}
	return result, nil
}
