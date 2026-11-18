package history

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/textutil"
)

// maxReviewReads bounds the pull requests one history load reads reviews for.
const maxReviewReads = 10

// readReviews reads the reviews of cs's pull requests, the most recently
// updated first, through review, which the forge caches per head. A failed or
// stale read, or pull requests past maxReviewReads, makes the result partial.
func readReviews(ctx context.Context, cs []changes.Change, review func(context.Context, changes.Change) (changes.Review, error)) (map[string]changes.Review, error) {
	var pulls []changes.Change
	for _, c := range cs {
		if c.Number > 0 && review != nil {
			pulls = append(pulls, c)
		}
	}
	slices.SortStableFunc(pulls, func(a, b changes.Change) int { return b.Updated.Compare(a.Updated) })
	var problems []string
	if len(pulls) > maxReviewReads {
		problems = append(problems, fmt.Sprintf("reviews read for the %d newest pull requests", maxReviewReads))
		pulls = pulls[:maxReviewReads]
	}
	out := map[string]changes.Review{}
	for _, c := range pulls {
		r, err := review(ctx, c)
		switch {
		case err != nil:
			problems = append(problems, changes.Label(c)+": "+err.Error())
			continue
		case r.Stale != "":
			problems = append(problems, changes.Label(c)+": "+r.Stale)
		}
		out[c.ID] = r
	}
	if len(problems) > 0 {
		return out, PartialError(errors.New(textutil.Line(strings.Join(problems, "; "))))
	}
	return out, nil
}

// changeEntries make one group per linked change (design.md D43). Its head
// spans the change from its first event to its merge, close, or last landed
// commit; its members are those events, the commits it landed, which share
// IDs with the git provider's so a commit appears once, and its pull
// request's reviews, comments, and review threads, which leave the span as is.
func changeEntries(cs []changes.Change, reviews map[string]changes.Review) []Entry {
	var out []Entry
	for _, c := range cs {
		id := "change:" + c.ID
		var members []Entry
		for _, e := range c.Events {
			members = append(members, Entry{ID: id + ":" + e.Kind, Time: e.Time, Kind: "change." + e.Kind, Group: id, Summary: changeEvent(c, e.Kind)})
		}
		for _, commit := range c.Commits {
			members = append(members, Entry{ID: commitID(commit.SHA), Time: commit.Time, Kind: "commit", Group: id, Summary: textutil.Line(commit.Subject), Detail: commit.SHA})
		}
		span := &Span{State: c.State}
		if c.Stage == changes.StageLanded && c.State == changes.StateMerged {
			span.State = "landed"
		}
		ended := c.State == changes.StateMerged || c.State == changes.StateClosed
		for _, m := range members {
			if m.Time.IsZero() {
				continue
			}
			if span.Start.IsZero() || m.Time.Before(span.Start) {
				span.Start = m.Time
			}
			if ended && m.Time.After(span.End) {
				span.End = m.Time
			}
		}
		if span.Start.IsZero() {
			span.Start = c.Updated
		}
		// A change with no known time has no place on a timeline; the Changes
		// tab still lists it.
		if span.Start.IsZero() {
			continue
		}
		out = append(out, Entry{ID: id, Time: span.Start, Kind: "change", Span: span, Summary: changeSummary(c, span.State), Detail: changeDetail(c)})
		out = append(out, members...)
		if r, ok := reviews[c.ID]; ok {
			out = append(out, reviewEntries(id, r)...)
		}
	}
	return out
}

var reviewVerbs = map[string]string{
	changes.ReviewApproved: "approved", changes.ReviewChanges: "requested changes",
	changes.ReviewCommented: "commented", changes.ReviewDismissed: "review dismissed",
}

