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
	if config := get(t, filepath.Join(target, "backlog.yaml")); !strings.Contains(config, "key: draft") {
		t.Fatalf("a source without drafts lost the draft status:\n%s", config)
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

// Catches detection accidentally enabling writes and section editing incorrectly
// requiring a native-format file for a configured Backlog.md source.
func TestBacklogMDDetectionAndSectionEdit(t *testing.T) {
	testkit.Isolate(t)
	root := t.TempDir()
	for _, dir := range []string{".git", ".config", "backlog/tasks"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	put(t, filepath.Join(root, ".git/config"), "[core]\n bare = false\n")
	put(t, filepath.Join(root, "backlog/config.yml"), "project_name: Fixture\nstatuses: [To Do, In Progress, Done]\n")
	path := filepath.Join(root, "backlog/tasks/task-1 - Original.md")
	original := "---\nid: TASK-1\ntitle: Original\nstatus: To Do\ncreated_date: 2026-10-21 12:00\n---\n\n## Description\n\n<!-- SECTION:DESCRIPTION:BEGIN -->\nOriginal body.\n<!-- SECTION:DESCRIPTION:END -->\n"
	put(t, path, original)
	code, out, stderr := writeRun(root, "", "ls")
	if code != 0 || !strings.Contains(out, "Original") || !strings.Contains(stderr, "read-only") {
		t.Fatalf("detected read: %d %s %s", code, out, stderr)
	}
	code, out, _ = writeRun(root, "", "set", "TASK-1", "title=Unsafe")
	if code == 0 || get(t, path) != original {
		t.Fatalf("detected write: %d %s", code, out)
	}
	put(t, filepath.Join(root, "backlog.yaml"), "version: 1\nsources: [{id: tasks, type: backlog-md, path: backlog}]\n")
	editor := filepath.Join(root, "editor")
	put(t, editor, "#!/bin/sh\nprintf 'Changed body.\\n' > \"$1\"\n")
	if err := os.Chmod(editor, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor)
	writeOK(t, root, "", "edit", "TASK-1", "--section", "description")
	code, out, stderr = writeRun(root, "", "show", "TASK-1")
	if code != 0 || stderr != "" || !strings.Contains(out, "Changed body.") || !strings.Contains(get(t, path), "<!-- SECTION:DESCRIPTION:BEGIN -->") {
		t.Fatalf("configured section edit: %d %s %s", code, out, stderr)
	}
}
