package backlogmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/brettinternet/backlog/internal/source"
)

// Share the repository lock with the native source; never rename its inode.
func (s *Source) lock(ctx context.Context) (*os.File, error) {
	if err := os.MkdirAll(s.state, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(s.state, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func unlock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}
func syncDir(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".bl-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (s *Source) ensureDir(dir string) error {
	if !within(s.root, dir) {
		return source.Refuse("invalid-path", "destination escapes backlog directory")
	}
	if dir != s.root {
		if err := s.ensureDir(filepath.Dir(dir)); err != nil {
			return err
		}
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return os.Mkdir(dir, 0755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return source.Refuse("invalid-path", "destination is not a regular directory")
	}
	return nil
}

type fileChange struct {
	path, target string
	before, data []byte
	mode         os.FileMode
}

// Like native renumber, a multi-file move validates every input before the
// first write and rolls back already-published files on a reported failure.
// Each individual replacement is atomic; external writers ignoring the lock
// still require content checks immediately before publication.
func (s *Source) publish(changes []fileChange) error {
	for _, change := range changes {
		if err := s.ensureDir(filepath.Dir(change.target)); err != nil {
			return err
		}
		if change.path != "" {
			current, err := os.ReadFile(change.path)
			if err != nil || contentVersion(current) != contentVersion(change.before) {
				return source.Refuse("conflict", "file changed before publication")
			}
		}
		if change.path != change.target {
			if _, err := os.Lstat(change.target); !errors.Is(err, os.ErrNotExist) {
				return source.Refuse("conflict", "destination exists or is inaccessible")
			}
		}
	}
	applied := 0
	rollback := func(cause error) error {
		var rollbackErrors []error
		for i := applied - 1; i >= 0; i-- {
			change := changes[i]
			if change.path != "" {
				if err := atomicWrite(change.path, change.before, change.mode); err != nil {
					rollbackErrors = append(rollbackErrors, err)
					continue
				}
			}
			if change.path != change.target {
				if err := os.Remove(change.target); err != nil && !errors.Is(err, os.ErrNotExist) {
					rollbackErrors = append(rollbackErrors, err)
				} else if err := syncDir(filepath.Dir(change.target)); err != nil {
					rollbackErrors = append(rollbackErrors, err)
				}
			}
		}
		return errors.Join(append([]error{cause}, rollbackErrors...)...)
	}
	for _, change := range changes {
		// Count the attempted write too: fsync may fail after rename.
		applied++
		if err := atomicWrite(change.target, change.data, change.mode); err != nil {
			return rollback(err)
		}
		if change.path != "" && change.path != change.target {
			if err := os.Remove(change.path); err != nil {
				return rollback(err)
			}
			if err := syncDir(filepath.Dir(change.path)); err != nil {
				return rollback(err)
			}
		}
	}
	return nil
}
