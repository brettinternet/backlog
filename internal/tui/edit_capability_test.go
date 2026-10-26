package tui

import (
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

// A global project status must not leak into a per-issue workflow picker.
func TestJiraStatusPickerUsesDiscoveredTransitions(t *testing.T) {
	item := editItem("BL-1", "Jira issue")
	item.Fields = map[string]any{"jiraTransitions": map[string]any{"In Progress": map[string]any{"id": "21", "category": "active"}}}
	m, _ := editFixture(t, []model.Item{item}, model.Capabilities{model.Set: true}, Options{Statuses: []project.StatusConfig{{Key: "unavailable", Name: "Unavailable"}}})
	m = editPress(m, "s")
	if len(m.picker.choices) != 1 || m.picker.choices[0].value != "In Progress" {
		t.Fatalf("undiscovered transition offered: %+v", m.picker.choices)
	}
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
