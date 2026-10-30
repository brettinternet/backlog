//go:build unix

package native_test

import (
	"errors"
	"syscall"
	"testing"
)

// breakWrites lowers the file-size limit: the temp is created and the write
// fails after partial output.
func breakWrites(t *testing.T, _ string) {
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 512, Max: 512}); err != nil {
		t.Fatal(err)
	}
}

func wantWriteFailure(t *testing.T, err error) {
	if !errors.Is(err, syscall.EFBIG) {
		t.Fatalf("expected file-size failure, got %v", err)
	}
}
