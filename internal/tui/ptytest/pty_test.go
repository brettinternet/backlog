//go:build unix

package ptytest_test

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/testkit"
	"github.com/brettinternet/backlog/internal/tui/ptytest"
)

// Regression: terminal renderers send cursor-addressed diffs, not complete
// lines. Searching the raw output alone misses a successfully rendered frame.
func TestIncrementalFrame(t *testing.T) {
	if os.Getenv("BL_TUI_PTY_HELPER") == "1" {
		fmt.Print("value=0")
		var input [1]byte
		if _, err := os.Stdin.Read(input[:]); err != nil {
			os.Exit(2)
		}
		fmt.Print("\x1b[1;7H1")
		return
	}
	testkit.Isolate(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestIncrementalFrame$")
	cmd.Env = append(os.Environ(), "BL_TUI_PTY_HELPER=1")
	session, err := ptytest.Start(cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := session.WaitFor("value=0", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := session.KeyToOutput("x\n", "value=1", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}
