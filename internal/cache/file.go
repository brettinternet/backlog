package cache

import (
	"os"
	"path/filepath"
)

// WriteFile replaces a file only its owner can read through a temporary file
// and a rename, so a reader never sees half of it. A failure leaves the old
// file; a disposable cache ignores the error, since it is only a shortcut.
func WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cache-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}
