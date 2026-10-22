package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
)

// Exercises the real list keys and native store together. It catches loss of
// CRLF, comments, unknown frontmatter, or body bytes across edit/undo/redo.
func TestNativeListEditRepeatUndoRedoRestoresBytes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	backlog := filepath.Join(root, "backlog")
	if err := os.MkdirAll(backlog, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(backlog, "BL-1.md")
	original := []byte("---\r\nid: BL-1\r\ntitle: Original # keep\r\nstatus: todo\r\ncreated: 2026-10-21T00:00:00Z\r\nupdated: 2026-10-21T00:00:00Z\r\ncustom: [one, two] # untouched\r\n---\r\n\r\n## Description\r\n\r\nA body.\r\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	var tick atomic.Int64
	backend, err := native.New(native.Options{
		ID: "tasks", Prefix: "BL", Path: backlog, CommonDir: filepath.Join(root, ".git"),
		Statuses: map[string]model.StatusCategory{"todo": model.Todo, "active": model.Active, "done": model.Done},
		Now:      func() time.Time { return time.Date(2026, 10, 22, 0, 0, int(tick.Add(1)), 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	statuses := []project.StatusConfig{{Key: "todo", Name: "To Do", Category: "todo"}, {Key: "active", Name: "In Progress", Category: "active"}, {Key: "done", Name: "Done", Category: "done"}}
	cfg := project.Config{Settings: project.Settings{Statuses: statuses, Ready: project.ReadyConfig{Statuses: []string{"todo"}, SatisfiedBy: []string{"done"}}}}
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
	defer store.Close()
	waitEdit(t, func() bool { return store.Current().Len() == 1 })
	item, _, ok := store.Current().Get(model.Ref{Source: "tasks", ID: "BL-1"})
	if !ok {
		t.Fatal("native item did not load")
	}
	m := New(Options{Context: ctx, Store: store, Statuses: statuses, NoColor: true})
	m.snapshot = store.Current()
	m.data.rows = []screenRow{{row: engine.Row{Ref: item.Ref, Version: item.Version, Title: item.Title, Status: item.Status}}}
	m.groups = []engine.RowGroup{{Value: "todo", Rows: []engine.Row{{Ref: item.Ref, Version: item.Version, Title: item.Title, Status: item.Status}}}}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m = editPress(m, "t")
	m = editPress(m, "ctrl+u")
	m = editPress(m, "Revised")
	m = editPress(m, "enter")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m = editPress(m, ".")
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(second, []byte("title: Revised")) || !bytes.Equal(withoutUpdatedField(first), withoutUpdatedField(second)) {
		t.Fatalf("repeat failed to apply or preserve native bytes:\n%s", second)
	}
	m = editPress(m, "u")
	undone, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(withoutUpdatedField(first), withoutUpdatedField(undone)) {
		t.Fatalf("undo did not restore prior bytes:\n%s\nwant:\n%s", undone, first)
	}
	m = editPress(m, "u")
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(withoutUpdatedField(before), withoutUpdatedField(restored)) {
		t.Fatalf("undo did not restore original bytes:\n%s\nwant:\n%s", restored, before)
	}
	m = editPress(m, "ctrl+r")
	m = editPress(m, "ctrl+r")
	redone, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(withoutUpdatedField(second), withoutUpdatedField(redone)) {
		t.Fatalf("redo did not restore edited bytes:\n%s\nwant:\n%s", redone, second)
	}
}

func withoutUpdatedField(data []byte) []byte {
	var result []byte
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("updated:")) {
			result = append(result, line...)
		}
	}
	return result
}
