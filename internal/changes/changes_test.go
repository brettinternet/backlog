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
// or a working tree diff running a filter that repository config names, a
// change's files counted from the default branch's tip instead of its merge
// base or missing uncommitted, renamed, or binary files, a merge that leaves
// the worktree and landed commits as separate changes, and a change lost once
// its branch is deleted.
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
	write := func(dir, name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(repo, "init", "-q", "-b", "main")
	write(repo, "old", "1\n2\n3\n4\n5\n")
	git(repo, "add", ".")
	git(repo, "commit", "-q", "-m", "start")
	git(repo, "worktree", "add", "-q", "-b", "bl-12-fix", worktree)
	for name, text := range map[string]string{"a": "a\n", ".gitattributes": "a filter=x\n", "bin": "\x00\x01"} {
		write(worktree, name, text)
	}
	git(worktree, "mv", "old", "new")
	git(worktree, "add", ".")
	git(worktree, "commit", "-q", "-m", "fix")
	write(repo, "main-only", "m\n")
	git(repo, "add", ".")
	git(repo, "commit", "-q", "-m", "elsewhere")
	write(worktree, "a", "a\nb\n")
	write(worktree, "untracked", "x\ny")
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
	enriched := Enrich(context.Background(), []Change{c}, nil, false)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("git status ran the repository's filter")
	}
	if enriched[0].Dirty != nil {
		t.Fatalf("counted changes through a repository filter: %d", *enriched[0].Dirty)
	}
	if d, err := LoadDetail(context.Background(), c, nil, false); err != nil || d.Reason == "" || len(d.Files) != 4 {
		t.Fatalf("detail through a repository filter: %+v %v", d, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the working tree diff ran the repository's filter")
	}
	git(repo, "config", "--unset", "filter.x.clean")
	if enriched = Enrich(context.Background(), []Change{c}, nil, false); enriched[0].Dirty == nil {
		t.Fatal("no dirty count without a repository filter")
	}
	detail, err := LoadDetail(context.Background(), c, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []File{
		{Path: ".gitattributes", Status: FileAdded, Additions: 1},
		{Path: "a", Status: FileUncommitted, Additions: 2},
		{Path: "bin", Status: FileBinary},
		{Path: "new", From: "old", Status: FileRenamed},
		{Path: "untracked", Status: FileUncommitted, Additions: 2},
	}
	if !slices.Equal(detail.Files, want) || detail.Reason != "" || len(detail.Commits) != 1 || detail.Commits[0].Subject != "fix" || detail.Commits[0].Author != "Tester" {
		t.Fatalf("worktree detail: %+v", detail)
	}
	// Patches never run an external diff, a textconv, or a filter that
	// repository config names.
	script := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	git(repo, "config", "diff.external", script)
	git(repo, "config", "diff.t.textconv", script)
	write(filepath.Join(repo, ".git", "info"), "attributes", "* diff=t\n")
	git(repo, "config", "filter.x.clean", script)
	if d, err := LoadDetail(context.Background(), c, nil, true); err != nil || d.Reason == "" || !d.Patched || !strings.Contains(d.Files[0].Patch, "+a filter=x") {
		t.Fatalf("patches through a repository filter: %+v %v", d, err)
	}
	git(repo, "config", "--unset", "filter.x.clean")
	patched, err := LoadDetail(context.Background(), c, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a patch ran a command repository config names")
	}
	patches := map[string]string{}
	for _, f := range patched.Files {
		patches[f.Path] = f.Patch
	}
	if patches["a"] != "@@ -0,0 +1,2 @@\n+a\n+b\n" || patches["untracked"] != "@@ -0,0 +1,2 @@\n+x\n+y\n\\ No newline at end of file\n" || patches["bin"] != "" || patches["new"] != "" {
		t.Fatalf("worktree patches: %q", patches)
	}
	if enriched[0].Additions != 5 || enriched[0].Deletions != 0 {
		t.Fatalf("worktree size: +%d −%d", enriched[0].Additions, enriched[0].Deletions)
	}
	git(repo, "merge", "-q", "--no-ff", "--no-edit", "bl-12-fix")
	data, c = load(data)
	if c.Stage != StageLanded || c.State != StateMerged || c.Worktree == "" || len(c.Commits) != 1 {
		t.Fatalf("after merge: %+v", c)
	}
	if d, err := LoadDetail(context.Background(), c, nil, false); err != nil || len(d.Files) != 4 || d.Files[3].Path != "new" || len(d.Commits) != 2 || d.Commits[1].Subject != "fix" {
		t.Fatalf("landed detail: %+v %v", d, err)
	}
	if d, err := LoadDetail(context.Background(), c, nil, true); err != nil || d.Files[1].Path != "a" || d.Files[1].Patch != "@@ -0,0 +1 @@\n+a\n" {
		t.Fatalf("landed patches: %+v %v", d, err)
	}
	if d, err := LoadDetail(context.Background(), CommitChange(repo, c.Commits[0].SHA), nil, true); err != nil || len(d.Files) != 4 || len(d.Commits) != 2 {
		t.Fatalf("commit patches: %+v %v", d, err)
	}
	root, err := exec.Command("git", "-C", repo, "rev-list", "--max-parents=0", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if d, err := LoadDetail(context.Background(), CommitChange(repo, strings.TrimSpace(string(root))), nil, true); err != nil || len(d.Files) == 0 || len(d.Commits) != 1 || d.Files[0].Patch == "" {
		t.Fatalf("root commit patches: %+v %v", d, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a landed patch ran a command repository config names")
	}
	git(repo, "worktree", "remove", "--force", worktree)
	git(repo, "branch", "-q", "-d", "bl-12-fix")
	if _, c = load(data); c.Stage != StageLanded || c.Link != "merge" || c.Worktree != "" {
		t.Fatalf("after branch deleted: %+v", c)
	}
}

// Catches a link that reads as merged or passing when it is ambiguous, stale,
// for another commit, or another repository's pull request with the merged
// one's number, a refs URL that vanishes when bl can't resolve it, and a
// GitLab merge request in a nested group that a refs URL misses.
func TestLinkStates(t *testing.T) {
	ref := func(source, id string) model.Ref { return model.Ref{Source: source, ID: id} }
	day := func(n int) time.Time { return time.Date(2026, 1, n, 0, 0, 0, 0, time.UTC) }
	items := []Item{
		{Ref: ref("a", "BL-12")}, {Ref: ref("a", "BL-7")}, {Ref: ref("a", "BL-9")}, {Ref: ref("b", "BL-9")},
		{Ref: ref("a", "BL-30"), Refs: []string{"https://github.com/acme/app/pull/99"}},
		{Ref: ref("a", "BL-31"), NodeID: "I_31"}, {Ref: ref("a", "BL-20")},
		{Ref: ref("a", "BL-40"), Refs: []string{"https://gitlab.com/grp/sub/app/-/merge_requests/7"}},
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
			{Repo: "grp/sub/app", Number: 7, URL: "https://gitlab.com/grp/sub/app/-/merge_requests/7", State: StateOpen, Head: "w", Created: day(1), Updated: day(1)},
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
		ref("a", "BL-40"): {"!7 pr open  ref"},
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

type reviewForge struct{ review Review }

func (reviewForge) Name() string { return "fake" }
func (reviewForge) Web() string  { return "https://github.com/o/r" }
func (reviewForge) PullRequests(context.Context) ([]PullRequest, error) {
	return nil, nil
}
func (reviewForge) Pull(context.Context, int) (Detail, error) { return Detail{}, nil }
func (f reviewForge) Review(context.Context, int, string, bool) (Review, bool, error) {
	return f.review, true, nil
}

// Catches a check run reading passing on an open pull request whose checks
// ran on another commit than the local branch head (D43).
func TestEnrichStaleChecks(t *testing.T) {
	forge := reviewForge{Review{Checks: []Check{{Name: "test", State: ChecksPassing}}}}
	c := Change{Stage: StagePR, State: StateOpen, Checks: ChecksStale, Number: 1, URL: "https://github.com/o/r/pull/1"}
	got := Enrich(context.Background(), []Change{c}, []Forge{forge}, false)[0]
	if len(got.CheckRuns) != 1 || got.CheckRuns[0].State != ChecksStale || forge.review.Checks[0].State != ChecksPassing {
		t.Fatalf("check runs %+v, kept %+v", got.CheckRuns, forge.review.Checks)
	}
}

// Catches a draft lost or exposed across restarts, an edit of one line's
// comment replacing another's, an emptied draft left on disk, and a stale or
// orphaned part not reported as a conflict.
func TestDraftPersistence(t *testing.T) {
	state := t.TempDir()
	url := "https://github.com/owner/repo/pull/45"
	d, err := LoadDraft(state, url)
	if err != nil || !d.Empty() || d.URL != url {
		t.Fatalf("missing draft: %+v %v", d, err)
	}
	d = d.WithBody(" LGTM \n").
		WithComment(LineComment{Head: "aaa", Path: "a.go", Line: 2, Body: "new side"}).
		WithComment(LineComment{Head: "aaa", Path: "a.go", Old: true, Line: 2, Body: "old side"}).
		WithComment(LineComment{Head: "aaa", Path: "a.go", Line: 2, Body: "edited"}).
		WithReply(Reply{Thread: "T_1", Path: "b.go", Body: "done"})
	if err := SaveDraft(state, d); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(draftPath(state, url)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("draft file: %v %v", info, err)
	}
	got, err := LoadDraft(state, url)
	if err != nil || got.Body != "LGTM" || len(got.Comments) != 2 || got.Comments[1].Body != "edited" || !got.Comments[0].Old || len(got.Replies) != 1 {
		t.Fatalf("reloaded: %+v %v", got, err)
	}
	if reason := got.Conflict(Review{Head: "bbb", Threads: []Thread{{ID: "T_1"}}}); !strings.Contains(reason, "2 line comments") {
		t.Fatalf("moved head: %q", reason)
	}
	if reason := got.Conflict(Review{Head: "aaa"}); !strings.Contains(reason, "b.go") {
		t.Fatalf("gone thread: %q", reason)
	}
	got = got.WithBody("").WithReply(Reply{Thread: "T_1"})
	for _, c := range got.Comments {
		c.Body = ""
		got = got.WithComment(c)
	}
	if err := SaveDraft(state, got); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(draftPath(state, url)); !os.IsNotExist(err) {
		t.Fatalf("emptied draft kept: %v", err)
	}
}
