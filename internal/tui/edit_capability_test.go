package tui

import (
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

// A configured status the item's source doesn't accept must not leak into the
// picker or a step, as GitHub rejects todo for an issue in Triage (D35).
func TestStatusEditsOfferOnlySourceStatuses(t *testing.T) {
	item := editItem("BL-1", "Issue")
	item.Status = "triage"
	m, backend := editFixture(t, []model.Item{item}, model.Capabilities{model.Set: true}, Options{})
	m.opts.Statuses = append([]project.StatusConfig{{Key: "triage", Name: "Triage", Category: "backlog"}}, m.opts.Statuses...)
	backend.choices = []model.StatusChoice{{Key: "active", Category: model.Active}, {Key: "done", Category: model.Done}}
	m = editPress(m, "s")
	if values := pickerValues(m.picker.choices); strings.Join(values, ",") != "active,done" {
		t.Fatalf("picker offered %v", values)
	}
	m = editPress(m, "esc")
	m = editPress(m, ">")
	if got := lastStatus(backend); got != "active" {
		t.Fatalf("> wrote %q, want the next accepted status", got)
	}
	waitEdit(t, func() bool { current, _, _ := m.opts.Store.Current().Get(item.Ref); return current.Status == "active" })
	m = editPress(m, "<")
	if !strings.Contains(m.notice, "source tasks accepts no previous status") {
		t.Fatalf("step into an unaccepted status: %q", m.notice)
	}
}

// Regression: undeclared source keys, such as Jira transitions or GitHub's
// open, rank after config, and < from one must move back, not stay put.
func TestStepStatusUndeclaredKeys(t *testing.T) {
	statuses := []project.StatusConfig{{Key: "a"}, {Key: "b"}}
	accepted := []model.StatusChoice{{Key: "a"}, {Key: "x"}, {Key: "y"}}
	for _, c := range []struct {
		current   string
		direction int
		want      string
	}{{"y", -1, "x"}, {"x", -1, "a"}, {"x", 1, "y"}, {"b", -1, "a"}, {"b", 1, "x"}, {"y", 1, ""}} {
		if got := stepStatus(statuses, accepted, c.current, c.direction); got != c.want {
			t.Errorf("%s %+d: got %q want %q", c.current, c.direction, got, c.want)
		}
	}
}

func pickerValues(choices []pickerChoice) []string {
	values := []string{}
	for _, choice := range choices {
		values = append(values, choice.value)
	}
	return values
}

func lastStatus(backend *editFakeSource) string {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.mutations) == 0 {
		return ""
	}
	value, _ := backend.mutations[len(backend.mutations)-1].Value.(string)
	return value
}

func TestUnsupportedEditKeysOnlyShowReason(t *testing.T) {
	cases := []struct {
		name     string
		keys     []string
		item     model.Item
		criteria bool
	}{
		{name: "status", keys: []string{"s"}},
		{name: "priority", keys: []string{"p"}},
		{name: "assignees", keys: []string{"a"}},
		{name: "self assignment", keys: []string{"A"}},
		{name: "labels", keys: []string{"#"}},
		{name: "milestone", keys: []string{"m"}},
		{name: "blocker", keys: []string{"b"}},
		{name: "parent", keys: []string{"P"}},
		{name: "title", keys: []string{"t"}},
		{name: "next status", keys: []string{">"}},
		{name: "start", keys: []string{"S"}},
		{name: "done", keys: []string{"D"}},
		{name: "archive", keys: []string{"d", "d"}},
		{name: "criterion", keys: []string{"x"}, item: func() model.Item {
			i := editItem("BL-1", "First")
			i.Criteria = []model.Criterion{{Text: "one"}}
			return i
		}(), criteria: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			item := editItem("BL-1", "First")
			if test.item.Ref.ID != "" {
				item = test.item
			}
			m, backend := editFixture(t, []model.Item{item}, model.Capabilities{}, Options{Me: map[string]string{"tasks": "@me"}})
			if test.criteria {
				m.detailOpen, m.detailFocus, m.detailTab = true, true, tabCriteria
			}
			for _, key := range test.keys {
				m = editPress(m, key)
			}
			if m.picker.open || m.archiveConfirm || len(backend.applied()) != 0 || !strings.Contains(m.notice, "Disabled") {
				t.Fatalf("key %v did more than report unavailable capability: picker=%t confirm=%t notice=%q applied=%v", test.keys, m.picker.open, m.archiveConfirm, m.notice, backend.applied())
			}
		})
	}
}
