package changes

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Check run and commit status states beyond the rollup's.
const (
	ChecksSkipped = "skipped"
)

// Reviewer states beyond the review decisions.
const (
	ReviewCommented = "commented"
	ReviewDismissed = "dismissed"
)

// Review is a pull request's description, merge state, checks, reviewers,
// and conversation, read for the item shown, never on a list refresh. ID is
// the forge's own pull request ID, which writes name. MergeState and
// Mergeable are the forge's, lower case, such as blocked and conflicting.
// Stale says why one kept from an earlier read may be out of date, Blocked
// why the account can't post reviews (D10), and Fetched when it was read.
type Review struct {
	ID         string     `json:"id,omitempty"`
	Head       string     `json:"head"`
	Body       string     `json:"body,omitempty"`
	MergeState string     `json:"mergeState,omitempty"`
	Mergeable  string     `json:"mergeable,omitempty"`
	Checks     []Check    `json:"checks,omitempty"`
	Reviewers  []Reviewer `json:"reviewers,omitempty"`
	Comments   []Comment  `json:"comments,omitempty"`
	Threads    []Thread   `json:"threads,omitempty"`
	Blocked    string     `json:"blocked,omitempty"`
	Stale      string     `json:"-"`
	Fetched    time.Time  `json:"-"`
}

// Check is one check run or commit status on the head commit. Status is its
// exact state word, such as timed out; State is the rollup category it counts
// as, so checks: queries and glyphs keep their meaning. Run is the forge's
// check run ID.
type Check struct {
	Name        string    `json:"name"`
	State       string    `json:"state"`
	URL         string    `json:"url,omitempty"`
	Status      string    `json:"status,omitempty"`
	Run         int64     `json:"run,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitzero"`
	CompletedAt time.Time `json:"completedAt,omitzero"`
}

// Exact check state words.
const (
	CheckQueued         = "queued"
	CheckRunning        = "running"
	CheckWaiting        = "waiting"
	CheckPending        = "pending"
	CheckFailed         = "failed"
	CheckTimedOut       = "timed out"
	CheckCancelled      = "cancelled"
	CheckActionRequired = "action required"
	CheckPassed         = "passed"
	CheckSkipped        = "skipped"
	CheckNeutral        = "neutral"
	CheckStale          = "stale"
)

var checkCategories = map[string]string{
	CheckQueued: ChecksPending, CheckRunning: ChecksPending, CheckWaiting: ChecksPending, CheckPending: ChecksPending,
	CheckFailed: ChecksFailing, CheckTimedOut: ChecksFailing, CheckCancelled: ChecksFailing, CheckActionRequired: ChecksFailing,
	CheckPassed: ChecksPassing, CheckSkipped: ChecksSkipped, CheckNeutral: ChecksSkipped, CheckStale: ChecksStale,
}

// CheckCategory is the rollup category an exact state word counts as; an
// unknown word counts as failing, so it never reads as passing.
func CheckCategory(status string) string {
	return cmp.Or(checkCategories[status], ChecksFailing)
}

// Reviewer is a requested or submitted reviewer and their latest state.
// Outdated marks a review submitted on a commit other than the head.
type Reviewer struct {
	Login       string    `json:"login"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submittedAt,omitzero"`
	Outdated    bool      `json:"outdated,omitempty"`
}

// Comment is a conversation comment, a submitted review's body (State is the
// review's), or a reply in a review thread.
type Comment struct {
	Author string    `json:"author"`
	Body   string    `json:"body,omitempty"`
	State  string    `json:"state,omitempty"`
	URL    string    `json:"url,omitempty"`
	Time   time.Time `json:"time"`
}

// Thread is a review thread on a file line, on the old side when Old, its
// comments oldest first. ID is the forge's, which a reply names; Hunk is the
// diff hunk its first comment was made on, ending at its line. Line is the
// head's, or for an outdated thread the line it was made on.
type Thread struct {
	ID       string    `json:"id,omitempty"`
	Path     string    `json:"path"`
	Line     int       `json:"line,omitempty"`
	Old      bool      `json:"old,omitempty"`
	Resolved bool      `json:"resolved,omitempty"`
	Outdated bool      `json:"outdated,omitempty"`
	Hunk     string    `json:"hunk,omitempty"`
	Comments []Comment `json:"comments"`
}

// Unresolved counts the review threads still open.
func (r Review) Unresolved() int {
	n := 0
	for _, t := range r.Threads {
		if !t.Resolved {
			n++
		}
	}
	return n
}

// Entry is one Conversation entry: a comment, or a thread with its replies.
type Entry struct {
	Comment Comment
	Thread  *Thread
}

