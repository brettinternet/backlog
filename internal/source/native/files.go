package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/brettinternet/backlog/internal/atomicfile"
	"github.com/brettinternet/backlog/internal/filelock"
	"github.com/brettinternet/backlog/internal/source"
)

// The lock file is never renamed: all processes must flock the same inode.
func (s *Source) lock(ctx context.Context) (*os.File, error) {
	if err := os.MkdirAll(s.state, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.state, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = filelock.Lock(ctx, f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func unlock(f *os.File) {
	_ = filelock.Unlock(f)
	_ = f.Close()
}

// Failures before rename preserve the original; after rename a directory-sync
// failure is reported as durability-unknown, since the new bytes are visible.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".bl-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = atomicfile.Rename(f.Name(), path); errors.Is(err, atomicfile.ErrBusy) {
		return source.Refuse("unavailable", err.Error()+"; close it and retry")
	} else if err != nil {
		return err
	}
	return atomicfile.SyncDir(dir)
}
