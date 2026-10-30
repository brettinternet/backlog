package changes

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/git"
	"github.com/brettinternet/backlog/internal/project"
)

// maxLanded bounds the first-parent history read for landed commits; older
// commits make the git provider partial.
const maxLanded = 1000

// Options locate one project's changes. Git reads local refs, worktrees, and
// landed commits; Forges list pull requests.
type Options struct {
	Discovery project.Discovery
	CacheDir  string
	Trailer   string
	Git       bool
	Forges    []Forge
}

// Forge lists a repository's pull requests, newest updated first, keeping its
// own incremental cache. On failure it returns what it last had with the
// error.
type Forge interface {
	Name() string
	// Web is the URL its pull request, commit, and tree pages hang off:
	// the repository's, or with /- appended for GitLab.
	Web() string
	PullRequests(ctx context.Context) ([]PullRequest, error)
	// Pull reads one pull request's files and commits.
	Pull(ctx context.Context, number int) (Detail, error)
	// Review reads one pull request's review, reusing a recent read for the
	// same head unless fresh. On failure it returns the last read, if any,
	// with ok, and the error.
	Review(ctx context.Context, number int, head string, fresh bool) (r Review, ok bool, err error)
}

// Data is one project's raw changes, before linking. Default is the default
// branch and DefaultRef the ref its history was read from; Repo is origin's
// repository and Web its forge URL, for commit and branch links.
type Data struct {
	Default, DefaultRef, Repo, Web string
	Directory                      string
	Branches                       []Branch
	Landed                         []Commit
	LandedHead                     string
	PullRequests                   []PullRequest
	Statuses                       []Status
}

// Branch is a branch seen locally or as an already-fetched remote-tracking
// ref, with the worktree it is checked out in.
type Branch struct {
	Name, Local, Remote, RemoteRef string
	Worktree                       string
	Created, WorktreeCreated       time.Time
}

// LoadLocal reads o's refs and worktrees from .git and, when the default
// branch moved since previous, its first-parent history with one bounded git
// log, cached by its head commit.
func LoadLocal(ctx context.Context, o Options, previous Data) Data {
	host, repo := project.Origin(o.Discovery)
	d := Data{Directory: o.Discovery.CheckoutRoot, Repo: repo}
	for _, f := range o.Forges {
		if repo != "" && strings.EqualFold(strings.TrimSuffix(f.Web(), "/-"), "https://"+host+"/"+repo) {
			d.Web = f.Web()
		}
	}
	if !o.Git {
		return d
	}
	common := o.Discovery.CommonDir
	if common == "" {
		d.Statuses = []Status{{Provider: "git", State: Unavailable, Reason: "not in a Git repository"}}
		return d
	}
	heads, err := git.Refs(common, "refs/heads/")
	var remotes map[string]string
	if err == nil {
		remotes, err = git.Refs(common, "refs/remotes/")
	}
	if err != nil {
		d.Statuses = []Status{{Provider: "git", State: Unavailable, Reason: err.Error()}}
		return d
	}
	d.Default = defaultBranch(common, heads, remotes)
	if d.Default != "" {
		d.DefaultRef = "refs/heads/" + d.Default
		if heads[d.DefaultRef] == "" {
			d.DefaultRef = "refs/remotes/origin/" + d.Default
		}
	}
	byName := map[string]*Branch{}
	branch := func(name string) *Branch {
		if b := byName[name]; b != nil {
			return b
		}
		byName[name] = &Branch{Name: name}
		return byName[name]
	}
	for ref, sha := range heads {
		name := strings.TrimPrefix(ref, "refs/heads/")
		if name == d.Default {
			continue
		}
		b := branch(name)
		b.Local, b.Created = sha, git.FirstSeen(filepath.Join(common, "logs", filepath.FromSlash(ref)))
	}
	for ref, sha := range remotes {
		remote, name, ok := strings.Cut(strings.TrimPrefix(ref, "refs/remotes/"), "/")
		if !ok || name == "HEAD" || name == d.Default {
			continue
		}
		// origin wins when several remotes carry the branch.
		if b := branch(name); b.Remote == "" || remote == "origin" {
			b.Remote, b.RemoteRef = sha, ref
		}
	}
	for _, w := range git.Worktrees(common) {
		if w.Branch == d.Default {
			continue
		}
		b := branch(w.Branch)
		b.Worktree, b.WorktreeCreated = w.Path, w.Created
		if b.Local == "" {
			b.Local = w.Head
		}
	}
	for _, b := range byName {
		d.Branches = append(d.Branches, *b)
	}
	status := Status{Provider: "git", State: OK}
	if d.DefaultRef == "" {
		status = Status{Provider: "git", State: Partial, Reason: "no default branch, so nothing reads as landed"}
	} else {
		head := heads[d.DefaultRef]
		if head == "" {
			head = remotes[d.DefaultRef]
		}
		d.LandedHead = head
		if previous.LandedHead == head && previous.DefaultRef == d.DefaultRef && head != "" {
			d.Landed = previous.Landed
			status = previousGit(previous.Statuses)
		} else {
			var reason string
			d.Landed, reason, err = landed(ctx, o, d.DefaultRef, head)
			switch {
			case err != nil:
				status = Status{Provider: "git", State: Unavailable, Reason: err.Error()}
				d.LandedHead = ""
			case reason != "":
				status = Status{Provider: "git", State: Partial, Reason: reason}
			}
		}
	}
	d.Statuses = []Status{status}
	return d
}

