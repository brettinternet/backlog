package engine_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
)

func storeConfig() project.Config {
	return project.Config{Settings: project.Settings{Statuses: []project.StatusConfig{{Key: "todo", Category: "todo"}, {Key: "done", Category: "done"}}, Ready: project.ReadyConfig{Statuses: []string{"todo"}, SatisfiedBy: []string{"done"}}}}
}
func awaitSnapshot(t testing.TB, s *engine.Store, predicate func(*engine.Snapshot) bool) *engine.Snapshot {
	t.Helper()
	ch, unsubscribe := s.Subscribe()
	defer unsubscribe()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case snapshot := <-ch:
			if predicate(snapshot) {
				return snapshot
			}
		case <-timer.C:
			t.Fatal("snapshot deadline")
			return nil
		}
	}
}
func awaitResult(t testing.TB, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("mutation deadline")
		return nil
	}
}
func requireSuccess(t testing.TB, ch <-chan error) {
	t.Helper()
	if err := awaitResult(t, ch); err != nil {
		t.Fatal(err)
	}
}
func change(i model.Item, title string) model.Mutation {
	return model.Mutation{Ref: i.Ref, ExpectedVersion: i.Version, Op: model.Set, Field: "title", Value: title}
}

// This fake owns real codec bytes; only source latency/failure is controlled.
// It catches optimistic state being published too late or rollback losing data.
type controlledSource struct {
	mu       sync.Mutex
	doc      *format.Document
	id       string
	gate     <-chan struct{}
	loadGate <-chan struct{}
	failure  error
	coverage source.Coverage
	empty    bool
	calls    atomic.Int32
}

