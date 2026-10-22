package query

import (
	"slices"
	"strings"
)

// Candidate.Value is the unquoted semantic value; Name is a display label.
// Callers quote Value when inserting whitespace or query punctuation.
type Candidate struct{ Value, Name string }

// Complete offers fields for an empty field, otherwise values. Hook-only fields
// are omitted outside hook if:. Declared values precede sorted values in use.
func (s Snapshot) Complete(field, prefix string, options Options) []Candidate {
	var candidates []Candidate
	field = strings.ToLower(field)
	if field == "" {
		for _, f := range fields {
			if !hookField(f) || options.Hook {
				candidates = append(candidates, Candidate{Value: f + ":"})
			}
		}
	} else if !hookField(field) || options.Hook {
		candidates = s.values(field)
	}
	out := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if strings.HasPrefix(strings.ToLower(c.Value), strings.ToLower(prefix)) || c.Name != "" && strings.HasPrefix(strings.ToLower(c.Name), strings.ToLower(prefix)) {
			out = append(out, c)
		}
	}
	return out
}
func (s Snapshot) values(field string) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	add := func(value, name string) {
		key := value
		if field == "status" {
			key = strings.ToLower(key)
		}
		if value != "" && !seen[key] {
			out = append(out, Candidate{value, name})
			seen[key] = true
		}
	}
	switch field {
	case "status":
		for _, v := range s.Settings.Statuses {
			add(v.Key, v.Name)
		}
	case "label":
		for _, v := range s.Settings.Labels {
			add(v, "")
		}
	case "milestone":
		for _, v := range s.Settings.Milestones {
			if !v.Archived {
				add(v.Key, v.Name)
			}
		}
	case "is":
		for _, v := range isValues {
			add(v, "")
		}
	case "has":
		for _, v := range hasValues {
			add(v, "")
		}
	case "priority":
		for _, v := range priorities {
			add(v, "")
		}
	case "category":
		for _, v := range categories {
			add(v, "")
		}
	case "claimed":
		for _, v := range []string{"me", "others", "none"} {
			add(v, "")
		}
	case "outcome":
		for _, v := range outcomes {
			add(v, "")
		}
	case "sort":
		for _, v := range sortKeys {
			add(v, "")
		}
	case "group":
		for _, v := range groupKeys {
			add(v, "")
		}
	}
	var used []string
	for _, i := range s.Items {
		switch field {
		case "id", "parent", "blocked-by", "blocks":
			used = append(used, i.Ref.ID)
		case "title", "text":
			used = append(used, i.Title)
		case "status":
			used = append(used, i.Status)
		case "label":
			used = append(used, i.Labels...)
		case "milestone":
			used = append(used, i.Milestone)
		case "assignee":
			used = append(used, i.Assignees...)
		case "type":
			used = append(used, i.Type)
		case "source":
			used = append(used, i.Ref.Source)
		default:
			if strings.HasPrefix(field, "field.") {
				for key := range i.Fields {
					used = append(used, "field."+key+":")
				}
			}
		}
	}
	slices.Sort(used)
	for _, v := range used {
		add(v, "")
	}
	return out
}
