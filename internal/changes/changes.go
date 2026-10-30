// Package changes finds the worktrees, branches, pull requests, and landed
// commits that carry an item's work (design.md D43). Links are derived from
// branch names, PR titles and bodies, closing references, commit trailers,
// first-parent merges, and explicit refs URLs, and kept only in memory and the
// cache, never in item files.
package changes

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/model"
)

// Stages, from earliest to furthest.
const (
	StageWorktree = "worktree"
	StageBranch   = "branch"
	StagePR       = "pr"
	StageLanded   = "landed"
)

// States. Unknown and stale never read as merged, and stale checks never read
// as passing.
const (
	StateOpen    = "open"
	StateDraft   = "draft"
	StateMerged  = "merged"
	StateClosed  = "closed"
	StateUnknown = "unknown"
	StateStale   = "stale"
)

// Checks and review decisions.
const (
	ChecksPassing   = "passing"
	ChecksFailing   = "failing"
	ChecksPending   = "pending"
	ChecksStale     = "stale"
	ReviewApproved  = "approved"
	ReviewChanges   = "changes"
	ReviewRequested = "requested"
)

// Change is one line of work linked to an item, shown at its furthest stage:
// a worktree, branch, pull request, and merge that share a head branch or
// commit are one change. Link says how it was found: branch, title, closes,
// provider, trailer, merge, or ref.
type Change struct {
	ID        string `json:"id"`
	Stage     string `json:"stage"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Checks    string `json:"checks,omitempty"`
	Review    string `json:"review,omitempty"`
	Link      string `json:"link"`
	Branch    string `json:"branch,omitempty"`
	Head      string `json:"head,omitempty"`
	Title     string `json:"title,omitempty"`
	URL       string `json:"url,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Number    int    `json:"number,omitempty"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
	Worktree  string `json:"worktree,omitempty"`
	Dirty     *int   `json:"dirty,omitempty"`
	Ahead     *int   `json:"ahead,omitempty"`
	Behind    *int   `json:"behind,omitempty"`
	// CheckRuns and Unresolved are an open pull request's checks and
	// unresolved review threads, read for the item shown.
	CheckRuns  []Check   `json:"checkRuns,omitempty"`
	Unresolved *int      `json:"unresolved,omitempty"`
	Commits    []Commit  `json:"commits,omitempty"`
	Events     []Event   `json:"events,omitempty"`
	Updated    time.Time `json:"updated,omitzero"`
	// Tour is the tour kept for the change, read for bl changes.
	Tour *Tour `json:"tour,omitempty"`
	// ref is the branch ref ahead/behind compares with base, the default
	// branch, in the checkout dir.
	ref, base, dir string
}

// Tour is a stored tour's head and step count; it is stale once the change's
// head is another commit.
type Tour struct {
	Ref   string `json:"ref"`
	Steps int    `json:"steps"`
	Stale bool   `json:"stale"`
}

// Base is the default branch ref an open branch is compared with.
func (c Change) Base() string { return c.base }

// Dir is the checkout root the change was found in.
func (c Change) Dir() string { return c.dir }

// IsCommit is whether the change is one commit, such as one opened from
// History or a change's Commits.
func (c Change) IsCommit() bool { return strings.HasPrefix(c.ID, "commit:") }

// Commit is a commit on the default branch's first-parent history, or one a
// change's detail lists.
type Commit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Author  string    `json:"author,omitempty"`
	Time    time.Time `json:"time"`
	// Body is the message after the subject, read only for a commit shown
	// on its own, or as a pull request's forge sends it.
	Body string `json:"body,omitempty"`
	// Branch and PR are what a merge's subject names; Trailers are the values
	// of the project's commit trailer.
	Branch   string   `json:"-"`
	PR       int      `json:"-"`
	Trailers []string `json:"-"`
}

// Event is a moment in a change's life: worktree, branch, opened, merged, or
// closed.
type Event struct {
	Kind string    `json:"kind"`
	Time time.Time `json:"time"`
}

// PullRequest is a forge's pull request, as its adapter decodes it. State is
// open, merged, or closed; Closes holds the provider node IDs of the issues it
// closes; Bot marks an author that is an app, not a person; Stale is why one kept from a refresh that failed may be out of date.
type PullRequest struct {
	Repo            string    `json:"repo"`
	Number          int       `json:"number"`
	Title           string    `json:"title"`
	Body            string    `json:"body,omitempty"`
	URL             string    `json:"url"`
	State           string    `json:"state"`
	Draft           bool      `json:"draft,omitempty"`
	Head            string    `json:"head"`
	HeadSHA         string    `json:"headSha,omitempty"`
	MergeSHA        string    `json:"mergeSha,omitempty"`
	CrossRepository bool      `json:"crossRepository,omitempty"`
	Additions       int       `json:"additions,omitempty"`
	Deletions       int       `json:"deletions,omitempty"`
	Review          string    `json:"review,omitempty"`
	Checks          string    `json:"checks,omitempty"`
	Closes          []string  `json:"closes,omitempty"`
	Author          string    `json:"author,omitempty"`
	Bot             bool      `json:"bot,omitempty"`
	Created         time.Time `json:"created"`
	Updated         time.Time `json:"updated"`
	Merged          time.Time `json:"merged,omitzero"`
	Closed          time.Time `json:"closed,omitzero"`
	Stale           string    `json:"-"`
}

// Status is how one provider of changes fared: git or a forge.
type Status struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
}

const (
	OK          = "ok"
	Partial     = "partial"
	Unavailable = "unavailable"
)

// Rank orders stages, so the furthest change sorts first.
func Rank(stage string) int {
	return map[string]int{StageWorktree: 1, StageBranch: 2, StagePR: 3, StageLanded: 4}[stage]
}

// Furthest is the item's most advanced change, the newest of those at the
// furthest stage.
func Furthest(cs []Change) (Change, bool) {
	if len(cs) == 0 {
		return Change{}, false
	}
	return slices.MinFunc(cs, order), true
}

// order sorts changes furthest stage first, then newest first.
func order(a, b Change) int {
	return cmp.Or(cmp.Compare(Rank(b.Stage), Rank(a.Stage)), b.Updated.Compare(a.Updated), cmp.Compare(a.ID, b.ID))
}

// Item is what linking needs from an item: its reference, the provider node
// ID its forge's closing references name, and its refs URLs.
type Item struct {
	Ref    model.Ref
	NodeID string
	Refs   []string
}
