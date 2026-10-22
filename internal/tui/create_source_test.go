package tui

import (
	"context"
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

// A second configured source must not disable n, and N must create on its
// parent's source even when the picker would otherwise offer another source.
func TestMultiSourceCaptureChoosesSourceAndChildUsesParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	statuses := []project.StatusConfig{{Key: "todo", Category: "todo"}}
	registry := source.Registry{}
	for _, id := range []string{"tasks", "other"} {
		backend, err := native.New(native.Options{ID: id, Prefix: strings.ToUpper(id), Path: filepath.Join(root, id), CommonDir: filepath.Join(root, ".git"), Statuses: map[string]model.StatusCategory{"todo": model.Todo}})
		if err != nil {
			t.Fatal(err)
		}
		registry[id] = backend
	}
	store := engine.OpenStore(ctx, project.Config{Settings: project.Settings{Statuses: statuses}}, registry)
	defer store.Close()
	parent := model.Item{Title: "Parent", Status: "todo"}
	if err := <-store.Mutate(model.Mutation{Op: model.Create, Ref: model.Ref{Source: "tasks"}, Item: &parent}); err != nil {
		t.Fatal(err)
	}
	waitEdit(t, func() bool { return store.Current().Len() == 1 })
	var parentRef model.Ref
	for _, item := range store.Current().Items() {
		parentRef = item.Ref
	}
	m := New(Options{Context: ctx, Store: store, Sources: []string{"tasks", "other"}, Statuses: statuses})
	m = viewRows(m, parentRef.ID)
	m = send(m, "n")
	if !m.capture.choosing || !strings.Contains(m.View().Content, "other") {
		t.Fatalf("create source picker unavailable: %s", m.View().Content)
	}
	m.updateCapture("down")
	m.updateCapture("enter")
	if m.capture.source != "other" {
		t.Fatalf("picked %q, want other", m.capture.source)
	}
	m.capture.line = "Other item"
	command := m.updateCapture("enter")
	if command == nil || command().(editResultMsg).message != "Item saved." {
		t.Fatal("picker creation failed")
	}
	waitEdit(t, func() bool { return store.Current().Len() == 2 })
	found := false
	for _, item := range store.Current().Items() {
		if item.Title == "Other item" {
			found = item.Ref.Source == "other"
		}
	}
	if !found {
		t.Fatal("item was not created on picked source")
	}
	m = send(m, "j") // Move from the group header to the parent row.
	m = send(m, "N")
	if !m.capture.open || m.capture.choosing || m.capture.source != "tasks" {
		t.Fatalf("child source: %+v; notice: %s; view: %s", m.capture, m.notice, m.View().Content)
	}
	m.capture.line = "Child item"
	command = m.updateCapture("enter")
	if command == nil || command().(editResultMsg).message != "Item saved." {
		t.Fatal("child creation failed")
	}
	waitEdit(t, func() bool { return store.Current().Len() == 3 })
	for _, item := range store.Current().Items() {
		if item.Title == "Child item" {
			if item.Ref.Source != "tasks" || item.Parent == nil || item.Parent.ID != parentRef.ID {
				t.Fatalf("wrong child source or parent: %+v", item)
			}
			return
		}
	}
	t.Fatal("child was not created")
}
