package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
)

// A fake editor's saved buffer must never overwrite a concurrent native edit
// without an explicitly selected, fresh version-bound retry.
func TestEditorConflictChoicesAndInvalidFrontmatter(t *testing.T) {
	for _, choice := range []string{"y", "t", "r", "invalid-r", "invalid-d"} {
		t.Run(choice, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			root := t.TempDir()
			dir := filepath.Join(root, "items")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "BL-1.md")
			initial := []byte("---\nid: BL-1\ntitle: Original\nstatus: todo\ncreated: 2026-10-21T00:00:00Z\nupdated: 2026-10-21T00:00:00Z\ncustom: keep # untouched\n---\n\n## Description\n\nBody.\n")
			if err := os.WriteFile(path, initial, 0644); err != nil {
				t.Fatal(err)
			}
			backend, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: dir, CommonDir: filepath.Join(root, ".git"), Statuses: map[string]model.StatusCategory{"todo": model.Todo}})
			if err != nil {
				t.Fatal(err)
			}
			cfg := project.Config{Settings: project.Settings{Statuses: []project.StatusConfig{{Key: "todo", Category: "todo"}}}}
			store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
			defer store.Close()
			waitEdit(t, func() bool { return store.Current().Len() == 1 })
			ref := model.Ref{Source: "tasks", ID: "BL-1"}
			item, _, _ := store.Current().Get(ref)
			buffer := filepath.Join(root, "edit.md")
			yours := []byte(strings.Replace(string(initial), "Original", "Yours", 1))
			if strings.HasPrefix(choice, "invalid") {
				yours = []byte("not frontmatter")
			}
			if err := os.WriteFile(buffer, yours, 0600); err != nil {
				t.Fatal(err)
			}
			m := New(Options{Context: ctx, Store: store, ValidateItem: func(i model.Item) error { return engine.ValidateItem(cfg, i) }, NativeBytes: func(_ context.Context, _ model.Item) ([]byte, error) { return os.ReadFile(path) }, NativeSource: func(string) bool { return true }})
			m.longEdit = longEditState{ref: ref, version: item.Version, path: buffer, original: initial}
			if strings.HasPrefix(choice, "invalid") {
				_, cmd := m.editorFinished(editFinishedMsg{})
				m = cmd().(editSavedMsg).applyTo(m)
				if m.longEdit.choice != "invalid" {
					t.Fatal("invalid frontmatter was not offered for repair")
				}
				key := "r"
				if choice == "invalid-d" {
					key = "d"
				}
				command := m.updateLongEdit(key)
				if key == "r" {
					if command == nil {
						t.Fatal("reopen did not invoke editor")
					}
					if m.longEdit.choice != "" {
						t.Fatal("reopen did not clear prompt")
					}
				} else if m.longEdit.path != "" {
					t.Fatal("discard retained buffer")
				}
				data, err := os.ReadFile(path)
				if err != nil || string(data) != string(initial) {
					t.Fatalf("invalid edit wrote source: %s %v", data, err)
				}
				return
			}
			changed := model.Mutation{Ref: ref, ExpectedVersion: item.Version, Op: model.Set, Field: "title", Value: "Theirs"}
			if err := <-store.Mutate(changed); err != nil {
				t.Fatal(err)
			}
			waitEdit(t, func() bool { i, _, _ := store.Current().Get(ref); return i.Title == "Theirs" })
			_, cmd := m.editorFinished(editFinishedMsg{})
			m = cmd().(editSavedMsg).applyTo(m)
			if m.longEdit.choice != "conflict" {
				t.Fatal("conflict not offered")
			}
			command := m.updateLongEdit(choice)
			switch choice {
			case "y":
				if command == nil {
					t.Fatal("missing version-bound retry")
				}
				m = command().(editSavedMsg).applyTo(m)
				data, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(data), "title: Yours") {
					t.Fatalf("keep yours failed: %s %v", data, err)
				}
			case "t":
				data, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(data), "title: Theirs") {
					t.Fatalf("keep theirs failed: %s %v", data, err)
				}
			case "r":
				if command == nil {
					t.Fatal("missing reopen command")
				}
				prepared := command().(editPreparedMsg)
				if prepared.err != nil {
					t.Fatal(prepared.err)
				}
				data, err := os.ReadFile(buffer)
				if err != nil || !strings.Contains(string(data), "<<<<<<< yours") || !strings.Contains(string(data), "title: Theirs") || !strings.Contains(string(data), "title: Yours") {
					t.Fatalf("reopen lost a version: %s %v", data, err)
				}
				current, _, _ := store.Current().Get(ref)
				if prepared.state.version != current.Version {
					t.Fatal("reopen is not bound to the current version")
				}
				// Quitting with the markers unresolved must neither write them nor
				// delete the only copy of "yours".
				m.longEdit = prepared.state
				_, cmd := m.editorFinished(editFinishedMsg{})
				m = cmd().(editSavedMsg).applyTo(m)
				if m.longEdit.choice != "invalid" {
					t.Fatal("unresolved conflict markers were accepted")
				}
				if _, err := os.Stat(buffer); err != nil {
					t.Fatalf("buffer with yours was removed: %v", err)
				}
				if data, _ := os.ReadFile(path); strings.Contains(string(data), "<<<<<<<") {
					t.Fatal("conflict markers written to the item")
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := format.Parse(data); err != nil {
				t.Fatalf("source invalid: %v", err)
			}
		})
	}
}
func (msg editSavedMsg) applyTo(m Model) Model { next, _ := m.editorSaved(msg); return next.(Model) }

