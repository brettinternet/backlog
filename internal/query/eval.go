package query

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

// State carries derived engine facts. An absent readiness/claim stays unknown;
// it never proves an item ready, claimed, or free. Claim uses the query
// vocabulary (me, others, none), so callers map engine.ClaimMine to "me".
type State struct {
	Readiness, Claim  string
	Archived, Pending bool
}
type Event struct {
	Type                  string
	Changed               []string
	Hook, Action, Outcome string
}

// Snapshot is read-only. Next is the complete engine ranking (not just ready
// candidates). Sorting uses its order for ties; absent refs fall back to ID.
// Now is injected by the caller; no operation reads the wall clock.
type Snapshot struct {
	Items        []model.Item
	Settings     project.Settings
	Me           []string
	Now          time.Time
	States       map[model.Ref]State
	Next         []model.Ref
	Coordination bool
	// Sprints is the merged provider sprint catalog; roles derive from it.
	Sprints []model.Sprint
	// TextMatches holds optional snapshot-bound FTS candidates. Missing entries
	// use the in-memory evaluator; candidates are always post-filtered.
	TextMatches map[string]map[model.Ref]bool
}

// Validate returns all snapshot-dependent errors in input order. TUI callers can
// show these as warnings; CLI callers must refuse evaluation when any exist.
func (q Query) Validate(s Snapshot) []error {
	var errors []error
	for _, t := range q.Terms {
		if (t.Field == "claimed" || t.Field == "is" && hasValue(t, "claimed") || t.Field == "has" && hasValue(t, "claim")) && !s.Coordination {
			errors = append(errors, fail(t.Pos, "claim queries require coordination"))
			continue
		}
		switch t.Field {
		case "sprint":
			if !s.SprintCapable() {
				errors = append(errors, fail(t.Pos, "no source provides sprints"))
				continue
			}
			for _, v := range t.Values {
				if slices.Contains(sprintValues, strings.ToLower(v.Text)) || slices.ContainsFunc(s.Sprints, func(sprint model.Sprint) bool {
					return sprint.Key == v.Text || strings.EqualFold(sprint.Name, v.Text)
				}) {
					continue
				}
				names := slices.Clone(sprintValues)
				for _, sprint := range s.Sprints {
					names = append(names, sprint.Name)
				}
				errors = append(errors, &Error{Pos: v.Pos, Message: fmt.Sprintf("unknown sprint value %q", v.Text), Suggestion: closest(v.Text, names)})
			}
		case "status", "label", "milestone", "type", "source", "assignee":
			known := s.values(t.Field)
			for _, v := range t.Values {
				if strings.EqualFold(v.Text, "none") && t.Field != "status" || t.Field == "assignee" && strings.EqualFold(v.Text, "me") {
					continue
				}
				exists := false
				for _, k := range known {
					if equal(t.Field, k.Value, v.Text) || t.Field == "status" && strings.EqualFold(k.Name, v.Text) {
						exists = true
						break
					}
				}
				if !exists {
					names := make([]string, len(known))
					for i, k := range known {
						names[i] = k.Value
					}
					errors = append(errors, &Error{Pos: v.Pos, Message: fmt.Sprintf("unknown %s value %q", t.Field, v.Text), Suggestion: closest(v.Text, names)})
				}
			}
		}
	}
	return errors
}
func hasValue(t Term, v string) bool {
	for _, x := range t.Values {
		if strings.EqualFold(x.Text, v) {
			return true
		}
	}
	return false
}