func previousGit(statuses []Status) Status {
	for _, s := range statuses {
		if s.Provider == "git" {
			return s
		}
	}
	return Status{Provider: "git", State: OK}
}

// defaultBranch is origin's HEAD, else main, else master.
func defaultBranch(common string, heads, remotes map[string]string) string {
	if target := git.SymbolicTarget(common, "refs/remotes/origin/HEAD"); target != "" {
		return strings.TrimPrefix(target, "refs/remotes/origin/")
	}
	for _, name := range []string{"main", "master"} {
		if heads["refs/heads/"+name] != "" || remotes["refs/remotes/origin/"+name] != "" {
			return name
		}
	}
	return ""
}

type landedCache struct {
	Ref, Head, Trailer string
	Log                []byte
}

// landed reads ref's first-parent history once per head commit, from the
// cache when the CLI ran it before.
func landed(ctx context.Context, o Options, ref, head string) ([]Commit, string, error) {
	path := ""
	var out []byte
	if o.CacheDir != "" && head != "" {
		sum := sha256.Sum256([]byte(o.Discovery.CommonDir))
		path = filepath.Join(o.CacheDir, "changes", hex.EncodeToString(sum[:16])+".json")
		var c landedCache
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &c) == nil && c.Ref == ref && c.Head == head && c.Trailer == o.Trailer {
			out = c.Log
		}
	}
	if out == nil {
		format := "--format=%H%x1f%cI%x1f%P%x1f%s"
		if o.Trailer != "" {
			format += "%x1f%(trailers:key=" + o.Trailer + ",valueonly,unfold,separator=%x1e)"
		}
		var err error
		out, err = git.Run(ctx, o.Discovery.CommonDir, "log", "--first-parent", "--no-color", fmt.Sprintf("--max-count=%d", maxLanded+1), format, ref, "--")
		if err != nil {
			return nil, "", err
		}
		if path != "" {
			if data, err := json.Marshal(landedCache{Ref: ref, Head: head, Trailer: o.Trailer, Log: out}); err == nil {
				cache.WriteFile(path, data)
			}
		}
	}
	commits := parseLanded(out)
	reason := ""
	if len(commits) > maxLanded {
		commits, reason = commits[:maxLanded], fmt.Sprintf("newest %d commits of %s", maxLanded, strings.TrimPrefix(ref, "refs/heads/"))
	}
	return commits, reason, nil
}

