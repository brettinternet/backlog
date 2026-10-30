package query

import (
	"context"
	"slices"
	"strings"
)

// Candidate.Value is the unquoted semantic value; Name is a display label.
// Callers quote Value when inserting whitespace or query punctuation.
type Candidate struct{ Value, Name string }

// Complete offers fields for an empty field, otherwise values. Hook-only fields
// are omitted outside hook if:. Declared values precede sorted values in use.
func (s Snapshot) Complete(field, prefix string, options Options) []Candidate {
	candidates, _ := s.CompleteContext(context.Background(), field, prefix, options)
	return candidates
}

// CompleteContext cancels obsolete editor work and filters used values before
// sorting them, rather than sorting the entire backlog for each keystroke.
func (s Snapshot) CompleteContext(ctx context.Context, field, prefix string, options Options) ([]Candidate, error) {
	var candidates []Candidate
	field = strings.ToLower(field)
	if field == "" {
		for _, f := range fields {
			if offered(f, options) {
				candidates = append(candidates, Candidate{Value: f + ":"})
			}
		}
		var raw []string
		for n, i := range s.Items {
			if n%256 == 0 && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			for key := range i.Fields {
				raw = append(raw, "field."+strings.ToLower(key)+":")
			}
		}
		slices.Sort(raw)
		for _, f := range slices.Compact(raw) {
			candidates = append(candidates, Candidate{Value: f})
		}
	} else if offered(field, options) {
		candidates = s.valuesPrefix(ctx, field, prefix)
	}
	out := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if strings.HasPrefix(strings.ToLower(c.Value), strings.ToLower(prefix)) || c.Name != "" && strings.HasPrefix(strings.ToLower(c.Name), strings.ToLower(prefix)) {
			out = append(out, c)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
func (s Snapshot) values(field string) []Candidate {
	return s.valuesPrefix(context.Background(), field, "")
}
func (s Snapshot) valuesPrefix(ctx context.Context, field, prefix string) []Candidate {
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
	case "sprint":
		for _, v := range sprintValues {
			add(v, "")
		}
		for _, v := range s.Sprints {
			if v.Name != "" {
				add(v.Name, "")
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
	case "change", "checks", "review":
		for _, v := range map[string][]string{"change": changeValues, "checks": checksValues, "review": reviewValues}[field] {
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
	names := map[string]string{}
	prefix = strings.ToLower(prefix)
	use := func(value string) {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			used = append(used, value)
		}
	}
	// Opaque provider values complete by display name and insert the value.
	useNamed := func(value, name string) {
		if name == "" {
			use(value)
		} else if strings.HasPrefix(strings.ToLower(value), prefix) || strings.HasPrefix(strings.ToLower(name), prefix) {
			used = append(used, value)
			names[value] = name
		}
	}
	for n, i := range s.Items {
		if n%256 == 0 && ctx.Err() != nil {
			return nil
		}
		switch field {
		case "id", "parent", "blocked-by", "blocks":
			use(i.Ref.ID)
		case "title", "text":
			use(i.Title)
		case "status":
			useNamed(i.Status, i.Names[i.Status])
		case "label":
			for _, value := range i.Labels {
				use(value)
			}
		case "milestone":
			use(i.Milestone)
		case "assignee":
			for _, value := range i.Assignees {
				useNamed(value, i.Names[value])
			}
		case "type":
			use(i.Type)
		case "source":
			use(i.Ref.Source)
			if _, id, composed := strings.Cut(i.Ref.Source, "/"); composed {
				use(id)
			}
		}
	}
	slices.Sort(used)
	for _, v := range used {
		add(v, names[v])
	}
	return out
}