func fakeSource(t *testing.T, id string) *controlledSource {
	t.Helper()
	data, err := format.Serialize(model.Item{Ref: model.Ref{ID: "BL-1"}, Title: "Original", Status: "todo", Labels: []string{"original"}, Fields: map[string]any{"nested": map[string]any{"value": "original"}}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := format.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return &controlledSource{id: id, doc: doc, coverage: source.Complete}
}
func (s *controlledSource) Info() source.Info {
	return source.Info{ID: s.id, Capabilities: model.Capabilities{model.Set: true, model.Restore: true}}
}
func (s *controlledSource) current() model.Item {
	i := s.doc.Item()
	i.Ref.Source = s.id
	i.Category = model.Todo
	return i
}
func (s *controlledSource) Load(ctx context.Context, _ source.Cursor) (source.Delta, error) {
	if s.loadGate != nil {
		select {
		case <-s.loadGate:
		case <-ctx.Done():
			return source.Delta{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delta := source.Delta{Coverage: s.coverage}
	if !s.empty {
		delta.Items = []model.Item{s.current()}
	}
	return delta, nil
}
func (s *controlledSource) Read(context.Context, string) (model.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current(), nil
}
func (s *controlledSource) Prepare(_ context.Context, m model.Mutation) (source.Prepared, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, inverse, err := s.doc.Apply(m, time.Now())
	if err != nil {
		return source.Prepared{}, err
	}
	i := next.Item()
	i.Ref.Source = s.id
	return source.Prepared{Item: i, Inverse: inverse}, nil
}
func (s *controlledSource) Apply(ctx context.Context, m model.Mutation) (model.Item, error) {
	s.calls.Add(1)
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return model.Item{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return model.Item{}, s.failure
	}
	next, _, err := s.doc.Apply(m, time.Now())
	if err != nil {
		return model.Item{}, source.Refuse("conflict", err.Error())
	}
	s.doc = next
	return s.current(), nil
}
func TestOptimisticFailureAndConflict(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "failure", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			backend := fakeSource(t, "tasks")
			gate := make(chan struct{})
			backend.gate = gate
			s := engine.OpenStore(context.Background(), storeConfig(), source.Registry{"tasks": backend})
			defer s.Close()
			initial := awaitSnapshot(t, s, func(s *engine.Snapshot) bool { return s.Len() == 1 })
			ref := model.Ref{Source: "tasks", ID: "BL-1"}
			before, _, _ := initial.Get(ref)
			// Caller-owned collections cannot mutate a published snapshot.
			before.Labels[0] = "corrupt"
			before.Fields["nested"].(map[string]any)["value"] = "corrupt"
			before, _, _ = initial.Get(ref)
			if before.Labels[0] != "original" || before.Fields["nested"].(map[string]any)["value"] != "original" {
				t.Fatal("snapshot aliases caller memory")
			}
			result := s.Mutate(change(before, "Local"))
			pending := awaitSnapshot(t, s, func(s *engine.Snapshot) bool { i, e, _ := s.Get(ref); return i.Title == "Local" && e.Pending })
			if _, e, _ := pending.Get(ref); !e.Pending {
				t.Fatal("missing pending")
			}
			if err := awaitResult(t, s.Mutate(change(before, "Overlap"))); err == nil {
				t.Fatal("overlap accepted")
			}
			backend.mu.Lock()
			switch mode {
			case "failure":
				backend.failure = errors.New("disk full")
			case "conflict":
				next, _, err := backend.doc.Apply(change(before, "Remote"), time.Now())
				if err != nil {
					t.Fatal(err)
				}
				backend.doc = next
				backend.failure = source.Refuse("conflict", "external edit")
			}
			backend.mu.Unlock()
			close(gate)
			err := awaitResult(t, result)
			if (err != nil) != (mode != "success") {
				t.Fatalf("outcome: %v", err)
			}
			after := s.Current()
			i, e, _ := after.Get(ref)
			if e.Pending {
				t.Fatal("pending after completion")
			}
			switch mode {
			case "success":
				if i.Title != "Local" || e.Error != "" {
					t.Fatalf("success: %+v %+v", i, e)
				}
			case "failure":
				if i.Title != "Original" || !strings.Contains(e.Error, "disk full") {
					t.Fatalf("rollback: %+v %+v", i, e)
				}
			case "conflict":
				if i.Title != "Remote" || e.Local == nil || e.Local.Title != "Local" || e.Remote == nil || e.Remote.Title != "Remote" {
					t.Fatalf("conflict: %+v %+v", i, e)
				}
			}
			if backend.calls.Load() != 1 {
				t.Fatal("automatic retry")
			}
			old, oldEdit, _ := initial.Get(ref)
			if old.Title != "Original" || oldEdit.Pending || oldEdit.Error != "" {
				t.Fatal("old snapshot changed")
			}
		})
	}
}

func TestSourceIsolationCoverageAndBlockedSubscriber(t *testing.T) {
	t.Parallel()
	slow := fakeSource(t, "slow")
	slow.loadGate = make(chan struct{})
	fast := fakeSource(t, "fast")
	s := engine.OpenStore(context.Background(), storeConfig(), source.Registry{"slow": slow, "fast": fast})
	defer s.Close()
	blocked, unsubscribe := s.Subscribe()
	defer unsubscribe() // Intentionally never drain while workers run.
	awaitSnapshot(t, s, func(s *engine.Snapshot) bool { return s.Source("fast").Coverage == source.Complete })
	ref := model.Ref{Source: "fast", ID: "BL-1"}
	for n := range 20 {
		i, _, _ := s.Current().Get(ref)
		requireSuccess(t, s.Mutate(change(i, fmt.Sprintf("Edit %d", n))))
	}
	select {
	case newest := <-blocked:
		i, _, _ := newest.Get(ref)
		if i.Title != "Edit 19" {
			t.Fatal("mailbox retained stale value")
		}
	default:
		t.Fatal("no latest value")
	}
	fast.mu.Lock()
	fast.empty = true
	fast.coverage = source.Partial
	fast.mu.Unlock()
	s.Refresh()
	awaitSnapshot(t, s, func(s *engine.Snapshot) bool { return s.Source("fast").Coverage == source.Partial })
	if _, _, ok := s.Current().Get(ref); !ok {
		t.Fatal("partial coverage deleted item")
	}
	fast.mu.Lock()
	fast.coverage = source.Complete
	fast.mu.Unlock()
	s.Refresh()
	awaitSnapshot(t, s, func(s *engine.Snapshot) bool { _, _, ok := s.Get(ref); return !ok })
}

func nativeStore(t testing.TB) (*engine.Store, *native.Source, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "backlog")
	backend, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: path, CommonDir: filepath.Join(root, "common"), Statuses: map[string]model.StatusCategory{"todo": model.Todo, "done": model.Done}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	// CRLF, comments, noncanonical spacing and unknown fields must survive undo.
	data := []byte("---\r\nid: BL-1\r\ntitle: Original # keep\r\nstatus: todo\r\ncreated: 2026-10-21T00:00:00Z\r\nupdated: 2026-10-21T00:00:00Z\r\ncustom: [one, two] # untouched\r\n---\r\n\r\n## Description\r\n\r\nA body.\r\n")
	file := filepath.Join(path, "BL-1.md")
	if err := os.WriteFile(file, data, 0644); err != nil {
		t.Fatal(err)
	}
	s := engine.OpenStore(context.Background(), storeConfig(), source.Registry{"tasks": backend})
	t.Cleanup(s.Close)
	awaitSnapshot(t, s, func(s *engine.Snapshot) bool { return s.Len() == 1 })
	return s, backend, file
}
func withoutUpdated(data []byte) []byte {
	lines := bytes.SplitAfter(data, []byte("\n"))
	var out []byte
	for _, line := range lines {
		if !bytes.HasPrefix(line, []byte("updated:")) {
			out = append(out, line...)
		}
	}
	return out
}
func TestNativeUndoRedoAndExternalHistoryGap(t *testing.T) {
	t.Parallel()
	s, backend, path := nativeStore(t)
	ref := model.Ref{Source: "tasks", ID: "BL-1"}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	i, _, _ := s.Current().Get(ref)
	requireSuccess(t, s.Mutate(change(i, "First")))
	first, _ := os.ReadFile(path)
	i, _, _ = s.Current().Get(ref)
	requireSuccess(t, s.Mutate(model.Mutation{Ref: ref, ExpectedVersion: i.Version, Op: model.AppendNote, Text: "second"}))
	second, _ := os.ReadFile(path)
	for _, step := range []struct {
		run  func() <-chan error
		want []byte
	}{{s.Undo, first}, {s.Undo, original}, {s.Redo, first}, {s.Redo, second}} {
		requireSuccess(t, step.run())
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(withoutUpdated(got), withoutUpdated(step.want)) {
			t.Fatalf("byte restoration: %v\n%s\nwant\n%s", err, got, step.want)
		}
	}
	// An external edit between session edits is a hard boundary, not permission
	// for a second undo to restore bytes from before that external edit.
	i, _, _ = s.Current().Get(ref)
	remote, err := backend.Apply(context.Background(), change(i, "External"))
	if err != nil {
		t.Fatal(err)
	}
	s.Refresh()
	awaitSnapshot(t, s, func(s *engine.Snapshot) bool { i, _, _ := s.Get(ref); return i.Version == remote.Version })
	requireSuccess(t, s.Mutate(change(remote, "After external")))
	requireSuccess(t, s.Undo())
	if err := awaitResult(t, s.Undo()); err == nil {
		t.Fatal("undo crossed external history gap")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Contains(got, []byte("External")) {
		t.Fatal("external bytes overwritten")
	}
}

type slowPreparation struct {
	*native.Source
	gate <-chan struct{}
}

func (s slowPreparation) Prepare(ctx context.Context, m model.Mutation) (source.Prepared, error) {
	select {
	case <-s.gate:
		return s.Source.Prepare(ctx, m)
	case <-ctx.Done():
		return source.Prepared{}, ctx.Err()
	}
}

// A serial source must not hold the second item's optimistic update behind the
// first item's I/O, including preparation reads rather than just writes.
func TestQueuedEditsArePendingBeforeSourceIO(t *testing.T) {
	t.Parallel()
	initial, backend, _ := nativeStore(t)
	initial.Close()
	second, err := backend.Apply(context.Background(), model.Mutation{Op: model.Create, Item: &model.Item{Title: "Second", Status: "todo"}})
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	s := engine.OpenStore(context.Background(), storeConfig(), source.Registry{"tasks": slowPreparation{backend, gate}})
	defer s.Close()
	awaitSnapshot(t, s, func(s *engine.Snapshot) bool { return s.Len() == 2 })
	first, _, _ := s.Current().Get(model.Ref{Source: "tasks", ID: "BL-1"})
	a, b := s.Mutate(change(first, "First pending")), s.Mutate(change(second, "Second pending"))
	awaitSnapshot(t, s, func(s *engine.Snapshot) bool {
		_, a, _ := s.Get(first.Ref)
		_, b, _ := s.Get(second.Ref)
		return a.Pending && b.Pending
	})
	close(gate)
	requireSuccess(t, a)
	requireSuccess(t, b)
}
func TestCreateArchiveUndoAndShutdown(t *testing.T) {
	t.Parallel()
	s, nativeBackend, _ := nativeStore(t)
	requireSuccess(t, s.Mutate(model.Mutation{Ref: model.Ref{Source: "tasks"}, Op: model.Create, Item: &model.Item{Title: "New", Status: "todo"}}))
	requireSuccess(t, s.Undo())
	requireSuccess(t, s.Redo())
	ref := model.Ref{Source: "tasks", ID: "BL-2"}
	i, _, ok := s.Current().Get(ref)
	if !ok || i.Title != "New" {
		t.Fatal("create redo missing")
	}
	requireSuccess(t, s.Mutate(model.Mutation{Ref: ref, ExpectedVersion: i.Version, Op: model.Archive}))
	requireSuccess(t, s.Undo())
	// An external unarchive keeps the version, so the reload must still clear the
	// session's archived mark or the item stays hidden from every projection.
	i, _, _ = s.Current().Get(ref)
	requireSuccess(t, s.Mutate(model.Mutation{Ref: ref, ExpectedVersion: i.Version, Op: model.Archive}))
	i, _, _ = s.Current().Get(ref)
	if _, err := nativeBackend.Apply(context.Background(), model.Mutation{Ref: ref, ExpectedVersion: i.Version, Op: model.Unarchive}); err != nil {
		t.Fatal(err)
	}
	s.Refresh()
	p := engine.NewProjector(context.Background(), storeConfig())
	defer p.Close()
	deadline := time.After(5 * time.Second)
	for visible := false; !visible; {
		p.Request(s.Current(), "id:BL-2")
		select {
		case result := <-p.Results():
			visible = len(result.Groups) == 1 && len(result.Groups[0].Rows) == 1
		case <-deadline:
			t.Fatal("externally unarchived item stays hidden")
		}
	}
	backend := fakeSource(t, "blocked")
	backend.gate = make(chan struct{})
	blocked := engine.OpenStore(context.Background(), storeConfig(), source.Registry{"blocked": backend})
	awaitSnapshot(t, blocked, func(s *engine.Snapshot) bool { return s.Len() == 1 })
	before, _, _ := blocked.Current().Get(model.Ref{Source: "blocked", ID: "BL-1"})
	result := blocked.Mutate(change(before, "Pending"))
	blocked.Close()
	if !errors.Is(awaitResult(t, result), context.Canceled) {
		t.Fatal("shutdown stranded request")
	}
}

// A version mismatch against the current snapshot is a conflict before source
// I/O; it must still carry the displayed and current versions for the UI.
func TestDisplayedVersionConflictRetainsBothVersions(t *testing.T) {
	t.Parallel()
	backend := fakeSource(t, "tasks")
	store := engine.OpenStore(context.Background(), storeConfig(), source.Registry{"tasks": backend})
	defer store.Close()
	ref := model.Ref{Source: "tasks", ID: "BL-1"}
	initial := awaitSnapshot(t, store, func(s *engine.Snapshot) bool { return s.Len() == 1 })
	old, _, _ := initial.Get(ref)
	backend.mu.Lock()
	remote, _, err := backend.doc.Apply(change(old, "Remote"), time.Now())
	if err != nil {
		backend.mu.Unlock()
		t.Fatal(err)
	}
	backend.doc = remote
	backend.mu.Unlock()
	store.Refresh()
	current := awaitSnapshot(t, store, func(s *engine.Snapshot) bool {
		item, _, ok := s.Get(ref)
		return ok && item.Title == "Remote"
	})
	latest, _, _ := current.Get(ref)
	if latest.Version == old.Version {
		t.Fatal("external edit did not change the version")
	}
	if err := awaitResult(t, store.Mutate(change(old, "Local"))); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("stale write result: %v", err)
	}
	item, edit, _ := store.Current().Get(ref)
	if item.Title != "Remote" || edit.Local == nil || edit.Remote == nil || edit.Local.Version != old.Version || edit.Remote.Version != latest.Version {
		t.Fatalf("conflict detail did not retain both versions: item=%+v edit=%+v", item, edit)
	}
	if backend.calls.Load() != 0 {
		t.Fatal("preflight conflict reached the source")
	}
}