// Notes open with their text and save by replacing the section, so existing
// notes are never appended a second time. An append-only source starts empty.
func TestNotesEditor(t *testing.T) {
	t.Run("replace", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		root := t.TempDir()
		dir := filepath.Join(root, "items")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "BL-1.md")
		initial := "---\nid: BL-1\ntitle: Original\nstatus: todo\ncreated: 2026-10-21T00:00:00Z\nupdated: 2026-10-21T00:00:00Z\n---\n\nBody.\n\n## Notes\n\nFirst note.\n"
		if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
			t.Fatal(err)
		}
		backend, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: dir, CommonDir: filepath.Join(root, ".git"), Statuses: map[string]model.StatusCategory{"todo": model.Todo}})
		if err != nil {
			t.Fatal(err)
		}
		cfg := project.Config{Settings: project.Settings{Statuses: []project.StatusConfig{{Key: "todo", Category: "todo"}}}}
		store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
		defer store.Close()
		waitEdit(t, func() bool { return store.Current().Len() == 1 })
		ref := model.Ref{Source: "tasks", ID: "BL-1"}
		item, _, _ := store.Current().Get(ref)
		m := New(Options{Context: ctx, Store: store, NativeBytes: func(context.Context, model.Item) ([]byte, error) { return os.ReadFile(path) }, NativeSource: func(string) bool { return true }})
		m.data.rows = []screenRow{{row: engine.Row{Ref: ref, Version: item.Version}}}
		m.loading = false
		m.detailOpen, m.detailFocus = true, true
		m = editPress(m, "i")
		t.Cleanup(func() { os.Remove(m.longEdit.path) })
		if buffer, err := os.ReadFile(m.longEdit.path); err != nil || string(buffer) != "First note.\n" {
			t.Fatalf("notes buffer %q %v", buffer, err)
		}
		if err := os.WriteFile(m.longEdit.path, []byte("First note.\n\nSecond note.\n"), 0600); err != nil {
			t.Fatal(err)
		}
		_, cmd := m.editorFinished(editFinishedMsg{})
		if saved := cmd().(editSavedMsg); saved.err != nil || saved.conflict {
			t.Fatalf("save: %+v", saved)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := format.Parse(data)
		if err != nil || doc.Item().Notes != "First note.\n\nSecond note." {
			t.Fatalf("notes after save: %q %v", data, err)
		}
	})
	t.Run("append-only source", func(t *testing.T) {
		m, _ := editFixture(t, []model.Item{editItem("BL-1", "First")}, model.Capabilities{model.AppendNote: true}, Options{})
		m.detailOpen, m.detailFocus = true, true
		m = editPress(m, "i")
		t.Cleanup(func() { os.Remove(m.longEdit.path) })
		if m.longEdit.appendOp != model.AppendNote || len(m.longEdit.original) != 0 || m.longEdit.path == "" {
			t.Fatalf("append-only notes: %+v", m.longEdit)
		}
	})
}
