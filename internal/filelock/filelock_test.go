package filelock_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/filelock"
)

// holder takes a lock in another process and holds it until stdin closes.
func holder(t *testing.T, mode, path string) io.Closer {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHolderProcess$")
	cmd.Env = append(os.Environ(), "BL_FILELOCK_MODE="+mode, "BL_FILELOCK_PATH="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("holder: %q %v", line, err)
	}
	return stdin
}

func TestHolderProcess(t *testing.T) {
	path := os.Getenv("BL_FILELOCK_PATH")
	if path == "" {
		return
	}
	if os.Getenv("BL_FILELOCK_MODE") == "dir" {
		unlock, err := filelock.LockDir(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
	} else {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err = filelock.Lock(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = os.Stdout.WriteString("locked\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// Regression: every write, event log, autocommit and view lock goes through
// this package; a lock that doesn't exclude another process loses updates.
func TestLocksExcludeOtherProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	release := holder(t, "file", path)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if ok, err := filelock.TryLock(f); ok || err != nil {
		t.Fatalf("try-lock of a held file: %v %v", ok, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err = filelock.Lock(ctx, f); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock of a held file: %v", err)
	}
	_ = release.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = filelock.Lock(ctx, f); err != nil {
		t.Fatalf("lock after release: %v", err)
	}

	dir := t.TempDir()
	release = holder(t, "dir", dir)
	short, cancelShort := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelShort()
	if _, err = filelock.LockDir(short, dir); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dir lock while held: %v", err)
	}
	_ = release.Close()
	unlock, err := filelock.LockDir(ctx, dir)
	if err != nil {
		t.Fatalf("dir lock after release: %v", err)
	}
	unlock()
}