// reviewEntries are a pull request's reviews, conversation comments, and
// review threads, as members of group. A review with a body is a comment
// carrying the review's state; a bodyless verdict is known only from the
// reviewer's latest review, and a bodyless comment review only holds threads.
func reviewEntries(group string, r changes.Review) []Entry {
	var out []Entry
	member := func(key string, t time.Time, kind, actor, summary, detail string) {
		if !t.IsZero() {
			out = append(out, Entry{ID: group + ":" + key, Time: t, Kind: kind, Group: group, Actor: textutil.Line(actor), Summary: summary, Detail: detail})
		}
	}
	key := func(c changes.Comment) string { return cmp.Or(c.URL, c.Author+"@"+c.Time.Format(time.RFC3339Nano)) }
	stated := map[string]bool{}
	for _, c := range r.Comments {
		if c.State == "" {
			member("comment:"+key(c), c.Time, "review.comment", c.Author, "commented", c.Body)
			continue
		}
		stated[c.Author+"\x00"+c.State+"\x00"+c.Time.String()] = true
		member("review:"+key(c), c.Time, "review."+c.State, c.Author, cmp.Or(reviewVerbs[c.State], c.State), c.Body)
	}
	for _, v := range r.Reviewers {
		if v.State == changes.ReviewCommented || reviewVerbs[v.State] == "" || stated[v.Login+"\x00"+v.State+"\x00"+v.SubmittedAt.String()] {
			continue
		}
		member("review:"+v.Login+"@"+v.SubmittedAt.Format(time.RFC3339Nano), v.SubmittedAt, "review."+v.State, v.Login, reviewVerbs[v.State], "")
	}
	for _, t := range r.Threads {
		if len(t.Comments) == 0 {
			continue
		}
		first := t.Comments[0]
		summary := "commented on " + textutil.Line(t.Path)
		if t.Line > 0 {
			summary += ":" + strconv.Itoa(t.Line)
		}
		if n := len(t.Comments); n > 1 {
			summary += " · " + strconv.Itoa(n) + " comments"
		}
		if t.Resolved {
			summary += " · resolved"
		}
		detail := first.Body
		if len(t.Comments) > 1 {
			parts := make([]string, len(t.Comments))
			for i, c := range t.Comments {
				parts[i] = textutil.Line(c.Author) + ":\n" + c.Body
			}
			detail = strings.Join(parts, "\n\n")
		}
		member("thread:"+cmp.Or(t.ID, key(first)), first.Time, "review.thread", first.Author, summary, detail)
	}
	return out
}

func changeSummary(c changes.Change, state string) string {
	label := textutil.Line(changes.Label(c))
	parts := []string{label}
	if title := textutil.Line(c.Title); title != "" && title != label {
		parts = append(parts, title)
	}
	parts = append(parts, state)
	if c.Checks != "" {
		parts = append(parts, "checks "+c.Checks)
	}
	switch c.Review {
	case changes.ReviewApproved:
		parts = append(parts, "approved")
	case changes.ReviewChanges:
		parts = append(parts, "changes requested")
	case changes.ReviewRequested:
		parts = append(parts, "review requested")
	}
	if c.Reason != "" {
		parts = append(parts, textutil.Line(c.Reason))
	}
	return strings.Join(parts, " · ")
}

func changeEvent(c changes.Change, kind string) string {
	switch kind {
	case "worktree":
		return "worktree " + textutil.Line(c.Worktree)
	case "branch":
		return "branch " + textutil.Line(c.Branch)
	case "opened":
		if c.Number > 0 {
			return "opened " + changes.Label(c)
		}
	}
	return kind
}

// changeDetail aligns the change's locations after the longest key, as a
// session's detail does.
func changeDetail(c changes.Change) string {
	var detail []string
	for _, field := range [][2]string{{"url", c.URL}, {"branch", c.Branch}, {"head", c.Head}, {"worktree", c.Worktree}, {"linked by", c.Link}} {
		if field[1] != "" {
			detail = append(detail, fmt.Sprintf("%-10s %s", field[0]+":", textutil.Line(field[1])))
		}
	}
	return strings.Join(detail, "\n")
}
