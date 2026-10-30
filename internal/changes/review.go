package changes

import (
	"cmp"
	"context"
	"errors"
	"slices"
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

// Review is a pull request's description, checks, reviewers, and
// conversation, read for the item shown, never on a list refresh. Stale says
// why one kept from an earlier read may be out of date.
type Review struct {
	Head      string     `json:"head"`
	Body      string     `json:"body,omitempty"`
	Checks    []Check    `json:"checks,omitempty"`
	Reviewers []Reviewer `json:"reviewers,omitempty"`
	Comments  []Comment  `json:"comments,omitempty"`
	Threads   []Thread   `json:"threads,omitempty"`
	Stale     string     `json:"-"`
}

// Check is one check run or commit status on the head commit.
type Check struct {
	Name  string `json:"name"`
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

// Reviewer is a requested or submitted reviewer and their latest state.
type Reviewer struct {
	Login string `json:"login"`
	State string `json:"state"`
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

// Thread is a review thread on a file line, its comments oldest first.
type Thread struct {
	Path     string    `json:"path"`
	Line     int       `json:"line,omitempty"`
	Resolved bool      `json:"resolved,omitempty"`
	Outdated bool      `json:"outdated,omitempty"`
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

// Conversation lists unresolved threads first, then comments and resolved
// threads, each oldest first.
func (r Review) Conversation() []Entry {
	var out []Entry
	for _, c := range r.Comments {
		out = append(out, Entry{Comment: c})
	}
	for i := range r.Threads {
		t := &r.Threads[i]
		if len(t.Comments) > 0 {
			out = append(out, Entry{Comment: t.Comments[0], Thread: t})
		}
	}
	open := func(e Entry) bool { return e.Thread != nil && !e.Thread.Resolved }
	slices.SortStableFunc(out, func(a, b Entry) int {
		if open(a) != open(b) {
			if open(a) {
				return -1
			}
			return 1
		}
		return a.Comment.Time.Compare(b.Comment.Time)
	})
	return out
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
	return r, nil
}

// forgeFor is the forge whose web URL c's pull request is under.
func forgeFor(c Change, forges []Forge) Forge {
	for _, f := range forges {
		if web := f.Web(); web != "" && strings.HasPrefix(strings.ToLower(c.URL), strings.ToLower(web)+"/pull/") {
			return f
		}
	}
	return nil
}
