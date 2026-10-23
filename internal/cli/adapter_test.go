package cli_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/testkit"
)

func TestAdapterCheckProcess(t *testing.T) {
	if len(os.Args) < 2 || !strings.HasPrefix(os.Args[len(os.Args)-1], "adapter-fixture-") {
		return
	}
	mode := strings.TrimPrefix(os.Args[len(os.Args)-1], "adapter-fixture-")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(1)
	}
	var req struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(scanner.Bytes(), &req) != nil {
		os.Exit(1)
	}
	if mode == "oversized" {
		fmt.Println(strings.Repeat("x", 1<<20))
		os.Exit(0)
	}
	major := 1
	caps := []string{"list", "read"}
	if mode == "version" {
		major = 2
	}
	if mode == "capabilities" {
		caps = []string{"list"}
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": major, "manifest": map[string]any{"id": "fixture", "version": "1.0.0", "capabilities": caps}}})
	os.Exit(0)
}

// Catches CLI argument handling or envelope errors that hide conformance
// failures behind a successful exit, including executable arguments after --.
func TestAdapterCheckReportsProtocolFailures(t *testing.T) {
	testkit.Isolate(t)
	for _, tc := range []struct{ mode, want string }{{"oversized", "frame"}, {"version", "version"}, {"capabilities", "capabilit"}} {
		t.Run(tc.mode, func(t *testing.T) {
			code, out, stderr := run("--json", "adapter", "check", "--timeout", "2s", "--", os.Args[0], "-test.run=^TestAdapterCheckProcess$", "--", "adapter-fixture-"+tc.mode)
			if code == 0 || stderr != "" || !strings.Contains(out, `"command":"adapter check"`) || !strings.Contains(strings.ToLower(out), tc.want) {
				t.Fatalf("exit=%d stderr=%q output=%s", code, stderr, out)
			}
		})
	}
}
