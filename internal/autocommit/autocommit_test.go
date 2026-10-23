package autocommit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/testkit"
)

func repository(t *testing.T) string {
	t.Helper()
	testkit.Isolate(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := t.TempDir()
	run(t, root, "init", "--initial-branch=main")
	run(t, root, "config", "user.name", "Test")
	run(t, root, "config", "user.email", "test@example.invalid")
	for _, path := range []string{"item.md", "staged", "unstaged"} {
		put(t, root, path, "original\n")
	}
	run(t, root, "add", ".")
	run(t, root, "commit", "-m", "initial")
	return root
}
func run(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return string(data)
}
func put(t *testing.T, root, path, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func mutate(t *testing.T, s *Session, root string, paths []string, write func() error) {
	t.Helper()
	for i := range paths {
		paths[i] = filepath.Join(root, paths[i])
	}
	if err := Write(WithSession(context.Background(), s), root, paths, "set", "BL-1", "Title", write); err != nil {
		t.Fatal(err)
	}
}

// Regression: committing a backlog edit must not consume another path's staged
// hunk, discard its unstaged hunk, or miss an archive's deletion/new path.
func TestPreservesOtherChanges(t *testing.T) {
	root := repository(t)
	put(t, root, "staged", "staged version\n")
	run(t, root, "add", "staged")
	put(t, root, "staged", "unstaged version of staged file\n")
	put(t, root, "unstaged", "other edit\n")
	indexBefore := run(t, root, "diff", "--cached", "--binary")
	workBefore := run(t, root, "diff", "--binary", "--", "staged", "unstaged")
	var warnings []string
	s := New(0, "archive", func(w string) { warnings = append(warnings, w) })
	mutate(t, s, root, []string{"item.md", "archive.md"}, func() error { return os.Rename(filepath.Join(root, "item.md"), filepath.Join(root, "archive.md")) })
	s.Close()
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	if got := run(t, root, "diff", "--cached", "--binary"); got != indexBefore {
		t.Fatalf("index changed: %s", got)
	}
	if got := run(t, root, "diff", "--binary", "--", "staged", "unstaged"); got != workBefore {
		t.Fatalf("worktree changed: %s", got)
	}
	if got := run(t, root, "log", "-1", "--format=%s"); got != "docs(backlog): archive BL-1 Title\n" {
		t.Fatal(got)
	}
	if got := run(t, root, "show", "--format=", "--name-only", "--no-renames", "HEAD"); got != "archive.md\nitem.md\n" {
		t.Fatal(got)
	}
}

// Regression: Git failures must never roll back the successful file mutation;
// a dirty task path must not silently absorb edits that predate bl.
func TestSkipAndFailureKeepWrite(t *testing.T) {
	for _, scenario := range []string{"merge", "rebase", "rebase-apply", "cherry-pick", "detached", "hook", "dirty", "external", "staged-later"} {
		t.Run(scenario, func(t *testing.T) {
			root := repository(t)
			head := run(t, root, "rev-parse", "HEAD")
			switch scenario {
			case "merge":
				put(t, root, ".git/MERGE_HEAD", head)
			case "rebase", "rebase-apply":
				name := "rebase-merge"
				if scenario == "rebase-apply" {
					name = scenario
				}
				if err := os.Mkdir(filepath.Join(root, ".git", name), 0700); err != nil {
					t.Fatal(err)
				}
			case "cherry-pick":
				put(t, root, ".git/CHERRY_PICK_HEAD", head)
			case "detached":
				run(t, root, "checkout", "--detach")
			case "hook":
				put(t, root, ".git/hooks/pre-commit", "#!/bin/sh\necho hook-refused >&2\nexit 1\n")
				if err := os.Chmod(filepath.Join(root, ".git/hooks/pre-commit"), 0700); err != nil {
					t.Fatal(err)
				}
			case "dirty":
				put(t, root, "item.md", "user edit\n")
			}
			var warnings []string
			s := New(0, "", func(w string) { warnings = append(warnings, w) })
			mutate(t, s, root, []string{"item.md"}, func() error { return os.WriteFile(filepath.Join(root, "item.md"), []byte("bl edit\n"), 0600) })
			if scenario == "external" {
				put(t, root, "item.md", "external edit\n")
			}
			if scenario == "staged-later" {
				run(t, root, "add", "item.md")
			}
			s.Close()
			if len(warnings) == 0 {
				t.Fatal("missing warning")
			}
			if scenario == "hook" && !strings.Contains(strings.Join(warnings, " "), "hook-refused") {
				t.Fatal(warnings)
			}
			if got := run(t, root, "rev-parse", "HEAD"); got != head {
				t.Fatal("committed despite unsafe state")
			}
			data, err := os.ReadFile(filepath.Join(root, "item.md"))
			if err != nil {
				t.Fatal(err)
			}
			want := "bl edit\n"
			if scenario == "external" {
				want = "external edit\n"
			}
			if string(data) != want {
				t.Fatalf("lost write: %q", data)
			}
		})
	}
}

// Regression: consecutive edits must form one batch, and quit must flush rather
// than cancel a pending idle timer. A long delay avoids wall-clock assertions.
func TestBatchAndQuitFlush(t *testing.T) {
	root := repository(t)
	var warnings []string
	s := New(2*time.Second, "", func(w string) { warnings = append(warnings, w) })
	var callbacks []func()
	s.afterFunc = func(delay time.Duration, callback func()) *time.Timer {
		if delay != 2*time.Second {
			t.Fatalf("idle delay: %v", delay)
		}
		callbacks = append(callbacks, callback)
		return time.NewTimer(time.Hour)
	}
	for _, text := range []string{"first\n", "second\n"} {
		mutate(t, s, root, []string{"item.md"}, func() error { return os.WriteFile(filepath.Join(root, "item.md"), []byte(text), 0600) })
	}
	if got := run(t, root, "rev-list", "--count", "HEAD"); got != "1\n" {
		t.Fatal(got)
	}
	callbacks[0]() // A stopped callback that already fired must not flush a newer edit.
	if got := run(t, root, "rev-list", "--count", "HEAD"); got != "1\n" {
		t.Fatal(got)
	}
	callbacks[1]() // Inject the two-second idle boundary.
	if got := run(t, root, "rev-list", "--count", "HEAD"); got != "2\n" {
		t.Fatal(got)
	}
	mutate(t, s, root, []string{"item.md"}, func() error { return os.WriteFile(filepath.Join(root, "item.md"), []byte("quit\n"), 0600) })
	s.Close()
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	if got := run(t, root, "rev-list", "--count", "HEAD"); got != "3\n" {
		t.Fatal(got)
	}
	if got := run(t, root, "show", "HEAD:item.md"); got != "quit\n" {
		t.Fatal(got)
	}
}
