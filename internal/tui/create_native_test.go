package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
)

// A duplicate warning that creates anyway, or a create that never happens
// after it, defeats the check. Quick-add stays on its line while checking.
func TestDuplicateTitleCreatesOnSecondEnter(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	dir := filepath.Join(root, "items")
	backend, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: dir, CommonDir: filepath.Join(root, ".git"), Statuses: map[string]model.StatusCategory{"todo": model.Todo}})
	if err != nil {
		t.Fatal(err)
	}
	statuses := []project.StatusConfig{{Key: "todo", Category: "todo"}}
	cfg := project.Config{Settings: project.Settings{Statuses: statuses, Sources: []project.SourceConfig{{ID: "tasks", Type: "native", Path: dir, Prefix: "BL"}}}}
	cfg.Discovery.CommonDir = filepath.Join(root, ".git")
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
	defer store.Close()
	if err := <-store.Mutate(model.Mutation{Op: model.Create, Ref: model.Ref{Source: "tasks"}, Item: &model.Item{Title: "Fix login", Status: "todo"}}); err != nil {
		t.Fatal(err)
	}
	checks := 0
	m := New(Options{Context: ctx, Store: store, Statuses: statuses, DuplicateTitle: func(ctx context.Context, source, title string) error {
		checks++
		return engine.DuplicateTitle(ctx, cfg, source, title)
	}})
	enter := func() tea.Cmd {
		t.Helper()
		cmd := m.updateCapture("enter")
		if checked, ok := cmd().(titleCheckedMsg); ok {
			return m.titleChecked(checked)
		}
		return cmd
	}
	saved := editResultMsg{message: "Item saved."}
	m.capture = captureState{open: true, line: newLine("Fix  LOGIN #bug"), source: "tasks"}
	if cmd := enter(); cmd != nil || !strings.Contains(m.capture.errorText, "tasks:BL-1") || m.capture.full {
		t.Fatalf("first enter: %v %q full=%v", cmd, m.capture.errorText, m.capture.full)
	}
	m.updateCapture("esc")
	if !m.capture.open || m.capture.errorText != "" {
		t.Fatal("esc closed the prompt instead of dismissing the matches")
	}
	enter()
	if result := m.updateCapture("enter")(); result != saved || checks != 2 {
		t.Fatalf("second enter: %v checks=%d", result, checks)
	}
	m.capture = captureState{open: true, line: newLine("Other work"), source: "tasks"}
	if result := enter()(); result != saved {
		t.Fatalf("unmatched title: %v", result)
	}
	waitEdit(t, func() bool { return store.Current().Len() == 3 })
}

// Create and edit must go through the native mutation pipeline: a form that
// only changes its local copy would lose sections and optional frontmatter.
func TestNativeCaptureCreateAndEditAllFields(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	dir := filepath.Join(root, "items")
	backend, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: dir, CommonDir: filepath.Join(root, ".git"), Statuses: map[string]model.StatusCategory{"todo": model.Todo, "active": model.Active}})
	if err != nil {
		t.Fatal(err)
	}
	statuses := []project.StatusConfig{{Key: "todo", Category: "todo"}, {Key: "active", Category: "active"}}
	cfg := project.Config{Settings: project.Settings{Statuses: statuses}}
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
	defer store.Close()
	item := model.Item{Title: "All fields", Status: "todo", Type: "feature", Priority: model.High, Rank: "a", Assignees: []string{"alice"}, Labels: []string{"tui"}, Milestone: "m-1", Due: "2026-12-01", Parent: &model.Ref{ID: "BL-9"}, BlockedBy: []model.Ref{{ID: "BL-8"}}, Related: []model.Ref{{ID: "BL-7"}}, DuplicateOf: &model.Ref{ID: "BL-6"}, Refs: []string{"abc"}, Files: []string{"file"}, Description: "description", Criteria: []model.Criterion{{Text: "criterion"}}, DoD: []model.Criterion{{Text: "done"}}, Plan: "plan", Notes: "notes", Summary: "summary"}
	m := New(Options{Context: ctx, Store: store, CreateSource: "tasks", Statuses: statuses, ValidateItem: func(i model.Item) error { return engine.ValidateItem(cfg, i) }})
	m.capture = captureState{open: true, full: true, source: "tasks"}
	m.capture.loadFields(item, false)
	command := m.updateCapture("enter")
	if m.capture.errorText != "" {
		t.Fatal(m.capture.errorText)
	}
	if command == nil {
		t.Fatal("no create command")
	}
	if msg := command().(editResultMsg); msg.message != "Item saved." {
		t.Fatal(msg.message)
	}
	waitEdit(t, func() bool { return store.Current().Len() == 1 })
	var saved model.Item
	for _, i := range store.Current().Items() {
		saved = i
	}
	for _, name := range captureNames {
		if captureValue(saved, name) != captureValue(item, name) {
			t.Fatalf("create %s: got %q want %q", name, captureValue(saved, name), captureValue(item, name))
		}
	}
	m.capture = captureState{open: true, full: true, source: "tasks", original: saved, version: saved.Version}
	m.capture.loadFields(saved, false)
	changes := map[string]string{
		"title": "Edited", "status": "active", "type": "bug", "priority": "urgent", "rank": "b",
		"assignees": "bob", "labels": "review", "milestone": "m-2", "due": "2026-12-02",
		"parent": "BL-10", "blocked_by": "BL-11", "related": "BL-12", "duplicate_of": "BL-13",
		"refs": "new-ref", "files": "new-file", "description": `Edited\nbody`,
		"criteria": `- [x] changed\n  Evidence: ran it`, "dod": `verified`,
		"plan": "next plan", "notes": "next notes", "summary": "next summary",
	}
	for i := range m.capture.fields {
		m.capture.fields[i].value = newLine(changes[m.capture.fields[i].name])
	}
	want, err := captureItem(m.capture.fields, "tasks")
	if err != nil {
		t.Fatal(err)
	}
	command = m.updateCapture("enter")
	if m.capture.errorText != "" {
		t.Fatal(m.capture.errorText)
	}
	if command == nil {
		t.Fatal("no edit command")
	}
	if msg := command().(editResultMsg); msg.message != "Item saved." {
		t.Fatal(msg.message)
	}
	waitEdit(t, func() bool { i, _, ok := store.Current().Get(saved.Ref); return ok && i.Title == "Edited" })
	edited, _, _ := store.Current().Get(saved.Ref)
	for _, name := range captureNames {
		if got := captureValue(edited, name); got != captureValue(want, name) {
			t.Fatalf("edit %s: got %q want %q", name, got, captureValue(want, name))
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, saved.Ref.ID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Edited") {
		t.Fatalf("native file not edited: %s", data)
	}
}
