// Package testkit provides test isolation shared by CLI and configuration tests.
package testkit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Isolate redirects all application homes and clears invocation overrides.
// It changes process environment, so callers must not use t.Parallel.
func Isolate(t testing.TB) string {
	t.Helper()
	home := t.TempDir()
	for _, pair := range os.Environ() {
		name, _, _ := strings.Cut(pair, "=")
		// Harness session variables name the agent behind each event, and
		// contact lookup must not reach the developer's live sessions.
		if strings.HasPrefix(name, "BL_") || strings.HasPrefix(name, "WORKLEASE_") || slices.Contains([]string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "PI_SESSION_ID", "CLAUDE_CONFIG_DIR", "HERDR_SOCKET_PATH"}, name) {
			t.Setenv(name, "")
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{
		"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
		"XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS",
	} {
		t.Setenv(name, filepath.Join(home, name))
	}
	return home
}