// Conversation lists unresolved threads by file and line, then comments
// and reviews oldest first, then resolved threads by file and line.
func (r Review) Conversation() []Entry {
	var open, resolved []Entry
	for i := range r.Threads {
		t := &r.Threads[i]
		if len(t.Comments) == 0 {
			continue
		}
		if t.Resolved {
			resolved = append(resolved, Entry{Comment: t.Comments[0], Thread: t})
		} else {
			open = append(open, Entry{Comment: t.Comments[0], Thread: t})
		}
	}
	byLine := func(a, b Entry) int {
		return cmp.Or(cmp.Compare(a.Thread.Path, b.Thread.Path), cmp.Compare(a.Thread.Line, b.Thread.Line))
	}
	slices.SortStableFunc(open, byLine)
	slices.SortStableFunc(resolved, byLine)
	comments := make([]Entry, len(r.Comments))
	for i, c := range r.Comments {
		comments[i] = Entry{Comment: c}
	}
	slices.SortStableFunc(comments, func(a, b Entry) int { return a.Comment.Time.Compare(b.Comment.Time) })
	return slices.Concat(open, comments, resolved)
}

// LoadReview reads a pull request change's review from the forge serving it,
// on a worker. A failed read returns what the forge last had, marked stale.
func LoadReview(ctx context.Context, c Change, forges []Forge, fresh bool) (Review, error) {
	f := forgeFor(c, forges)
	if c.Number == 0 || f == nil {
		return Review{}, errors.New("no forge serves " + cmp.Or(c.URL, "this change"))
	}
	r, ok, err := f.Review(ctx, c.Number, c.Head, fresh)
	if err != nil {
		if !ok || ctx.Err() != nil {
			return Review{}, err
		}
		r.Stale = "pull request detail may be out of date: " + err.Error()
	}
	if _, ok := f.(ReviewWriter); !ok {
		r.Blocked = f.Name() + " can't post reviews"
	}
	// Stale checks never read as passing: a kept read may be for another
	// head, or the checks ran on another commit than the local branch head.
	if r.Stale != "" || c.Checks == ChecksStale {
		r.Checks = slices.Clone(r.Checks)
		for i := range r.Checks {
			if r.Checks[i].State == ChecksPassing {
				r.Checks[i].State, r.Checks[i].Status = ChecksStale, CheckStale
			}
		}
	}
	return r, nil
}

// forgeFor is the forge whose web URL c's pull request is under.
func forgeFor(c Change, forges []Forge) Forge {
	for _, f := range forges {
		if web := f.Web(); web != "" && strings.HasPrefix(strings.ToLower(c.URL), strings.ToLower(web)+"/") {
			return f
		}
	}
	return nil
}

// Verdicts: whether a pull request can land, from the forge's merge state
// first (design.md D46).
const (
	VerdictReady     = "ready"
	VerdictUnstable  = "unstable"
	VerdictRunning   = "running"
	VerdictBlocked   = "blocked"
	VerdictBehind    = "behind"
	VerdictConflicts = "conflicts"
	VerdictDraft     = "draft"
	VerdictMerged    = "merged"
	VerdictClosed    = "closed"
	VerdictChecking  = "checking"
)

// Reason kinds, in the order Verdict lists them: what to fix first.
const (
	ReasonFailing    = "failing"
	ReasonChanges    = "changes"
	ReasonThreads    = "threads"
	ReasonReview     = "review"
	ReasonPending    = "pending"
	ReasonConflicts  = "conflicts"
	ReasonBehind     = "behind"
	ReasonProtection = "protection"
	ReasonStale      = "stale"
	ReasonReady      = "ready"
)

// Merge is a pull request's verdict and the reasons for it. State is the
// forge's merge state; Check and Login name the check or reviewer a reason is
// about.
type Merge struct {
	State   string   `json:"state,omitempty"`
	Verdict string   `json:"verdict"`
	Reasons []Reason `json:"reasons"`
}

type Reason struct {
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Check string `json:"check,omitempty"`
	Login string `json:"login,omitempty"`
}

