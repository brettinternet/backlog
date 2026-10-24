package cli_test

import (
	"os"
	"os/exec"
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

// Catches an in-place cutover that loses uncommitted work, leaves Backlog.md
// files behind, or produces a project bl check rejects. A docs/backlog source
// needs backlog.yaml at the checkout root, where discovery looks.
func TestImportInPlaceCutover(t *testing.T) {
	testkit.Isolate(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_DATE", "2026-03-04T05:06:07Z")
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if data, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, data)
		}
	}
	git("init", "--initial-branch=main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	for _, dir := range []string{"docs/backlog/tasks", "docs/backlog/docs"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	put(t, filepath.Join(root, "docs/backlog/config.yml"), "project_name: Example\nstatuses: [To Do, In Progress, Done]\ntask_prefix: task\n")
	taskFile := filepath.Join(root, "docs/backlog/tasks/task-1 - First.md")
	put(t, taskFile, "---\nid: task-1\ntitle: First\nstatus: To Do\n---\n\n## Description\n\nSee [docs](../docs/readme.md).\n")
	put(t, filepath.Join(root, "docs/backlog/docs/readme.md"), "kept\n")
	git("add", ".")
	git("commit", "-m", "backlog")

	put(t, filepath.Join(root, ".gitignore"), "docs/backlog/drafts/\n")
	git("add", ".gitignore")
	git("commit", "-m", "ignore")
	if err := os.MkdirAll(filepath.Join(root, "docs/backlog/drafts"), 0700); err != nil {
		t.Fatal(err)
	}
	ignored := filepath.Join(root, "docs/backlog/drafts/draft-1.md")
	put(t, ignored, "---\nid: draft-1\ntitle: Ignored\nstatus: To Do\n---\n")
	if code, out, _ := writeRun(root, "", "import", "backlog-md", "docs/backlog", "--in-place"); code == 0 || !strings.Contains(out, "removes only files committed") || get(t, ignored) == "" {
		t.Fatalf("ignored source: %d %s", code, out)
	}
	if err := os.Remove(ignored); err != nil {
		t.Fatal(err)
	}
	put(t, taskFile+".tmp", "dirty")
	if code, out, _ := writeRun(root, "", "import", "backlog-md", "docs/backlog", "--in-place", "--dry-run"); code == 0 || !strings.Contains(out, "commit or stash") {
		t.Fatalf("dirty source: %d %s", code, out)
	}
	if err := os.Remove(taskFile + ".tmp"); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := writeRun(root, "", "import", "backlog-md", "docs/backlog", "--in-place", "--dry-run")
	if code != 0 || stderr != "" || !strings.Contains(out, "docs/backlog/config.yml") {
		t.Fatalf("dry-run: %d %s %s", code, out, stderr)
	}
	if _, err := os.Lstat(filepath.Join(root, "backlog.yaml")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote config: %v", err)
	}
	if code, out, stderr = writeRun(root, "", "import", "backlog-md", "docs/backlog", "--in-place"); code != 0 || stderr != "" {
		t.Fatalf("import: %d %s %s", code, out, stderr)
	}
	for _, gone := range []string{"docs/backlog/config.yml", "docs/backlog/tasks"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s survived the cutover: %v", gone, err)
		}
	}
	if get(t, filepath.Join(root, "docs/backlog/docs/readme.md")) != "kept\n" {
		t.Fatal("cutover changed docs")
	}
	item := get(t, filepath.Join(root, "docs/backlog/TASK-1.md"))
	if !strings.Contains(item, "created: 2026-03-04T05:06:07Z") || !strings.Contains(item, "[docs](docs/readme.md)") {
		t.Fatalf("created date or link not migrated:\n%s", item)
	}
	if code, out, stderr = writeRun(root, "", "check"); code != 0 || stderr != "" {
		t.Fatalf("check: %d %s %s", code, out, stderr)
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
