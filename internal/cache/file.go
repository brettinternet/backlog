package cache

import (
	"os"
	"path/filepath"
)

// WriteFile replaces a disposable cache file through a temporary file and a
// rename, so a reader never sees half of it. A failure leaves the old file,
// since the cache is only ever a shortcut.
func WriteFile(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cache-*")
	if err != nil {
		return
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
}
