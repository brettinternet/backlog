//go:build unix

package oauth

import (
	"os"
	"testing"
)

func wantPrivate(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("%s mode: %v %v", path, info, err)
	}
}
