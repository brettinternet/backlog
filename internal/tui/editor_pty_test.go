//go:build unix

package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
	"github.com/brettinternet/backlog/internal/tui/ptytest"
)

// ExecProcess must release and regain terminal ownership: after the real editor
// exits, navigation/help and the exit key must still reach Bubble Tea.
func TestEditorRestoresPTYAndInput(t *testing.T) {
	if os.Getenv("BL_EDITOR_PTY_HELPER") == "1" {
		root := os.Getenv("BL_EDITOR_PTY_ROOT")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		dir := filepath.Join(root, "items")
		backend, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: dir, CommonDir: filepath.Join(root, ".git"), Statuses: map[string]model.StatusCategory{"todo": model.Todo}})
		if err != nil {
			panic(err)
		}
		cfg := project.Config{Settings: project.Settings{Statuses: []project.StatusConfig{{Key: "todo", Category: "todo"}}, Ready: project.ReadyConfig{Statuses: []string{"todo"}}}}
		store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
		defer store.Close()
		projector := engine.NewProjector(ctx, cfg)
		defer projector.Close()
		snapshots, unsubscribe := store.Subscribe()
		defer unsubscribe()
		m := New(Options{Context: ctx, Store: store, Projector: projector, Project: "test", Statuses: cfg.Statuses, NativeSource: func(string) bool { return true }, NativeBytes: func(_ context.Context, _ model.Item) ([]byte, error) {
			return os.ReadFile(filepath.Join(dir, "BL-1.md"))
		}, ValidateItem: func(i model.Item) error { return engine.ValidateItem(cfg, i) }, Read: func(_ context.Context, _ model.Ref) (model.Item, error) { return backend.Read(ctx, "BL-1") }})
		m.Bind(snapshots)
		m.switchView(3) // All open, independent of readiness configuration.
		_, err = tea.NewProgram(m, tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout)).Run()
		if err != nil {
			panic(err)
		}
		fmt.Println("PTY_EDITOR_DONE")
		return
	}
	root := t.TempDir()
	dir := filepath.Join(root, "items")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	initial := []byte("---\nid: BL-1\ntitle: Original\nstatus: todo\ncreated: 2026-10-21T00:00:00Z\nupdated: 2026-10-21T00:00:00Z\n---\n\n## Description\n\nBody.\n")
	if err := os.WriteFile(filepath.Join(dir, "BL-1.md"), initial, 0644); err != nil {
		t.Fatal(err)
	}
	editor := filepath.Join(root, "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'Editor added.\\n' >> \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestEditorRestoresPTYAndInput$")
	cmd.Env = append(os.Environ(), "BL_EDITOR_PTY_HELPER=1", "BL_EDITOR_PTY_ROOT="+root, "EDITOR="+editor, "TERM=xterm-256color")
	session, err := ptytest.Start(cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := session.WaitFor("Original", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := session.Write("j"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.KeyToOutput("e", "Editor saved.", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := session.KeyToOutput("?", "R resync", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := session.KeyToOutput("q", "PTY_EDITOR_DONE", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "BL-1.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("Editor added.")) {
		t.Fatalf("editor content missing: %s", data)
	}
}
