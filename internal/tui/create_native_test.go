package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
)

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
	m.capture.loadFields(item)
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
	m.capture.loadFields(saved)
	changes := map[string]string{
		"title": "Edited", "status": "active", "type": "bug", "priority": "urgent", "rank": "b",
		"assignees": "bob", "labels": "review", "milestone": "m-2", "due": "2026-12-02",
		"parent": "BL-10", "blocked_by": "BL-11", "related": "BL-12", "duplicate_of": "BL-13",
		"refs": "new-ref", "files": "new-file", "description": `Edited\nbody`,
		"criteria": `[{"text":"changed","checked":true}]`, "dod": `[{"text":"verified"}]`,
		"plan": "next plan", "notes": "next notes", "summary": "next summary",
	}
	for i := range m.capture.fields {
		m.capture.fields[i].value = changes[m.capture.fields[i].name]
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
