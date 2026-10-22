package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/testkit"
)

// Catches dry-run writes, source modification, and destructive imports into occupied targets.
func TestImportDryRunAndDestinationRefusal(t *testing.T) {
	testkit.Isolate(t)
	root := t.TempDir()
	source := filepath.Join(root, "backlog")
	if err := os.MkdirAll(filepath.Join(source, "tasks"), 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(source, "config.yml"), "project_name: Example\nstatuses: [To Do, In Progress, Done]\ntask_prefix: task\n")
	input := "---\nid: task-01.2\ntitle: Preserve me\nstatus: To Do\ncreated_date: 2026-10-21 12:00\nupdated_date: 2026-10-22 12:00\n---\n\n## Description\n\n<!-- SECTION:DESCRIPTION:BEGIN -->\nBody.\n<!-- SECTION:DESCRIPTION:END -->\n"
	sourceFile := filepath.Join(source, "tasks", "task-01.2 - Preserve.md")
	put(t, sourceFile, input)
	target := filepath.Join(root, "imported")
	code, out, stderr := writeRun(root, "", "import", "backlog-md", "backlog", "--to", "imported", "--dry-run")
	if code != 0 || stderr != "" || !strings.Contains(out, `"command":"import backlog-md"`) || !strings.Contains(out, "TASK-01.2") {
		t.Fatalf("dry-run: %d %s %s", code, out, stderr)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("dry-run created target: %v", err)
	}
	code, out, stderr = writeRun(root, "", "import", "backlog-md", "backlog", "--to", "imported")
	if code != 0 || stderr != "" {
		t.Fatalf("import: %d %s %s", code, out, stderr)
	}
	if actual := get(t, sourceFile); actual != input {
		t.Fatal("import modified source")
	}
	// Project discovery currently requires a Git checkout; importing itself does not.
	if err := os.Mkdir(filepath.Join(target, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(target, ".git", "config"), "[core]\n bare = false\n")
	code, out, stderr = writeRun(target, "", "check")
	if code != 0 || stderr != "" {
		t.Fatalf("check: %d %s %s", code, out, stderr)
	}
	before := get(t, filepath.Join(target, "backlog", "TASK-01.2.md"))
	code, out, stderr = writeRun(root, "", "import", "backlog-md", "backlog", "--to", "imported")
	if code != 5 || stderr != "" || !strings.Contains(out, `"reason":"rejected"`) {
		t.Fatalf("occupied target: %d %s %s", code, out, stderr)
	}
	if after := get(t, filepath.Join(target, "backlog", "TASK-01.2.md")); after != before {
		t.Fatal("refused import modified target")
	}
}
