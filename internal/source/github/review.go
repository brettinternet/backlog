package github

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/textutil"
)

const reviewQuery = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){body headRefOid
reviewRequests(first:50){nodes{requestedReviewer{__typename ... on User{login} ... on Bot{login} ... on Mannequin{login} ... on Team{slug}}}}
latestReviews(first:50){nodes{author{login} state}}
reviews(first:100){nodes{author{login} body state submittedAt url}}
comments(first:100){nodes{author{login} body createdAt url}}
reviewThreads(first:100){nodes{isResolved isOutdated path line originalLine comments(first:50){nodes{author{login} body createdAt url}}}}
commits(last:1){nodes{commit{statusCheckRollup{contexts(first:100){nodes{__typename ... on CheckRun{name status conclusion detailsUrl url} ... on StatusContext{context state targetUrl}}}}}}}}}}`

type reviewAuthor struct{ Login string }

type reviewComment struct {
	Author    *reviewAuthor
	Body, URL string
	CreatedAt time.Time
}

func (c reviewComment) comment() changes.Comment {
	author := "ghost"
	if c.Author != nil {
		author = c.Author.Login
	}
	return changes.Comment{Author: textutil.Line(author), Body: textutil.Sanitize(c.Body), URL: textutil.Line(c.URL), Time: c.CreatedAt.UTC()}
}

type reviewNode struct {
	Body, HeadRefOid string
	ReviewRequests   struct {
		Nodes []struct {
			RequestedReviewer *struct{ Login, Slug string }
		}
	}
	LatestReviews struct {
		Nodes []struct {
			Author *reviewAuthor
			State  string
		}
	}
	Reviews struct {
		Nodes []struct {
			reviewComment
			State       string
			SubmittedAt time.Time
		}
	}
	Comments      struct{ Nodes []reviewComment }
	ReviewThreads struct {
		Nodes []struct {
			IsResolved, IsOutdated bool
			Path                   string
			Line, OriginalLine     *int
			Comments               struct{ Nodes []reviewComment }
		}
	}
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []struct {
							Typename                                  string `json:"__typename"`
							Name, Status, Conclusion, DetailsURL, URL string
							Context, State, TargetURL                 string
						}
					}
				}
			}
		}
	}
}

var reviewStates = map[string]string{
	"APPROVED": changes.ReviewApproved, "CHANGES_REQUESTED": changes.ReviewChanges,
	"COMMENTED": changes.ReviewCommented, "DISMISSED": changes.ReviewDismissed,
}

// checkState maps a check run's conclusion, or a commit status's state, as
// the rollup does: unfinished runs are pending, and stale never passes.
func checkState(status, conclusion string) string {
	if status != "" && status != "COMPLETED" {
		return changes.ChecksPending
	}
	switch conclusion {
	case "SUCCESS":
		return changes.ChecksPassing
	case "NEUTRAL", "SKIPPED":
		return changes.ChecksSkipped
	case "STALE":
		return changes.ChecksStale
	case "PENDING", "EXPECTED":
		return changes.ChecksPending
	}
	return changes.ChecksFailing
}

func (n reviewNode) review() changes.Review {
	r := changes.Review{Head: n.HeadRefOid, Body: textutil.Sanitize(n.Body)}
	states := map[string]string{}
	var order []string
	set := func(login, state string) {
		if login == "" || state == "" {
			return
		}
		if _, ok := states[login]; !ok {
			order = append(order, login)
		}
		states[login] = state
	}
	for _, n := range n.LatestReviews.Nodes {
		if n.Author != nil {
			set(n.Author.Login, reviewStates[n.State])
		}
	}
	// A re-requested reviewer is waiting again, whatever they said before.
	for _, n := range n.ReviewRequests.Nodes {
		if n.RequestedReviewer != nil {
			set(cmp.Or(n.RequestedReviewer.Login, n.RequestedReviewer.Slug), changes.ReviewRequested)
		}
	}
	for _, login := range order {
		r.Reviewers = append(r.Reviewers, changes.Reviewer{Login: textutil.Line(login), State: states[login]})
	}
	for _, n := range n.Reviews.Nodes {
		if strings.TrimSpace(n.Body) == "" {
			continue
		}
		c := n.comment()
		c.State, c.Time = reviewStates[n.State], n.SubmittedAt.UTC()
		r.Comments = append(r.Comments, c)
	}
	for _, n := range n.Comments.Nodes {
		r.Comments = append(r.Comments, n.comment())
	}
	for _, n := range n.ReviewThreads.Nodes {
		t := changes.Thread{Path: textutil.Line(n.Path), Resolved: n.IsResolved, Outdated: n.IsOutdated}
		if line := cmp.Or(n.Line, n.OriginalLine); line != nil {
			t.Line = *line
		}
		for _, c := range n.Comments.Nodes {
			t.Comments = append(t.Comments, c.comment())
		}
		r.Threads = append(r.Threads, t)
	}
	if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		for _, c := range n.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes {
			check := changes.Check{Name: c.Name, State: checkState(c.Status, c.Conclusion), URL: cmp.Or(c.DetailsURL, c.URL)}
			if c.Typename == "StatusContext" {
				check = changes.Check{Name: c.Context, State: checkState("", c.State), URL: c.TargetURL}
			}
			check.Name, check.URL = textutil.Line(check.Name), textutil.Line(check.URL)
			r.Checks = append(r.Checks, check)
		}
	}
	return r
}

// reviewError tolerates checks or a team reviewer the token can't read; the
// rest of the review still shows.
func reviewError(kind string, path []any) bool {
	if kind != "FORBIDDEN" && kind != "NOT_FOUND" || len(path) < 3 || path[0] != "repository" || path[1] != "pullRequest" {
		return false
	}
	return path[2] == "commits" || path[2] == "reviewRequests"
}

// Review reads one pull request's description, checks, reviewers, and
// conversation through the account's request queue, redacted before it is
// kept or shown. A read for the same head in the last minute is reused unless
// fresh; a failed read returns the last one kept with the error.
func (f *Forge) Review(ctx context.Context, number int, head string, fresh bool) (changes.Review, bool, error) {
	return f.reviews.Get(number, head, fresh, func() (changes.Review, error) { return f.readReview(ctx, number) })
}

func (f *Forge) readReview(ctx context.Context, number int) (changes.Review, error) {
	c, err := f.s.authenticate(ctx)
	if err != nil {
		return changes.Review{}, err
	}
	vars := f.s.variables()
	vars["number"] = number
	response, err := f.s.request(ctx, c, http.MethodPost, f.s.endpoint(), map[string]any{"query": reviewQuery, "variables": vars})
	if err != nil {
		return changes.Review{}, err
	}
	var result struct {
		Repository *struct{ PullRequest *reviewNode }
	}
	if err := decodeAllowing(response.Body, &result, reviewError); err != nil {
		return changes.Review{}, err
	}
	if result.Repository == nil || result.Repository.PullRequest == nil {
		return changes.Review{}, source.Refuse("not-found", "pull request unavailable")
	}
	r := result.Repository.PullRequest.review()
	data, err := json.Marshal(r)
	if err == nil {
		r = changes.Review{}
		err = json.Unmarshal([]byte(f.s.o.Secrets.Redact(string(data))), &r)
	}
	if err != nil {
		return changes.Review{}, source.Refuse("rejected", "invalid pull request data")
	}
	return r, nil
}
