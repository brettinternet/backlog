// Package doctest checks published examples against the built public CLI.
package doctest

import (
	"bytes"
	"context"
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
func TestShellExamples(t *testing.T) {
	testkit.Isolate(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	run(t, repo, "go", "build", "-o", filepath.Join(bin, "bl"), "./cmd/bl")
	fake, err := os.ReadFile(filepath.Join(repo, "internal/cli/testdata/next-worklease.py"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "worklease"), fake, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
