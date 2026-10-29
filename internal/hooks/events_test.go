package hooks_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/brettinternet/backlog/internal/hooks"
)

// Catches interleaved lines, duplicate dispatch ownership, and lost/duplicated
// cursor replay when concurrent appenders rotate the current segment.
func TestEventConcurrentRotation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ctx := context.Background()
	type child struct {
		cmd    *exec.Cmd
		output bytes.Buffer
	}
	children := make([]*child, 8)
	for n := range children {
		c := &child{cmd: exec.Command(os.Args[0], "-test.run=^TestEventAppendProcess$")}
		c.cmd.Env = append(os.Environ(), "BL_APPEND_ROOT="+root, "BL_APPEND_WORKER="+strconv.Itoa(n))
		c.cmd.Stdout = &c.output
		c.cmd.Stderr = &c.output
		if err := c.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children[n] = c
	}
	for _, c := range children {
		if err := c.cmd.Wait(); err != nil {
			t.Fatalf("append child: %v %s", err, c.output.String())
		}
	}
	log := hooks.Log{Dir: root, MaxBytes: 900}
	events, err := log.Read(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 81 {
		t.Fatalf("want 80 unique events plus one shared event, got %d", len(events))
	}
	for n, e := range events {
		if len(e.ID) != 26 || n > 0 && e.ID <= events[n-1].ID {
			t.Fatalf("unordered ULID: %s", e.ID)
		}
		replay, err := log.Read(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(replay) != len(events)-n-1 {
			t.Fatalf("cursor gap at %d: %d", n, len(replay))
		}
		for k, r := range replay {
			if r.ID != events[n+k+1].ID {
				t.Fatalf("cursor duplicate/gap at %d", n)
			}
		}
	}
	paths, _ := filepath.Glob(filepath.Join(root, "*.jsonl"))
	if len(paths) < 2 {
		t.Fatal("test did not rotate")
	}
	if _, err = log.Read(ctx, "unknown"); err == nil {
		t.Fatal("unknown cursor silently skipped history")
	}
}

// Catches a killed producer leaving a partial line or losing its derived index.
func TestEventInterruptedAppend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := hooks.Log{Dir: t.TempDir()}
	appendEvent := func(version string) {
		t.Helper()
		if err := log.Update(ctx, func(tx *hooks.Transaction) error {
			_, err := tx.Append(hooks.Event{Type: "item.updated", Ref: "tasks:BL-1", Versions: []string{version}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("one")
	path := filepath.Join(log.Dir, "current.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"id":"interrupted`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	appendEvent("two")
	// An index is not a second dedupe authority: rebuild entirely from JSONL.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err = os.Remove(filepath.Join(log.Dir, "index.sqlite") + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if err = log.Update(ctx, func(tx *hooks.Transaction) error {
		owned, err := tx.Append(hooks.Event{Type: "item.updated", Ref: "tasks:BL-1", Versions: []string{"one"}})
		if owned {
			t.Fatal("lost dedupe after index rebuild")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	events, err := log.Read(ctx, "")
	if err != nil || len(events) != 2 {
		t.Fatalf("interrupted append: %d %v", len(events), err)
	}
	// An index from before the per-item ref column records every file as
	// indexed, so keeping its file rows would leave per-item reads empty.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err = os.Remove(filepath.Join(log.Dir, "index.sqlite") + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(log.Dir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE events(id TEXT PRIMARY KEY, identity TEXT NOT NULL UNIQUE, data BLOB NOT NULL); CREATE TABLE files(path TEXT PRIMARY KEY, size INTEGER NOT NULL, modified INTEGER NOT NULL); INSERT INTO files VALUES(?,?,?)`, path, info.Size(), info.ModTime().UnixNano())
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if events, err = log.ItemEvents(ctx, "tasks:BL-1"); err != nil || len(events) != 2 {
		t.Fatalf("old index: %d %v", len(events), err)
	}
}

func TestEventAppendProcess(t *testing.T) {
	root := os.Getenv("BL_APPEND_ROOT")
	if root == "" {
		return
	}
	worker := os.Getenv("BL_APPEND_WORKER")
	log := hooks.Log{Dir: root, MaxBytes: 900}
	for n := 0; n < 10; n++ {
		err := log.Update(context.Background(), func(tx *hooks.Transaction) error {
			e := hooks.Event{Type: "item.updated", Ref: "tasks:BL-1", Origin: "cli", Versions: []string{worker, fmt.Sprint(n)}}
			if appended, err := tx.Append(e); err != nil {
				return err
			} else if !appended {
				t.Fatal("unique event deduplicated")
			}
			shared := hooks.Event{Type: "item.updated", Ref: "tasks:BL-2", Origin: "sync", Versions: []string{"v1", "v2"}}
			_, err := tx.Append(shared)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
