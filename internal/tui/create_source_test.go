package tui

import (
	"context"
	"path/filepath"
	"slices"
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
	m.capture.line = newLine("Other item")
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
	m.capture.line = newLine("Child item")
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

// Adopting a pull request must create one item, in the create target rather
// than the read-only pulls source, whose title survives quick-add tokens and
// whose refs carry the URL that hides the pull request (design.md D44).
func TestAdoptPullRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	statuses := []project.StatusConfig{{Key: "backlog", Category: "backlog"}, {Key: "todo", Category: "todo"}}
	tasks, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: filepath.Join(t.TempDir(), "tasks"), CommonDir: t.TempDir(), Statuses: map[string]model.StatusCategory{"backlog": model.Backlog, "todo": model.Todo}})
	if err != nil {
		t.Fatal(err)
	}
	url := "https://github.com/acme/app/pull/45"
	pr := model.Item{Ref: model.Ref{Source: "prs", ID: "#45"}, Title: "#12 fix: login +retry", Status: "pr-open", Category: model.Active, Refs: []string{url}, URL: url, Version: "v1"}
	cfg := project.Config{Settings: project.Settings{Statuses: statuses, Sources: []project.SourceConfig{{ID: "tasks", Type: "native"}, {ID: "prs", Type: "github-pulls"}}}}
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": tasks, "prs": newEditFakeSource([]model.Item{pr}, model.Capabilities{})})
	defer store.Close()
	waitEdit(t, func() bool { return store.Current().Len() == 1 })
	m := New(Options{Context: ctx, Store: store, Workspace: &cfg, Sources: []string{"tasks", "prs"}, Statuses: statuses})
	next, _ := m.Update(projectionMsg{Groups: []engine.RowGroup{{Value: "pr-open", Rows: []engine.Row{{Ref: pr.Ref, Title: pr.Title, Status: pr.Status, Version: pr.Version}}}}})
	m = next.(Model)
	next, _ = m.Update(m.prepare()())
	m = send(next.(Model), "j", "+")
	if !m.capture.open || m.capture.source != "tasks" {
		t.Fatalf("adopt: %+v; notice: %s", m.capture, m.notice)
	}
	command := m.updateCapture("enter")
	if command == nil || command().(editResultMsg).message != "Item saved." {
		t.Fatalf("adopt create failed: %+v", m.capture)
	}
	waitEdit(t, func() bool { return store.Current().Len() == 2 })
	for _, item := range store.Current().Items() {
		if item.Ref.Source == "tasks" && (item.Title != pr.Title || !slices.Equal(item.Refs, []string{url}) || item.Status != "todo" || len(item.Labels) > 0) {
			t.Fatalf("adopted item: %+v", item)
		}
	}
	// An unscoped workspace adopts into the pull request's project, even when
	// the anchor's defaults.source names another project's source.
	projects := []project.Config{
		{Settings: project.Settings{Project: project.ProjectConfig{ID: "a"}, Statuses: statuses, Sources: cfg.Sources[:1], Defaults: project.DefaultsConfig{Source: "tasks"}}},
		{Settings: project.Settings{Project: project.ProjectConfig{ID: "b"}, Statuses: statuses, Sources: cfg.Sources}},
	}
	ws := projects[0]
	ws.Workspace = &project.Workspace{Name: "all", Project: "a", Projects: projects, Sources: map[string]project.WorkspaceSource{"a/tasks": {Project: 0, ID: "tasks"}, "b/tasks": {Project: 1, ID: "tasks"}, "b/prs": {Project: 1, ID: "prs"}}}
	pr.Ref.Source = "b/prs"
	registry := source.Registry{"b/prs": newEditFakeSource([]model.Item{pr}, model.Capabilities{})}
	for _, id := range []string{"a/tasks", "b/tasks"} {
		backend, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: filepath.Join(t.TempDir(), "tasks"), CommonDir: t.TempDir(), Statuses: map[string]model.StatusCategory{"backlog": model.Backlog, "todo": model.Todo}})
		if err != nil {
			t.Fatal(err)
		}
		registry[id] = backend
	}
	wsStore := engine.OpenStore(ctx, ws, registry)
	defer wsStore.Close()
	waitEdit(t, func() bool { return wsStore.Current().Len() == 1 })
	m = New(Options{Context: ctx, Store: wsStore, Workspace: &ws, WorkspaceScope: true, Sources: []string{"a/tasks", "b/tasks", "b/prs"}, Statuses: statuses})
	next, _ = m.Update(projectionMsg{Groups: []engine.RowGroup{{Value: "pr-open", Rows: []engine.Row{{Ref: pr.Ref, Title: pr.Title, Status: pr.Status, Version: pr.Version}}}}})
	m = next.(Model)
	next, _ = m.Update(m.prepare()())
	if m = send(next.(Model), "j", "+"); m.capture.source != "b/tasks" {
		t.Fatalf("workspace adopt: source %q; notice: %s", m.capture.source, m.notice)
	}
}