// Verdict says whether c can land and what's left, from the forge's merge
// state, then r's checks, reviewers, and threads, and c's review decision.
// The forge decides mergeability; without a merge state the verdict comes
// from mergeable, checks, and the review decision. A stale read or stale
// checks never read Ready, and a draft never does.
func Verdict(c Change, r Review) Merge {
	m := Merge{State: r.MergeState, Reasons: []Reason{}}
	switch c.State {
	case StateMerged:
		m.Verdict = VerdictMerged
		return m
	case StateClosed:
		m.Verdict = VerdictClosed
		return m
	case StateUnknown, StateStale:
		m.Verdict = VerdictChecking
		return m
	}
	add := func(kind, text, check, login string) {
		m.Reasons = append(m.Reasons, Reason{Kind: kind, Text: text, Check: check, Login: login})
	}
	checks := Ordered(r.Checks)
	failing, pending := 0, 0
	for _, check := range checks {
		if check.State == ChecksFailing {
			failing++
			add(ReasonFailing, check.Name+" "+cmp.Or(check.Status, CheckFailed), check.Name, "")
		}
	}
	changes := 0
	for _, reviewer := range r.Reviewers {
		if reviewer.State == ReviewChanges {
			changes++
			add(ReasonChanges, reviewer.Login+" requested changes", "", reviewer.Login)
		}
	}
	if changes == 0 && c.Review == ReviewChanges {
		changes++
		add(ReasonChanges, "changes requested", "", "")
	}
	if n := r.Unresolved(); n == 1 {
		add(ReasonThreads, "1 unresolved thread", "", "")
	} else if n > 1 {
		add(ReasonThreads, strconv.Itoa(n)+" unresolved threads", "", "")
	}
	required := changes == 0 && c.Review == ReviewRequested
	if required {
		add(ReasonReview, "review required", "", "")
	}
	for _, check := range checks {
		if check.State == ChecksPending {
			pending++
			add(ReasonPending, check.Name+" "+cmp.Or(check.Status, CheckPending), check.Name, "")
		}
	}
	conflicts := r.MergeState == "dirty" || r.Mergeable == "conflicting"
	if conflicts {
		add(ReasonConflicts, "conflicts with the base branch", "", "")
	}
	if r.MergeState == "behind" {
		add(ReasonBehind, "behind the base branch", "", "")
	}
	blocking := failing > 0 || changes > 0 || required
	switch {
	case c.State == StateDraft || r.MergeState == "draft":
		m.Verdict = VerdictDraft
	case conflicts:
		m.Verdict = VerdictConflicts
	case r.MergeState == "behind":
		m.Verdict = VerdictBehind
	case r.MergeState == "unstable":
		m.Verdict = VerdictUnstable
	case r.MergeState == "blocked" && !blocking && pending > 0:
		m.Verdict = VerdictRunning
	case r.MergeState == "blocked":
		m.Verdict = VerdictBlocked
		if !blocking {
			add(ReasonProtection, "blocked by branch protection", "", "")
		}
	case r.MergeState == "clean" || r.MergeState == "has_hooks":
		m.Verdict = VerdictReady
	case r.MergeState != "":
		m.Verdict = VerdictChecking
	case blocking:
		m.Verdict = VerdictBlocked
	case pending > 0:
		m.Verdict = VerdictRunning
	case r.Mergeable == "mergeable":
		m.Verdict = VerdictReady
	default:
		m.Verdict = VerdictChecking
	}
	stale := r.Stale != "" || c.Checks == ChecksStale || slices.ContainsFunc(r.Checks, func(c Check) bool { return c.State == ChecksStale })
	if stale {
		add(ReasonStale, "pull request detail may be out of date", "", "")
		if m.Verdict == VerdictReady {
			m.Verdict = VerdictChecking
		}
	}
	if m.Verdict == VerdictReady {
		add(ReasonReady, "ready to merge", "", "")
	}
	return m
}

// Ordered is checks problems first: failing, then pending, stale, passing,
// and skipped, each in the forge's order.
func Ordered(checks []Check) []Check {
	rank := map[string]int{ChecksFailing: 0, ChecksPending: 1, ChecksStale: 2, ChecksPassing: 3, ChecksSkipped: 4}
	out := slices.Clone(checks)
	slices.SortStableFunc(out, func(a, b Check) int { return cmp.Compare(rank[a.State], rank[b.State]) })
	return out
}

// ListMerge is an open pull request's verdict from the list read alone, for
// the list glyph and merge: (design.md D46): ready, unstable, blocked,
// behind, or conflicts. It is empty for anything else, such as a draft, an
// unknown or stale state, or a merge state the forge is still computing, and
// never ready while its checks are stale.
func ListMerge(c Change) string {
	if c.Stage != StagePR || c.State != StateOpen {
		return ""
	}
	verdict := map[string]string{"clean": VerdictReady, "has_hooks": VerdictReady, "unstable": VerdictUnstable, "blocked": VerdictBlocked, "behind": VerdictBehind, "dirty": VerdictConflicts}[c.MergeState]
	if verdict == VerdictReady && c.Checks == ChecksStale {
		return ""
	}
	return verdict
}
