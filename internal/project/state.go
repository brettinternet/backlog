package project

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func UIStatePath(cfg Config) string {
	key := sha256.Sum256([]byte(cfg.Project.ID))
	return filepath.Join(cfg.Paths.StateDir, "ui", fmt.Sprintf("%x.json", key[:]))
}
func ReadUIState(cfg Config) ([]byte, error) {
	f, err := os.Open(UIStatePath(cfg))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("UI state exceeds 1 MiB")
	}
	return data, nil
}
func WriteUIState(cfg Config, data []byte) error { return writeAtomic(UIStatePath(cfg), data, 0600) }

// WriteStateFile replaces a private file in the state directory atomically.
func WriteStateFile(path string, data []byte) error { return writeAtomic(path, data, 0600) }
