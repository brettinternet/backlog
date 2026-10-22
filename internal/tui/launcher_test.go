package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

// Catches go-to excluding archives/closed items, doing I/O on Update, or a
// stale query reopening an item after the user canceled the picker.
func TestGoToWorkerAndDetachedDetail(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	archived := GoToItem{Ref: model.Ref{Source: "tasks", ID: "BL-50000"}, Title: "Archived lighthouse", Status: "done", Version: "archive-v1"}
	m := New(Options{Context: ctx, NoColor: true, GoToItems: func(ctx context.Context) ([]GoToItem, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []GoToItem{archived}, nil
	}})
	m.data = shellFixture(1).data
	m.selected = 1
	m = send(m, "ctrl+p")
	if !m.launcher.open {
		t.Fatal("go-to did not open")
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The load remains blocked, yet input and View stay usable.
	next, _ := m.updateLauncher("", "lighthouse")
	m = next.(Model)
	if !strings.Contains(m.View().Content, "lighthouse") {
		t.Fatal("input blocked by load")
	}
	close(release)
	for m.launcher.loading {
		select {
		case result := <-m.goToWorker.results:
			next, _ = m.Update(result)
			m = next.(Model)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if calls.Load() != 1 || len(m.launcher.rows) != 1 {
		t.Fatal("catalog reloaded per keystroke or archived item missing")
	}
	originalQuery, originalView := m.query, m.view
	next, _ = m.updateLauncher("enter", "")
	m = next.(Model)
	row, ok := m.selectedRow()
	if !ok || row.Ref != archived.Ref || !m.detailOpen || !m.detailFocus || m.query != originalQuery || m.view != originalView {
		t.Fatal("go-to did not open detached archive detail")
	}
	m = send(m, "esc")
	row, _ = m.selectedRow()
	if row.Ref == archived.Ref {
		t.Fatal("archive replaced the current view selection")
	}
	// Old results must not resurrect a canceled launcher.
	next, _ = m.Update(goToResult{serial: m.launcherSerial - 1, rows: []launcherRow{{item: &archived}}})
	m = next.(Model)
	if m.launcher.open {
		t.Fatal("stale result reopened picker")
	}
}

// Catches cache-backed catalog loading dropping archive/ or filtering done.
func TestGoToNativeCatalog(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "backlog", "archive"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ path, id, title, status string }{{"BL-1.md", "BL-1", "Open task", "todo"}, {"BL-2.md", "BL-2", "Closed task", "done"}, {"archive/BL-3.md", "BL-3", "Archived lighthouse", "done"}} {
		data := fmt.Sprintf("---\nid: %s\ntitle: %s\nstatus: %s\ncreated: 2026-01-01T00:00:00Z\nupdated: 2026-01-01T00:00:00Z\n---\n", entry.id, entry.title, entry.status)
		if err := os.WriteFile(filepath.Join(root, "backlog", entry.path), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := project.Config{Settings: project.Settings{Sources: []project.SourceConfig{{ID: "tasks", Type: "native", Path: filepath.Join(root, "backlog"), Prefix: "BL"}}}}
	cfg.Project.ID = "test"
	cfg.Paths.CacheDir = cache
	for range 2 {
		rows, err := engine.GoToItems(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 {
			t.Fatalf("active/closed/archive coverage: %d", len(rows))
		}
		found := searchGoTo(context.Background(), rows, "arc light")
		if len(found) != 1 || found[0].item.Ref.ID != "BL-3" {
			t.Fatalf("fuzzy archive lookup: %+v", found)
		}
	}
}

// BenchmarkGoTo50k reports input-to-results latency per keystroke over the
// immutable ID/title catalog. No absolute timing assertion in normal CI.
func BenchmarkGoTo50k(b *testing.B) {
	// The immutable catalog is the worker's cache-backed input. Native cache
	// coverage (including archive/) is verified by TestGoToNativeCatalog;
	// this benchmark isolates per-keystroke latency, not initial cache building.
	items := make([]GoToItem, 50000)
	for i := range items {
		items[i] = GoToItem{Ref: model.Ref{Source: "tasks", ID: fmt.Sprintf("BL-%d", i+1)}, Title: fmt.Sprintf("Task %d lighthouse", i+1), Status: "done", Version: "1"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := New(Options{Context: ctx, NoColor: true, GoToItems: func(context.Context) ([]GoToItem, error) { return items, nil }})
	m = send(m, "ctrl+p")
	next, _ := m.Update(m.waitGoTo()())
	m = next.(Model)
	samples := map[string][]time.Duration{}
	b.ResetTimer()
	for b.Loop() {
		m.launcher.query = ""
		for _, key := range strings.Split("B L - 5 0 0 0 0", " ") {
			started := time.Now()
			next, _ = m.Update(tea.KeyPressMsg{Code: []rune(key)[0], Text: key})
			m = next.(Model)
			next, _ = m.Update(m.waitGoTo()())
			m = next.(Model)
			_ = m.View()
			samples[m.launcher.query] = append(samples[m.launcher.query], time.Since(started))
		}
		if len(m.launcher.rows) == 0 || m.launcher.rows[0].item.Ref.ID != "BL-50000" {
			b.Fatal("archived target missing")
		}
	}
	b.StopTimer()
	for query, times := range samples {
		slices.Sort(times)
		b.ReportMetric(float64(times[(len(times)*95-1)/100].Nanoseconds())/1e6, query+"-p95-ms")
	}
}
