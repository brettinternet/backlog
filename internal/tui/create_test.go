package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
)

// A quick-add token changing its interpretation in the form would silently
// create an item different from `bl new`'s shared query parser.
func TestQuickAddExpandsToSameItem(t *testing.T) {
	t.Parallel()
	line := `Debounce detail !high @me #tui ^BL-3 <BL-10 ~m-2 :feature`
	expected, err := query.QuickAdd(line, "alice")
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{Me: "alice", Statuses: []project.StatusConfig{{Key: "todo", Category: "todo"}}})
	m.capture = captureState{open: true, line: line, source: "tasks"}
	m.expandCapture(false)
	if m.capture.errorText != "" {
		t.Fatal(m.capture.errorText)
	}
	actual, err := captureItem(m.capture.fields, "tasks")
	if err != nil {
		t.Fatal(err)
	}
	if actual.Title != expected.Title || actual.Priority != expected.Priority || actual.Type != expected.Type || actual.Milestone != expected.Milestone || !strings.Contains(captureValue(actual, "assignees"), "alice") || !strings.Contains(captureValue(actual, "blocked_by"), "BL-10") || actual.Parent == nil || actual.Parent.ID != "BL-3" {
		t.Fatalf("quick-add mismatch: %#v vs %#v", actual, expected)
	}
}

// A form must preserve all writable fields and keep an invalid item in the form
// without issuing any engine mutation.
func TestFullFormFieldsAndInlineValidation(t *testing.T) {
	t.Parallel()
	item := model.Item{Ref: model.Ref{Source: "tasks", ID: "BL-1"}, Title: "Title", Status: "todo", Type: "feature", Priority: model.High, Rank: "a", Assignees: []string{"alice"}, Labels: []string{"tui"}, Milestone: "m-2", Due: "2026-12-01", Parent: &model.Ref{ID: "BL-2"}, BlockedBy: []model.Ref{{ID: "BL-3"}}, Related: []model.Ref{{ID: "BL-4"}}, DuplicateOf: &model.Ref{ID: "BL-5"}, Refs: []string{"ref"}, Files: []string{"file"}, Description: "A\nB", Criteria: []model.Criterion{{Text: "works"}}, DoD: []model.Criterion{{Text: "done", Checked: true}}, Plan: "plan", Notes: "notes", Summary: "summary"}
	c := captureState{source: "tasks"}
	c.loadFields(item, false)
	actual, err := captureItem(c.fields, "tasks")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range captureNames {
		if captureValue(actual, name) != captureValue(item, name) {
			t.Fatalf("%s changed: %q vs %q", name, captureValue(actual, name), captureValue(item, name))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	backend := newEditFakeSource(nil, model.Capabilities{model.Create: true})
	store := engine.OpenStore(ctx, project.Config{}, source.Registry{"tasks": backend})
	defer store.Close()
	m := New(Options{Store: store, Context: ctx})
	m.capture = captureState{open: true, full: true, source: "tasks", fields: c.fields}
	for i := range m.capture.fields {
		if m.capture.fields[i].name == "title" {
			m.capture.fields[i].value = " "
		}
	}
	m.updateCapture("enter")
	if !strings.Contains(m.capture.errorText, "title") || len(backend.applied()) != 0 {
		t.Fatalf("validation failed: %s", m.capture.errorText)
	}
}

// Provider-rejected choices must not be reachable through picker shortcuts or
// a full form that would silently drop unsupported ordering metadata.
func TestBacklogMDUnsupportedChoices(t *testing.T) {
	t.Parallel()
	cfg := project.Config{Settings: project.Settings{Sources: []project.SourceConfig{{ID: "tasks", Type: "backlog-md"}}}}
	item := editItem("TASK-1", "Provider item")
	item.Rank = "000100"
	m, backend := editFixture(t, []model.Item{item}, model.Capabilities{model.Set: true, model.Create: true}, Options{Workspace: &cfg, CreateSource: "tasks"})
	m = editPress(m, "p")
	if choiceIndex(m.picker.choices, "urgent") >= 0 {
		t.Fatal("unsupported urgent priority was offered")
	}
	m = editPress(m, "u")
	if len(backend.applied()) != 0 {
		t.Fatal("shortcut bypassed provider choices")
	}
	m = editPress(m, "esc")
	m.beginFullEdit()
	for _, field := range m.capture.fields {
		if field.name == "rank" {
			t.Fatal("unsupported rank editing was offered")
		}
	}
	// Exercise the distinct multi-source chooser route, not only a default.
	store := engine.OpenStore(context.Background(), cfg, source.Registry{
		"tasks": newEditFakeSource(nil, model.Capabilities{model.Create: true}),
		"other": newEditFakeSource(nil, model.Capabilities{model.Create: true}),
	})
	defer store.Close()
	m = New(Options{Store: store, Workspace: &cfg, Sources: []string{"tasks", "other"}})
	m.beginCapture(false)
	if !m.capture.choosing {
		t.Fatal("source chooser did not open")
	}
	m.updateCapture("enter")
	m.capture.line = "New task"
	m.expandCapture(false)
	for _, field := range m.capture.fields {
		if field.name == "rank" {
			t.Fatal("unsupported rank creation was offered")
		}
	}
}