func parseLanded(out []byte) []Commit {
	var commits []Commit
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\x1f")
		if len(fields) < 4 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, fields[1])
		c := Commit{SHA: fields[0], Time: at.UTC(), Subject: fields[3]}
		if len(strings.Fields(fields[2])) > 1 {
			c.Branch, c.PR = MergedBranch(c.Subject)
		}
		if len(fields) > 4 {
			for value := range strings.SplitSeq(fields[4], "\x1e") {
				if value = strings.TrimSpace(value); value != "" {
					c.Trailers = append(c.Trailers, value)
				}
			}
		}
		commits = append(commits, c)
	}
	return commits
}

var (
	mergeBranch  = regexp.MustCompile(`^Merge branch '([^']+)'`)
	mergeRemote  = regexp.MustCompile(`^Merge remote-tracking branch '[^/']+/([^']+)'`)
	mergeRequest = regexp.MustCompile(`^Merge pull request #(\d+) from [^/\s]+/(\S+)`)
)

// MergedBranch is the branch a merge commit's subject names, and the pull
// request number when GitHub wrote it.
func MergedBranch(subject string) (string, int) {
	if m := mergeRequest.FindStringSubmatch(subject); m != nil {
		n, _ := strconv.Atoi(m[1])
		return m[2], n
	}
	if m := mergeRemote.FindStringSubmatch(subject); m != nil {
		return m[1], 0
	}
	if m := mergeBranch.FindStringSubmatch(subject); m != nil {
		return m[1], 0
	}
	return "", 0
}

// Enrich adds what only Git and the forges can say about one item's changes,
// on a worker:
// how far each open branch is ahead of and behind the default branch, how
// many lines a branch or worktree adds and deletes since its merge base, and
// how many files are dirty in each worktree, and each open pull request's
// checks and unresolved threads, read again from the forge when fresh.
func Enrich(ctx context.Context, cs []Change, forges []Forge, fresh bool) []Change {
	out := slices.Clone(cs)
	for i := range out {
		c := &out[i]
		if c.State != StateOpen && c.State != StateDraft || ctx.Err() != nil {
			continue
		}
		if c.ref != "" && c.base != "" && c.dir != "" {
			if raw, err := git.Run(ctx, c.dir, "rev-list", "--left-right", "--count", c.base+"..."+c.ref, "--"); err == nil {
				if fields := strings.Fields(string(raw)); len(fields) == 2 {
					behind, err1 := strconv.Atoi(fields[0])
					ahead, err2 := strconv.Atoi(fields[1])
					if err1 == nil && err2 == nil {
						c.Ahead, c.Behind = &ahead, &behind
					}
				}
			}
			if c.Stage == StageWorktree || c.Stage == StageBranch {
				if files, _, err := localFiles(ctx, *c, false); err == nil {
					c.Additions, c.Deletions = Size(files)
				}
			}
		}
		if c.Stage == StagePR && (c.State == StateOpen || c.State == StateDraft) {
			if r, err := LoadReview(ctx, *c, forges, fresh); err != nil {
				c.Reason = cmp.Or(c.Reason, "pull request detail unavailable: "+err.Error())
			} else {
				unresolved := r.Unresolved()
				c.CheckRuns, c.Unresolved, c.Reason = r.Checks, &unresolved, cmp.Or(c.Reason, r.Stale)
			}
		}
		if c.Worktree != "" {
			// A repository filter leaves the dirty count unknown rather than run.
			if filtered, err := git.RepoFilter(ctx, c.Worktree); err == nil && !filtered {
				if raw, err := git.Run(ctx, c.Worktree, "status", "--porcelain", "--untracked-files=normal", "--ignore-submodules=all", "--no-renames"); err == nil {
					n := bytes.Count(raw, []byte("\n"))
					c.Dirty = &n
				}
			}
		}
	}
	return out
}
