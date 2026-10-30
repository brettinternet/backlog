// Package filelock provides advisory, whole-file, exclusive inter-process
// locks: flock on Unix and LockFileEx on Windows. A lock belongs to the open
// file and ends when it is unlocked or closed.
package filelock

import (
	"context"
	"os"
	"time"
)

// Lock waits for f's exclusive lock until ctx ends, polling so that
// cancellation is observed promptly.
func Lock(ctx context.Context, f *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := TryLock(f)
		if err != nil || ok {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// TryLock takes f's exclusive lock if it is free and reports whether it did.
func TryLock(f *os.File) (bool, error) { return tryLock(f) }

// Unlock releases f's lock; closing f releases it too.
func Unlock(f *os.File) error { return unlock(f) }

// LockDir holds an exclusive lock for dir that leaves no file inside it, and
// returns the function that releases it.
func LockDir(ctx context.Context, dir string) (func(), error) { return lockDir(ctx, dir) }
