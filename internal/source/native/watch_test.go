package native_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
)

// These checks catch per-item descriptor growth, missed in-place writes, and
// the resource-exhaustion fallback silently disabling external change signals.
func TestDirectoryWatchAndPollingFallback(t *testing.T) {
	for _, polling := range []bool{false, true} {
		t.Run(fmt.Sprint(polling), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "backlog")
			if err := os.Mkdir(path, 0755); err != nil {
				t.Fatal(err)
			}
			s := openSource(t, root, filepath.Join(root, "common"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			signals := make(chan struct{}, 1)
			done := make(chan error, 1)
			var fallback atomic.Int32
			options := native.WatchOptions{SweepInterval: 10 * time.Millisecond, PollInterval: 10 * time.Millisecond, OnPolling: func(error) { fallback.Add(1) }}
			if polling {
				options.Open = func(string) (native.DirectoryWatch, error) { return nil, syscall.EMFILE }
			}
			go func() {
				done <- s.WatchWithOptions(ctx, func() {
					select {
					case signals <- struct{}{}:
					default:
					}
				}, options)
			}()
			waitSignal := func() {
				t.Helper()
				select {
				case <-signals:
				case err := <-done:
					t.Fatalf("watch ended: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("missing signal")
				}
			}
			waitSignal() // Watch installed and baseline initialized.
			file := filepath.Join(path, "BL-1.md")
			for _, operation := range []func() error{
				func() error { return os.WriteFile(file, []byte("first"), 0644) },
				func() error { return os.WriteFile(file, []byte("in-place longer"), 0644) },
				func() error { return os.Rename(file, filepath.Join(path, "BL-2.md")) },
				func() error { return os.Remove(filepath.Join(path, "BL-2.md")) },
			} {
				// Sweep reconciliation, rather than a prior directory hint, must account
				// for the current operation. The last operation is observed via a sweep
				// even on hosts with no directory notification backend.
				if err := operation(); err != nil {
					t.Fatal(err)
				}
				waitSignal()
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("watch leaked on cancellation")
			}
			if polling && fallback.Load() != 1 {
				t.Fatalf("fallback count %d", fallback.Load())
			}
		})
	}
}

func TestDirectoryWatchDescriptorBound(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "backlog")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	for n := range 10_000 {
		if err := os.WriteFile(filepath.Join(path, fmt.Sprintf("BL-%d.md", n+1)), []byte("item"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		t.Helper()
		entries, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	before := count()
	s := openSource(t, root, filepath.Join(root, "common"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- s.Watch(ctx, func() {
			select {
			case ready <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("watch startup deadline")
	}
	during := count()
	if during-before > 3 {
		t.Fatalf("descriptor growth %d for 10k files", during-before)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("watch cancellation deadline")
	}
	after := count()
	if after > before {
		t.Fatalf("leaked %d descriptors", after-before)
	}
	t.Logf("10k directory watch FDs: before=%d during=%d after=%d", before, during, after)
}

// Catches stale reuse after same-size edits, replacements, and a racing read.
func TestWatchedCacheRefresh(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "backlog", "BL-1.md")
	data := fixture(t, "BL-1")
	write(t, path, data)
	write(t, filepath.Join(root, "backlog", "BL-2.md"), fixture(t, "BL-2"))
	var reads atomic.Int64
	var race atomic.Bool
	s := cachedSource(t, root, func(filename string) ([]byte, error) {
		reads.Add(1)
		content, err := os.ReadFile(filename)
		if filename == path && race.Swap(false) {
			write(t, path, bytes.Replace(content, []byte("Modified"), []byte("Replaced"), 1))
		}
		return content, err
	})
	cachedLoad(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- s.Watch(ctx, func() {
			select {
			case ready <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("watch startup deadline")
	}
	reads.Store(0)
	cachedLoad(t, s)
	if reads.Load() != 0 {
		t.Fatal("unchanged watched items reparsed")
	}
	write(t, path, bytes.Replace(data, []byte("Imported"), []byte("Modified"), 1))
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	changed := cachedLoad(t, s)
	if reads.Load() != 1 || changed.Items[0].Title != "Modified" {
		t.Fatalf("missed same-size edit: reads=%d items=%+v", reads.Load(), changed.Items)
	}
	write(t, path, bytes.Replace(data, []byte("Imported"), []byte("Modified"), 1))
	race.Store(true)
	cachedLoad(t, s)
	if got := cachedLoad(t, s); got.Items[0].Title != "Replaced" {
		t.Fatal("racing read retained old bytes under a new identity")
	}
	replacement := filepath.Join(root, "backlog", ".replacement")
	write(t, replacement, data)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if got := cachedLoad(t, s); got.Items[0].Title != "Imported" {
		t.Fatal("atomic replacement missed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := cachedLoad(t, s); len(got.Items) != 1 || got.Items[0].Ref.ID != "BL-2" {
		t.Fatal("deleted item retained")
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := s.Load(canceled, source.Cursor{}); err != context.Canceled {
		t.Fatalf("canceled load: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("watch cancellation deadline")
	}
}
