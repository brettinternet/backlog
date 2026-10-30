package filelock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Lock every byte, as flock does; lock files hold no data, so the mandatory
// byte-range semantics never block a reader.
const allBytes = ^uint32(0)

func tryLock(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, allBytes, allBytes, new(windows.Overlapped))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	default:
		return false, err
	}
}

func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, allBytes, allBytes, new(windows.Overlapped))
}

// Windows cannot lock a directory handle, so the lock is a file in the
// per-user temporary directory named by the directory's case-folded path.
func lockDir(ctx context.Context, dir string) (func(), error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(abs))))
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "bl-dir-"+hex.EncodeToString(sum[:16])+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = Lock(ctx, f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = unlock(f); _ = f.Close() }, nil
}
