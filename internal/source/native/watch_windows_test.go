package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A watch that can't open must fall back to polling rather than end the watch.
func TestWindowsWatchOpenFailurePolls(t *testing.T) {
	_, err := openDirectoryWatch(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, errPollingOnly) {
		t.Fatalf("open error %v does not fall back to polling", err)
	}
}

// Catches an overflowed notification buffer being dropped or ending the watch:
// it must still signal so the reload rescans. 5,000 creates queue at least
// 5,000 32-byte records, past the 64 KiB buffer, while nothing is waiting.
func TestWindowsWatchOverflowSignals(t *testing.T) {
	dir := t.TempDir()
	w, err := openDirectoryWatch(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close() //nolint:errcheck
	for n := range 5_000 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("BL-%d.md", n+1)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Wait(ctx); err != nil {
		t.Fatalf("overflow: %v", err)
	}
	// The watch stays usable after an overflow.
	if err := os.Remove(filepath.Join(dir, "BL-1.md")); err != nil {
		t.Fatal(err)
	}
	if err := w.Wait(ctx); err != nil {
		t.Fatalf("after overflow: %v", err)
	}
	idle, stop := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, stop)
	err = w.Wait(idle)
	for err == nil { // Drain the remaining notifications.
		err = w.Wait(idle)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
