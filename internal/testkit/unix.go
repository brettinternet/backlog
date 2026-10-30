package testkit

import (
	"runtime"
	"testing"
)

// Unix skips a test on Windows, naming the Unix facility it needs.
func Unix(t testing.TB, needs string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs Unix: " + needs)
	}
}
