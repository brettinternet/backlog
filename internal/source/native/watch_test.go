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
	"github.com/brettinternet/backlog/internal/testkit"
)

// failedWatch is a notification backend that fails after installing, as a
// Windows directory handle does when its directory is deleted.
type failedWatch struct{}

func (failedWatch) Wait(context.Context) error {
	return fmt.Errorf("%w: access denied", native.ErrPollingOnly)
}
func (failedWatch) Close() error { return nil }

// These checks catch per-item descriptor growth, missed in-place writes, and
// a failed open or a failed watch silently disabling external change signals.
// On Windows the native mode has no sweep, so the watcher alone must signal.
func TestDirectoryWatchAndPollingFallback(t *testing.T) {
	for _, mode := range []string{"native", "open-failed", "watch-failed"} {
		t.Run(mode, func(t *testing.T) {
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
			switch mode {
			case "open-failed":
				options.Open = func(string) (native.DirectoryWatch, error) { return nil, syscall.EMFILE }
			case "watch-failed":
				options.Open = func(string) (native.DirectoryWatch, error) { return failedWatch{}, nil }
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
			// Drop signals left by the previous step, such as the fallback's own or a
			// second notification for one write, so each wait observes its operation.
			drain := func() {
				time.Sleep(50 * time.Millisecond)
				for {
					select {
					case <-signals:
					default:
						return
					}
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
				drain()
				if mode == "native" {
					// A watcher that signals while idle would reload constantly.
					select {
					case <-signals:
						t.Fatal("signal without a change")
					case <-time.After(100 * time.Millisecond):
					}
				}
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
			if want := map[bool]int32{true: 1}[mode != "native"]; fallback.Load() != want {
				t.Fatalf("fallback count %d", fallback.Load())
			}
		})
	}
}

// Catches a watch that keeps following its directory after it is moved away
// and replaced, as Windows does, so items in the new directory go unseen.
func TestWatchedDirectoryReplaced(t *testing.T) {
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
	go func() {
		done <- s.WatchWithOptions(ctx, func() {
			select {
			case signals <- struct{}{}:
			default:
			}
		}, native.WatchOptions{SweepInterval: 10 * time.Millisecond, PollInterval: 10 * time.Millisecond})
	}()
	<-signals
	if err := os.Rename(path, filepath.Join(root, "backlog.old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // Let the move's own signal pass.
	select {
	case <-signals:
	default:
	}
	if err := os.WriteFile(filepath.Join(path, "BL-1.md"), []byte("item"), 0644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-signals:
	case err := <-done:
		t.Fatalf("watch ended: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("item in the replaced directory missed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryWatchDescriptorBound(t *testing.T) {
	testkit.Unix(t, "/dev/fd")
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
