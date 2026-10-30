// Package doctest checks published examples against the built public CLI.
package doctest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/testkit"
)

// Catch examples drifting from actual arguments, mutation behavior and claim
// lifecycle, without contacting providers or touching the reader's projects.
// blDir holds the bl binary built once for every test in the package.
var blDir string

func TestMain(m *testing.M) {
	os.Exit(buildAndRun(m))
}

func buildAndRun(m *testing.M) int {
	dir, err := os.MkdirTemp("", "bl-doctest-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	// Build before tests isolate HOME/XDG so Go keeps the caller's toolchain caches.
	if out, err := exec.Command("go", "build", "-o", filepath.Join(dir, "bl"), "../../cmd/bl").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build bl: %v\n%s", err, out)
		return 1
	}
	blDir = dir
	return m.Run()
}

func docCLI(t *testing.T) string {
	t.Helper()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	testkit.Isolate(t)
	fake, err := os.ReadFile(filepath.Join(repo, "internal/cli/testdata/next-worklease.py"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "worklease"), fake, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", strings.Join([]string{bin, blDir, os.Getenv("PATH")}, string(os.PathListSeparator)))
	return repo
}

func TestShellExamples(t *testing.T) {
	testkit.Unix(t, "POSIX shell examples")
	repo := docCLI(t)
	for _, doc := range []string{"README.md", "docs/guide.md"} {
		t.Run(doc, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "repo")
			if err := os.Mkdir(root, 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BL_FAKE_ROOT", root)
			t.Setenv("BL_FAKE_STATE", filepath.Join(root, "authority.json"))
			t.Setenv("BL_FAKE_ITEMS", "backlog")
			run(t, root, "git", "init", "--quiet")
			legacy := filepath.Join(root, "old-backlog", "backlog")
			if err := os.MkdirAll(filepath.Join(legacy, "tasks"), 0755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(legacy, "config.yml"), "project_name: Legacy\nstatuses: [To Do, In Progress, Done]\n")
			write(t, filepath.Join(legacy, "tasks", "task-1.md"), "---\nid: task-1\ntitle: Imported feature\nstatus: To Do\n---\n\n## Description\n\nLegacy task.\n")
			data, err := os.ReadFile(filepath.Join(repo, doc))
			if err != nil {
				t.Fatal(err)
			}
			blocks := shellBlocks(t, string(data))
			if len(blocks) == 0 {
				t.Fatal("no executable examples")
			}
			// One shell preserves exports and cwd across all examples in a doc.
			run(t, root, "sh", "-eu", "-c", strings.Join(blocks, "\n"))
			out := run(t, root, "bl", "show", "BL-1", "--json")
			if !strings.Contains(out, "Ship the first feature") {
				t.Fatal(out)
			}
			if doc == "docs/guide.md" {
				if !strings.Contains(out, `"status":"done"`) || !strings.Contains(out, `"checked":true`) {
					t.Fatalf("guide did not finish its verified task: %s", out)
				}
				state, err := os.ReadFile(filepath.Join(root, "authority.json"))
				if err != nil || !bytes.Contains(state, []byte(`"claims": {}`)) {
					t.Fatalf("guide left a claim: %s (%v)", state, err)
				}
				run(t, root, "bl", "-C", "../imported", "show", "TASK-1", "--json")
			}
		})
	}
}

// Catch examples rotting as the config schema and CLI change. Examples that
// need a provider account or bd only have their config validated.
func TestExamples(t *testing.T) {
	testkit.Unix(t, "POSIX shell examples")
	repo := docCLI(t)
	external := map[string]bool{"07-beads": true, "08-github-issues": true, "09-github-project": true, "10-linear": true, "11-jira": true}
	entries, err := os.ReadDir(filepath.Join(repo, "examples"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			home := testkit.Isolate(t)
			root := filepath.Join(t.TempDir(), "repo")
			if err := os.CopyFS(root, os.DirFS(filepath.Join(repo, "examples", entry.Name()))); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "config.yaml")); err == nil {
				config := filepath.Join(home, "XDG_CONFIG_HOME", "backlog")
				if err := os.MkdirAll(config, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(root, "config.yaml"), filepath.Join(config, "config.yaml")); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("BL_FAKE_ROOT", root)
			t.Setenv("BL_FAKE_STATE", filepath.Join(root, "authority.json"))
			t.Setenv("BL_FAKE_ITEMS", "backlog")
			for _, name := range []string{"GIT_AUTHOR", "GIT_COMMITTER"} {
				t.Setenv(name+"_NAME", "Ana")
				t.Setenv(name+"_EMAIL", "ana@example.com")
			}
			run(t, root, "git", "init", "--quiet")
			if _, err := os.Stat(filepath.Join(root, "backlog.yaml")); err == nil {
				run(t, root, "bl", "config", "show", "--json")
			}
			data, err := os.ReadFile(filepath.Join(root, "README.md"))
			if err != nil {
				t.Fatal(err)
			}
			blocks := shellBlocks(t, string(data))
			if !external[entry.Name()] {
				run(t, root, "sh", "-eu", "-c", strings.Join(blocks, "\n"))
			}
		})
	}
}

func shellBlocks(t *testing.T, doc string) []string {
	t.Helper()
	var blocks []string
	var block strings.Builder
	fence := ""
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "```") {
			if fence != "" {
				if fence == "sh" {
					blocks = append(blocks, block.String())
				}
				fence = ""
				block.Reset()
			} else {
				fence = strings.TrimPrefix(line, "```")
				if fence == "" {
					t.Fatal("code fences must name their language")
				}
				if fence == "bash" || fence == "shell" {
					t.Fatal("use sh for executable shell examples")
				}
			}
		} else if fence == "sh" {
			block.WriteString(line + "\n")
		}
	}
	if fence != "" {
		t.Fatal("unclosed code fence")
	}
	return blocks
}

// Catch missing documentation in the entry point and stale local link targets.
func TestDocumentationIndex(t *testing.T) {
	t.Parallel()
	repo := filepath.Join("..", "..")
	readme, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(filepath.Join(repo, "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(repo, path)
			if err != nil {
				return err
			}
			if !bytes.Contains(readme, []byte("("+filepath.ToSlash(rel)+")")) {
				t.Errorf("README does not link %s", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`\]\(([^)]+)\)`)
	for _, doc := range []string{"README.md", "docs/guide.md"} {
		data, err := os.ReadFile(filepath.Join(repo, doc))
		if err != nil {
			t.Fatal(err)
		}
		for _, link := range links.FindAllSubmatch(data, -1) {
			target := strings.SplitN(string(link[1]), "#", 2)[0]
			if target == "" || strings.Contains(target, "://") {
				continue
			}
			if _, err := os.Stat(filepath.Join(repo, filepath.Dir(doc), target)); err != nil {
				t.Errorf("%s link %s: %v", doc, target, err)
			}
		}
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir, program string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %q: %v\n%s", program, args, err, out)
	}
	return string(out)
}
