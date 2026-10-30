package changes

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

// Catches packed refs or linked worktrees read wrongly from .git, git status
// running a filter that repository config names, a merge that leaves the
// worktree and landed commits as separate changes, and a change lost once its
// branch is deleted.
func TestWorktreeToLanded(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	testkit.Isolate(t)
	repo := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "wt")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Tester", "-c", "user.email=t@example.com", "-c", "commit.gpgSign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "commit", "-q", "--allow-empty", "-m", "start")
	git(repo, "worktree", "add", "-q", "-b", "bl-12-fix", worktree)
	for name, text := range map[string]string{"a": "a\n", ".gitattributes": "a filter=x\n"} {
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(worktree, "add", ".")
	git(worktree, "commit", "-q", "-m", "fix")
	git(repo, "pack-refs", "--all")
	d, err := project.Discover(repo)
	if err != nil {
		t.Fatal(err)
	}
	o := Options{Discovery: d, CacheDir: t.TempDir(), Git: true}
	items := []Item{{Ref: model.Ref{Source: "tasks", ID: "BL-12"}}}
	load := func(previous Data) (Data, Change) {
		t.Helper()
		data := LoadLocal(context.Background(), o, previous)
		if len(data.Statuses) != 1 || data.Statuses[0].State != OK {
			t.Fatalf("git status: %+v", data.Statuses)
		}
		cs := Link(items, data)[items[0].Ref]
		if len(cs) != 1 {
			t.Fatalf("want one change, got %+v", cs)
		}
		return data, cs[0]
	}
	data, c := load(Data{})
	physical, _ := filepath.EvalSymlinks(worktree)
	if got, _ := filepath.EvalSymlinks(c.Worktree); c.Stage != StageWorktree || c.State != StateOpen || c.Link != "branch" || got != physical {
		t.Fatalf("before merge: %+v", c)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	git(repo, "config", "filter.x.clean", "touch "+marker+"; cat")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(worktree, "a"), later, later); err != nil {
		t.Fatal(err)
	}
	enriched := Enrich(context.Background(), []Change{c})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("git status ran the repository's filter")
	}
	if enriched[0].Dirty != nil {
		t.Fatalf("counted changes through a repository filter: %d", *enriched[0].Dirty)
	}
	git(repo, "config", "--unset", "filter.x.clean")
	if enriched = Enrich(context.Background(), []Change{c}); enriched[0].Dirty == nil {
		t.Fatal("no dirty count without a repository filter")
	}
	git(repo, "merge", "-q", "--no-ff", "--no-edit", "bl-12-fix")
	data, c = load(data)
	if c.Stage != StageLanded || c.State != StateMerged || c.Worktree == "" || len(c.Commits) != 1 {
		t.Fatalf("after merge: %+v", c)
	}
	git(repo, "worktree", "remove", worktree)
	git(repo, "branch", "-q", "-d", "bl-12-fix")
	if _, c = load(data); c.Stage != StageLanded || c.Link != "merge" || c.Worktree != "" {
		t.Fatalf("after branch deleted: %+v", c)
	}
}

// Catches a link that reads as merged or passing when it is ambiguous, stale,
// for another commit, or another repository's pull request with the merged
// one's number, and a refs URL that vanishes when bl can't resolve it.
func TestLinkStates(t *testing.T) {
	ref := func(source, id string) model.Ref { return model.Ref{Source: source, ID: id} }
	day := func(n int) time.Time { return time.Date(2026, 1, n, 0, 0, 0, 0, time.UTC) }
	items := []Item{
		{Ref: ref("a", "BL-12")}, {Ref: ref("a", "BL-7")}, {Ref: ref("a", "BL-9")}, {Ref: ref("b", "BL-9")},
		{Ref: ref("a", "BL-30"), Refs: []string{"https://github.com/acme/app/pull/99"}},
		{Ref: ref("a", "BL-31"), NodeID: "I_31"}, {Ref: ref("a", "BL-20")},
	}
	merge := Commit{SHA: "c1", Subject: "Merge pull request #52 from acme/bl-12-y", Time: day(5)}
	merge.Branch, merge.PR = MergedBranch(merge.Subject)
	if merge.Branch != "bl-12-y" || merge.PR != 52 {
		t.Fatalf("merge subject: %q %d", merge.Branch, merge.PR)
	}
	for subject, want := range map[string]string{"Merge branch 'bl-7-a' into main": "bl-7-a", "Merge remote-tracking branch 'origin/feat/bl-7-b'": "feat/bl-7-b", "Revert \"Merge branch 'x'\"": ""} {
		if got, _ := MergedBranch(subject); got != want {
			t.Fatalf("%s: %q", subject, got)
		}
	}
	d := Data{
		Default: "main", DefaultRef: "refs/heads/main", Repo: "acme/app",
		Branches: []Branch{{Name: "feat/bl-12-x", Local: "bbb", Remote: "aaa", RemoteRef: "refs/remotes/origin/feat/bl-12-x"}},
		PullRequests: []PullRequest{
			{Repo: "acme/app", Number: 45, URL: "https://github.com/acme/app/pull/45", State: StateOpen, Head: "feat/bl-12-x", HeadSHA: "aaa", Checks: ChecksPassing, Created: day(1), Updated: day(1)},
			{Repo: "acme/app", Number: 46, URL: "https://github.com/acme/app/pull/46", State: StateOpen, Draft: true, Head: "x", Title: "Fix BL-7 crash", Created: day(2), Updated: day(2)},
			{Repo: "acme/app", Number: 47, URL: "https://github.com/acme/app/pull/47", State: StateOpen, Head: "y", Body: "Fixes BL-9", Checks: ChecksFailing, Created: day(1), Updated: day(1)},
			{Repo: "acme/app", Number: 48, URL: "https://github.com/acme/app/pull/48", State: StateMerged, Head: "bl-7-old", Checks: ChecksPassing, Stale: "github refresh failed: boom", Created: day(1), Updated: day(1)},
			{Repo: "acme/app", Number: 50, URL: "https://github.com/acme/app/pull/50", State: StateOpen, Head: "z", Closes: []string{"I_31"}, Created: day(1), Updated: day(1)},
			{Repo: "acme/lib", Number: 52, URL: "https://github.com/acme/lib/pull/52", State: StateOpen, Head: "x", Title: "BL-20 new API", Created: day(1), Updated: day(1)},
		},
		Landed: []Commit{merge},
	}
	got := map[model.Ref][]string{}
	for r, cs := range Link(items, d) {
		for _, c := range cs {
			got[r] = append(got[r], strings.Join([]string{Label(c), c.Stage, c.State, c.Checks, c.Link}, " "))
		}
	}
	want := map[model.Ref][]string{
		ref("a", "BL-12"): {"bl-12-y landed merged  merge", "#45 pr open stale branch"},
		ref("a", "BL-7"):  {"#46 pr draft  title", "#48 pr stale stale branch"},
		ref("a", "BL-9"):  {"#47 pr unknown  closes"},
		ref("b", "BL-9"):  {"#47 pr unknown  closes"},
		ref("a", "BL-30"): {"#99 pr unknown  ref"},
		ref("a", "BL-31"): {"#50 pr open  provider"},
		ref("a", "BL-20"): {"#52 pr open  title"},
	}
	for r, w := range want {
		if !slices.Equal(got[r], w) {
			t.Errorf("%s: got %q want %q", r.ID, got[r], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("linked %d items, want %d: %v", len(got), len(want), got)
	}
}
