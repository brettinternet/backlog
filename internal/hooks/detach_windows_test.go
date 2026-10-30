package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestDetachedConsoleProcess(t *testing.T) {
	if marker := os.Getenv("BL_DETACHED_MARKER"); marker != "" {
		_, err := windows.GetConsoleCP()
		_ = os.WriteFile(marker, []byte(map[bool]string{true: "console", false: "none"}[err == nil]), 0o600)
	}
}

// A runner attached to the invoking console dies when that console closes.
func TestDetachedRunnerHasNoConsole(t *testing.T) {
	if _, err := windows.GetConsoleCP(); err != nil {
		t.Skip("the test process has no console to detach from")
	}
	marker := filepath.Join(t.TempDir(), "marker")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDetachedConsoleProcess$")
	cmd.Env = append(os.Environ(), "BL_DETACHED_MARKER="+marker)
	if err := startDetached(cmd); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil && len(data) > 0 {
			if string(data) != "none" {
				t.Fatalf("detached runner shares a console: %s", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("detached runner did not report")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
