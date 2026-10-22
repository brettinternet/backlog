package native_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

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
