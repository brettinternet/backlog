package native_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/fixtures"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

// Opt-in measurement of the production store over the native watcher at 10k,
// without a PTY, so it also runs on Windows. It records external-change latency
// to the snapshot the TUI renders, and idle CPU, goroutines, and heap. It has no
// timing gate; docs/benchmarks.md records results.
//
//	BL_WATCH_MEASURE=1 BL_WATCH_SOAK=1h task cli:test PKG=./internal/source/native -- -run '^TestWatchMeasurement$' -v -count=1 -timeout=75m
func TestWatchMeasurement(t *testing.T) {
	if os.Getenv("BL_WATCH_MEASURE") != "1" {
		return
	}
	soak := os.Getenv("BL_WATCH_SOAK")
	testkit.Isolate(t)
	root := filepath.Join(t.TempDir(), "project")
	if err := fixtures.Generate(root, 10000, 1); err != nil {
		t.Fatal(err)
	}
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := engine.OpenProjectStore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshots, unsubscribe := store.Subscribe()
	defer unsubscribe()
	await := func(done func(*engine.Snapshot) bool) {
		t.Helper()
		timeout := time.After(30 * time.Second)
		for {
			select {
			case s := <-snapshots:
				if done(s) {
					return
				}
			case <-timeout:
				t.Fatal("snapshot deadline")
			}
		}
	}
	await(func(s *engine.Snapshot) bool { return s.Source("tasks").Coverage == "complete" && s.Len() == 10000 })
	dir := filepath.Join(root, "backlog")
	data, err := os.ReadFile(filepath.Join(dir, "BL-1.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := format.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	template := doc.Item()
	serialize := func(id, title string) []byte {
		item := template
		item.Ref.ID, item.Title = id, title
		out, err := format.Serialize(item)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	title := func(id, want string) func(*engine.Snapshot) bool {
		return func(s *engine.Snapshot) bool {
			item, _, ok := s.Get(model.Ref{Source: "tasks", ID: id})
			return ok && item.Title == want
		}
	}
	gone := func(id string) func(*engine.Snapshot) bool {
		return func(s *engine.Snapshot) bool {
			_, _, ok := s.Get(model.Ref{Source: "tasks", ID: id})
			return !ok
		}
	}
	// Each operation's change is fully applied before the next starts.
	operations := []struct {
		name string
		run  func(n int) (func(*engine.Snapshot) bool, error)
	}{
		{"create", func(n int) (func(*engine.Snapshot) bool, error) {
			id := fmt.Sprintf("BL-%d", 20001+n)
			return title(id, "Created"), os.WriteFile(filepath.Join(dir, id+".md"), serialize(id, "Created"), 0644)
		}},
		{"edit-in-place", func(n int) (func(*engine.Snapshot) bool, error) {
			want := fmt.Sprintf("Edited%03d", n)
			return title("BL-1", want), os.WriteFile(filepath.Join(dir, "BL-1.md"), serialize("BL-1", want), 0644)
		}},
		{"atomic-rename", func(n int) (func(*engine.Snapshot) bool, error) {
			want := fmt.Sprintf("Renamed%03d", n)
			temporary := filepath.Join(dir, ".measure.tmp")
			if err := os.WriteFile(temporary, serialize("BL-2", want), 0644); err != nil {
				return nil, err
			}
			return title("BL-2", want), os.Rename(temporary, filepath.Join(dir, "BL-2.md"))
		}},
		{"delete", func(n int) (func(*engine.Snapshot) bool, error) {
			id := fmt.Sprintf("BL-%d", 20001+n)
			return gone(id), os.Remove(filepath.Join(dir, id+".md"))
		}},
	}
	for _, operation := range operations {
		samples := make([]time.Duration, 20)
		for n := range samples {
			start := time.Now()
			done, err := operation.run(n)
			if err != nil {
				t.Fatal(err)
			}
			await(done)
			samples[n] = time.Since(start)
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		t.Logf("%s 10k external %s-to-snapshot n=20 p95=%s", runtime.GOOS, operation.name, samples[(len(samples)*95-1)/100])
	}
	if soak == "" {
		return
	}
	duration, err := time.ParseDuration(soak)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Second) // Let the last refresh settle.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutines, cpu, start := runtime.NumGoroutine(), cpuSeconds(), time.Now()
	time.Sleep(duration)
	used, elapsed := cpuSeconds()-cpu, time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("%s 10k idle duration=%s cpu_seconds=%.3f (%.3f%% of one core) goroutines=%d->%d heap_bytes=%d->%d", runtime.GOOS, elapsed.Round(time.Second), used, 100*used/elapsed.Seconds(), goroutines, runtime.NumGoroutine(), before.HeapAlloc, after.HeapAlloc)
}