// Match performs AND across terms and OR across each term's values. Event terms
// are false without an event (before applying an explicit negation).
func (q Query) Match(item model.Item, s Snapshot, event *Event) bool {
	for _, t := range q.Terms {
		matched := false
		for _, v := range t.Values {
			if matchValue(item, s, event, t.Field, v) {
				matched = true
				break
			}
		}
		if matched == t.Negated {
			return false
		}
	}
	return true
}
func equal(field, a, b string) bool {
	if slices.Contains([]string{"status", "category", "priority", "is", "has", "claimed", "outcome"}, field) {
		return strings.EqualFold(a, b)
	}
	return a == b
}
func matchValue(i model.Item, s Snapshot, e *Event, f string, v Value) bool {
	state := s.States[i.Ref]
	text := v.Text
	lower := strings.ToLower(text)
	list := func(values []string) bool {
		if lower == "none" {
			return len(values) == 0
		}
		for _, value := range values {
			if equal(f, value, text) {
				return true
			}
		}
		return false
	}
	refList := func(refs []model.Ref) bool {
		for _, r := range refs {
			if matchID(r, text) {
				return true
			}
		}
		return false
	}
	switch f {
	case "":
		return contains(i.Ref.ID+"\n"+i.Title, text)
	case "id":
		return matchID(i.Ref, text)
	case "status":
		if strings.EqualFold(i.Status, text) {
			return true
		}
		for _, status := range s.Settings.Statuses {
			if strings.EqualFold(status.Key, i.Status) && strings.EqualFold(status.Name, text) {
				return true
			}
		}
		return false
	case "category":
		return strings.EqualFold(string(i.Category), text)
	case "priority":
		a, b := priority(string(i.Priority)), priority(text)
		return compare(a-b, v.Op)
	case "assignee":
		if lower == "me" {
			for _, a := range i.Assignees {
				if slices.Contains(s.Me, a) {
					return true
				}
			}
			return false
		}
		return list(i.Assignees)
	case "label":
		return list(i.Labels)
	case "type":
		return scalar(i.Type, text)
	case "milestone":
		return scalar(i.Milestone, text)
	case "sprint":
		return matchSprint(i, s, text)
	case "source":
		return scalar(i.Ref.Source, text)
	case "parent":
		if i.Parent == nil {
			return lower == "none"
		}
		return matchID(*i.Parent, text)
	case "blocked-by":
		return refList(i.BlockedBy)
	case "blocks":
		return refList(i.Blocks)
	case "created", "updated", "due":
		actual := i.Created
		if f == "updated" {
			actual = i.Updated
		}
		if f == "due" {
			var err error
			actual, err = time.ParseInLocation(time.DateOnly, i.Due, s.Now.Location())
			if err != nil {
				return false
			}
		}
		if actual.IsZero() {
			return false
		}
		target, age, err := dateValue(text, s.Now)
		if err != nil {
			return false
		}
		if !age {
			y, m, d := actual.In(s.Now.Location()).Date()
			actual = time.Date(y, m, d, 0, 0, 0, 0, s.Now.Location())
		}
		c := actual.Compare(target)
		if age {
			c = -c
		}
		return compare(c, v.Op)
	case "text":
		if matches, ok := s.TextMatches[text]; ok && !matches[i.Ref] {
			return false
		}
		return contains(SearchText(i), text)
	case "claimed":
		return s.Coordination && state.Claim == lower
	case "is":
		switch lower {
		case "open":
			return slices.Contains([]model.StatusCategory{model.Backlog, model.Todo, model.Active}, i.Category)
		case "closed":
			return i.Category == model.Done || i.Category == model.Canceled
		case "ready", "blocked", "waiting", "unknown":
			return state.Readiness == lower || lower == "unknown" && state.Readiness == ""
		case "mine":
			for _, a := range i.Assignees {
				if slices.Contains(s.Me, a) {
					return true
				}
			}
			return false
		case "claimed":
			return s.Coordination && (state.Claim == "me" || state.Claim == "others")
		case "archived":
			return state.Archived
		case "pending":
			return state.Pending
		}
	case "has":
		switch lower {
		case "parent":
			return i.Parent != nil
		case "children":
			return len(i.Children) > 0
		case "blockers":
			return len(i.BlockedBy) > 0
		case "assignee":
			return len(i.Assignees) > 0
		case "due":
			return i.Due != ""
		case "criteria":
			return len(i.Criteria) > 0
		case "claim":
			return s.Coordination && (state.Claim == "me" || state.Claim == "others")
		}
	case "changed":
		if e != nil {
			for _, field := range e.Changed {
				if strings.EqualFold(field, text) {
					return true
				}
			}
		}
		return false
	case "hook":
		return e != nil && e.Type == "hook.finished" && e.Hook == text
	case "action":
		return e != nil && e.Type == "action.finished" && e.Action == text
	case "outcome":
		return e != nil && (e.Type == "hook.finished" || e.Type == "action.finished") && strings.EqualFold(e.Outcome, text)
	default:
		if name, ok := strings.CutPrefix(f, "field."); ok {
			// f is lowercased; raw keys keep their file spelling.
			present := false
			for key, value := range i.Fields {
				if !strings.EqualFold(key, name) || value == nil {
					continue
				}
				present = true
				switch x := value.(type) {
				case []string:
					if list(x) {
						return true
					}
				case []any:
					for _, a := range x {
						if fmt.Sprint(a) == text {
							return true
						}
					}
				default:
					if fmt.Sprint(value) == text {
						return true
					}
				}
			}
			return !present && lower == "none"
		}
	}
	return false
}
func scalar(a, b string) bool   { return a == b || a == "" && strings.EqualFold(b, "none") }
func contains(a, b string) bool { return strings.Contains(strings.ToLower(a), strings.ToLower(b)) }
func compare(c int, op string) bool {
	switch op {
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	default:
		return c == 0
	}
}
func priority(s string) int {
	switch strings.ToLower(s) {
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "urgent":
		return 4
	}
	return 0
}

// Numeric shorthand matches the last hyphen-separated ID component, ignoring
// zero padding. Full IDs remain exact; source:ID disambiguates across sources.
func matchID(ref model.Ref, value string) bool {
	if source, id, ok := strings.Cut(value, ":"); ok {
		return ref.Source == source && matchID(model.Ref{ID: ref.ID}, id)
	}
	numeric := value != ""
	for _, c := range value {
		if c < '0' || c > '9' {
			numeric = false
			break
		}
	}
	if !numeric {
		return ref.ID == value
	}
	id := ref.ID
	if at := strings.LastIndexByte(id, '-'); at >= 0 {
		id = id[at+1:]
	}
	return strings.TrimLeft(id, "0") == strings.TrimLeft(value, "0")
}

// SearchText is the exact text: search surface shared with the cache index.
func SearchText(i model.Item) string { return i.Title + "\n" + body(i) }

func body(i model.Item) string {
	parts := []string{i.Description, i.Plan, i.Notes, i.Summary}
	for _, c := range append(slices.Clone(i.Criteria), i.DoD...) {
		parts = append(parts, c.Text)
	}
	for _, c := range i.Comments {
		parts = append(parts, c.Body)
	}
	return strings.Join(parts, "\n")
}
