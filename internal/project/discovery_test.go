package project_test

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

func git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", directory}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}
func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "commit", "--allow-empty", "-m", "fixture")
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// These fixtures catch accidentally using cwd or the linked gitdir as the
// shared control root, including when git cannot be executed at all.
func TestDiscoverWorktrees(t *testing.T) {
	testkit.Isolate(t)
	root := repo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "-b", "linked", linked)
	linked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(linked, "a", "b")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(t.TempDir(), "bare.git")
	git(t, root, "clone", "--bare", root, bare)
	bare, err = filepath.EvalSymlinks(bare)
	if err != nil {
		t.Fatal(err)
	}
	bareLinked := filepath.Join(t.TempDir(), "bare-linked")
	git(t, bare, "worktree", "add", "-b", "bare-linked", bareLinked)
	bareLinked, err = filepath.EvalSymlinks(bareLinked)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	for _, tc := range []struct {
		dir, checkout, control string
		warning                bool
	}{{root, root, root, false}, {linked, linked, root, false}, {sub, linked, root, false}, {bare, bare, bare, true}, {bareLinked, bareLinked, bareLinked, true}} {
		d, err := project.Discover(tc.dir)
		if err != nil {
			t.Fatal(err)
		}
		if d.Directory != tc.dir || d.CheckoutRoot != tc.checkout || d.ControlRoot != tc.control || (len(d.Warnings) > 0) != tc.warning {
			t.Fatalf("%s: %+v", tc.dir, d)
		}
	}
	paths := project.Paths{ConfigFile: filepath.Join(t.TempDir(), "missing.yaml")}
	put(t, filepath.Join(linked, ".config", "backlog.yaml"), "root: checkout\nproject: {name: linked}\n")
	cfg, err := project.Resolve(paths, sub, nil)
	if err != nil || cfg.Discovery.ControlRoot != linked || cfg.Project.Name != "linked" {
		t.Fatalf("checkout opt-out: %+v %v", cfg, err)
	}
}
func TestRemoteIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ remote, want string }{
		{"git@github.com:owner/repo.git", "github.com/owner/repo"},
		{(&url.URL{Scheme: "https", User: url.UserPassword("fixture-user", "fixture-password"), Host: "GITHUB.com", Path: "/owner/repo.git/"}).String(), "github.com/owner/repo"},
		{"ssh://git@github.com:2222/owner/repo.git", "github.com/owner/repo"},
		{"https://example.com:8443/owner/repo", "example.com/owner/repo"},
		{"git://example.com/owner/repo.git", "example.com/owner/repo"},
	} {
		t.Run(tc.remote, func(t *testing.T) {
			t.Parallel()
			got, err := project.NormalizeRemote(tc.remote)
			if err != nil || got != tc.want {
				t.Fatalf("%q: %q %v", tc.remote, got, err)
			}
		})
	}
	for _, remote := range []string{"/local/repo", "file:///local/repo", "https://github.com/", "git@host:repo"} {
		if _, err := project.NormalizeRemote(remote); err == nil {
			t.Errorf("accepted %s", remote)
		}
	}
	root := repo(t)
	d, err := project.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	id, local, err := project.Identity(d)
	if err != nil || !local || id != root {
		t.Fatalf("fallback: %s %t %v", id, local, err)
	}
	git(t, root, "config", "remote.origin.url", "ssh://git@github.com:2222/owner/repo.git")
	id, local, err = project.Identity(d)
	if err != nil || local || id != "github.com/owner/repo" {
		t.Fatalf("origin: %s %t %v", id, local, err)
	}
}
func TestDetectLegacySources(t *testing.T) {
	t.Parallel()
	root := repo(t)
	put(t, filepath.Join(root, "backlog", "config.yml"), "project_name: fixture\n")
	put(t, filepath.Join(root, "backlog.config.yml"), "project_name: fixture\n")
	if err := os.Mkdir(filepath.Join(root, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	d, err := project.Discover(root)
	if err != nil || len(d.Detected) != 3 {
		t.Fatalf("legacy: %+v %v", d, err)
	}
	put(t, filepath.Join(root, ".config", "backlog.yaml"), "version: 1\n")
	d, err = project.Discover(root)
	if err != nil || len(d.Detected) != 0 {
		t.Fatalf("explicit: %+v %v", d, err)
	}
}
