package tui

import (
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/model"
)

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
			m, backend := editFixture(t, []model.Item{item}, model.Capabilities{}, Options{Me: "@me"})
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
