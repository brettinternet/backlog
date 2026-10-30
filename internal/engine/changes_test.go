package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source/github"
	"github.com/brettinternet/backlog/internal/testkit"
	"github.com/brettinternet/backlog/internal/tour"
)

// Catches a GitLab origin read without its one GitLab account, or as a
// GitHub forge, a forge picked when the host's account is ambiguous, and a
// GitLab forge borrowing a GitHub account.
func TestOriginForge(t *testing.T) {
	testkit.Isolate(t)
	common := t.TempDir()
	if err := os.WriteFile(filepath.Join(common, "config"), []byte("[remote \"origin\"]\n\turl = git@gitlab.example.com:grp/sub/app.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := project.Config{Discovery: project.Discovery{CommonDir: common}, Accounts: map[string][]project.AccountConfig{
		"github": {{Name: "hub", Host: "gitlab.example.com", User: "alice", Env: "TOKEN"}},
		"gitlab": {{Name: "work", Host: "gitlab.example.com", User: "alice", Env: "TOKEN"}},
	}}
	names := func(cfg project.Config) []string {
		var out []string
		for _, f := range forges(cfg) {
			out = append(out, f.Name()+" "+f.Web())
		}
		return out
	}
	if got := names(cfg); len(got) != 0 {
		t.Fatalf("both a GitHub and a GitLab account: %v", got)
	}
	delete(cfg.Accounts, "github")
	if got := names(cfg); len(got) != 1 || got[0] != "gitlab grp/sub/app https://gitlab.example.com/grp/sub/app/-" {
		t.Fatalf("one GitLab account: %v", got)
	}
	cfg.Accounts["gitlab"] = append(cfg.Accounts["gitlab"], project.AccountConfig{Name: "other", Host: "gitlab.example.com", User: "bob", Env: "TOKEN"})
	if got := names(cfg); len(got) != 0 {
		t.Fatalf("two GitLab accounts: %v", got)
	}
	cfg.Accounts = map[string][]project.AccountConfig{"github": {{Name: "hub", Host: "gitlab.com", User: "alice", Env: "TOKEN"}}}
	if _, err := cfg.ForgeAccount(project.ForgeConfig{Type: "gitlab", Host: "gitlab.com", Repo: "grp/app"}); err == nil {
		t.Fatal("a GitLab forge used a GitHub account")
	}
}

// Catches a pull request kept beside the GitHub issue it closes by
// GitHub's own closing reference, the one link rule a native item can't make.
func TestLinkedPullsClosingReference(t *testing.T) {
	cfg := project.Config{Sources: []project.SourceConfig{{ID: "issues", Type: "github", Repo: "owner/repo"}, {ID: "prs", Type: "github-pulls", Repo: "owner/repo"}}}
	items := []model.Item{
		{Ref: model.Ref{Source: "issues", ID: "#7"}, Fields: map[string]any{"id": "I_7"}},
		{Ref: model.Ref{Source: "prs", ID: "#8"}, URL: "https://github.com/owner/repo/pull/8", Status: github.PullOpen, Fields: map[string]any{"head": "patch", "closes": []any{"I_7"}}},
	}
	if got := linkedPulls(cfg, items); !slices.Equal(got[items[1].Ref], []model.Ref{items[0].Ref}) {
		t.Fatalf("linked pulls: %v", got)
	}
}

// Catches a tour stored with steps its Diff can't show: a file missing at the
// ref or before the change, a line or range end past the end of the file, or
// one counted from the worktree instead of the ref. A renamed file's old
// side, on either path, and a file the change doesn't touch are accepted, and
// every bad step is reported at once.
func TestCheckTourSteps(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	testkit.Isolate(t)
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=Tester", "-c", "user.email=t@example.com", "-c", "commit.gpgSign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("old.go", "1\n2\n3\n4\n5\n")
	write("keep.go", "1\n2\n3")
	write("gone.go", "1\n2\n")
	git("add", ".")
	git("commit", "-q", "-m", "start")
	git("mv", "old.go", "new.go")
	write("new.go", "1\n2\n3\n4\n5\n6\n")
	git("rm", "-q", "gone.go")
	git("add", ".")
	git("commit", "-q", "-m", "change")
	head := git("rev-parse", "HEAD")
	write("keep.go", strings.Repeat("x\n", 10))
	c := changes.CommitChange(repo, changes.Commit{SHA: head})
	step := func(file string, line int, side string) tour.Step {
		return tour.Step{File: file, Line: line, Side: side, Description: "x"}
	}
	good := []tour.Step{step("new.go", 6, ""), step("new.go", 5, tour.SideOld), step("old.go", 5, tour.SideOld), step("keep.go", 3, ""), step("gone.go", 2, tour.SideOld)}
	if err := checkSteps(context.Background(), c, tour.Tour{Ref: head, Steps: good}); err != nil {
		t.Fatalf("steps the Diff shows: %v", err)
	}
	past := step("new.go", 4, "")
	past.Selection = &tour.Selection{Start: tour.Position{Line: 4}, End: tour.Position{Line: 7}}
	bad := append(good, step("gone.go", 1, ""), step("keep.go", 9, ""), step("new.go", 6, tour.SideOld), step("missing.go", 1, tour.SideOld), past)
	err := checkSteps(context.Background(), c, tour.Tour{Ref: head, Steps: bad})
	want := "rejected: tour step 6: gone.go line 1: no such file at " + head[:7] +
		"; step 7: keep.go line 9: past the end of the file, which has 3 lines at " + head[:7] +
		"; step 8: new.go line 6: past the end of the file, which has 5 lines before the change" +
		"; step 9: missing.go line 1: no such file before the change" +
		"; step 10: new.go lines 4–7: past the end of the file, which has 6 lines at " + head[:7]
	if err == nil || err.Error() != want {
		t.Fatalf("bad steps:\n got %v\nwant %s", err, want)
	}
}
