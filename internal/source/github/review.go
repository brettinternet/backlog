package github

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
	"github.com/brettinternet/backlog/internal/textutil"
)

const reviewQuery = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){id body headRefOid mergeStateStatus mergeable
reviewRequests(first:50){nodes{requestedReviewer{__typename ... on User{login} ... on Bot{login} ... on Mannequin{login} ... on Team{slug}}}}
latestReviews(first:50){nodes{author{login} state submittedAt commit{oid}}}
reviews(first:100){nodes{author{login} body state submittedAt url}}
comments(first:100){nodes{author{login} body createdAt url}}
reviewThreads(first:100){nodes{id isResolved isOutdated path line originalLine comments(first:50){nodes{author{login} body createdAt url}}}}
commits(last:1){nodes{commit{statusCheckRollup{contexts(first:100){nodes{__typename ... on CheckRun{id name status conclusion detailsUrl url databaseId startedAt completedAt} ... on StatusContext{context state targetUrl description}}}}}}}}}}`

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
	ID, Body, HeadRefOid        string
	MergeStateStatus, Mergeable string
	ReviewRequests              struct {
		Nodes []struct {
			RequestedReviewer *struct{ Login, Slug string }
		}
	}
	LatestReviews struct {
		Nodes []struct {
			Author      *reviewAuthor
			State       string
			SubmittedAt time.Time
			Commit      *struct{ Oid string }
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
			ID                     string
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
							Typename                                   string `json:"__typename"`
							Name, Status, Conclusion, DetailsURL, URL  string
							DatabaseID                                 int64
							StartedAt, CompletedAt                     time.Time
							ID, Context, State, TargetURL, Description string
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

// checkStatus is a check run's exact state word, from its status until it
// completes and then its conclusion, or a commit status's.
func checkStatus(status, conclusion string) string {
	switch status {
	case "", "COMPLETED":
	case "QUEUED", "REQUESTED":
		return changes.CheckQueued
	case "IN_PROGRESS":
		return changes.CheckRunning
	default:
		return changes.CheckWaiting
	}
	return cmp.Or(map[string]string{
		"SUCCESS": changes.CheckPassed, "NEUTRAL": changes.CheckNeutral, "SKIPPED": changes.CheckSkipped,
		"STALE": changes.CheckStale, "TIMED_OUT": changes.CheckTimedOut, "CANCELLED": changes.CheckCancelled,
		"ACTION_REQUIRED": changes.CheckActionRequired, "PENDING": changes.CheckPending, "EXPECTED": changes.CheckWaiting,
	}[conclusion], changes.CheckFailed)
}

func (n reviewNode) review() changes.Review {
	r := changes.Review{ID: n.ID, Head: n.HeadRefOid, Body: textutil.Sanitize(n.Body),
		MergeState: strings.ToLower(textutil.Line(n.MergeStateStatus)), Mergeable: strings.ToLower(textutil.Line(n.Mergeable))}
	states := map[string]string{}
	submitted := map[string]changes.Reviewer{}
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
	for _, review := range n.LatestReviews.Nodes {
		if review.Author != nil {
			set(review.Author.Login, reviewStates[review.State])
			// A review on another commit than the head is outdated.
			submitted[review.Author.Login] = changes.Reviewer{SubmittedAt: review.SubmittedAt.UTC(), Outdated: review.Commit != nil && review.Commit.Oid != n.HeadRefOid}
		}
	}
	// A re-requested reviewer is waiting again, whatever they said before.
	for _, n := range n.ReviewRequests.Nodes {
		if n.RequestedReviewer != nil {
			set(cmp.Or(n.RequestedReviewer.Login, n.RequestedReviewer.Slug), changes.ReviewRequested)
		}
	}
	for _, login := range order {
		reviewer := changes.Reviewer{Login: textutil.Line(login), State: states[login]}
		if s, ok := submitted[login]; ok && reviewer.State != changes.ReviewRequested {
			reviewer.SubmittedAt, reviewer.Outdated = s.SubmittedAt, s.Outdated
		}
		r.Reviewers = append(r.Reviewers, reviewer)
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
		t := changes.Thread{ID: n.ID, Path: textutil.Line(n.Path), Resolved: n.IsResolved, Outdated: n.IsOutdated}
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
			check := changes.Check{ID: c.ID, Name: c.Name, Status: checkStatus(c.Status, c.Conclusion), URL: cmp.Or(c.DetailsURL, c.URL),
				Run: c.DatabaseID, StartedAt: c.StartedAt.UTC(), CompletedAt: c.CompletedAt.UTC()}
			if c.Typename == "StatusContext" {
				check = changes.Check{Name: c.Context, Status: checkStatus("", c.State), URL: c.TargetURL}
				check.Summary = textutil.Sanitize(c.Description)
			}
			check.Name, check.URL, check.State = textutil.Line(check.Name), textutil.Line(check.URL), changes.CheckCategory(check.Status)
			r.Checks = append(r.Checks, check)
		}
	}
	return r
}

// reviewBlocked says why the account can't post reviews: an OAuth grant
// needs --write and bl-cli's Pull requests: write.
func (f *Forge) reviewBlocked(c remote.Credential) string {
	switch {
	case f.s.o.Account.OAuth && !c.HasScope("write"):
		return oauth.WriteRequired().Message
	case f.s.o.Account.OAuth && !f.s.pullWrite:
		return "bl-cli lacks Pull requests: write on " + f.s.o.Repo + " (docs/github.md)"
	}
	return ""
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
	r.Blocked = f.reviewBlocked(c)
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

const checkQuery = `query($id:ID!){node(id:$id){... on CheckRun{status conclusion title summary
checkSuite{app{slug} workflowRun{databaseId}}
steps(first:50){nodes{number name status conclusion startedAt completedAt}}
annotations(first:50){nodes{path location{start{line}} annotationLevel message title blobUrl}}}}}`

type checkNode struct {
	Status, Conclusion, Title, Summary string
	CheckSuite                         *struct {
		App         *struct{ Slug string }
		WorkflowRun *struct{ DatabaseID int64 }
	}
	Steps struct {
		Nodes []struct {
			Number                 int
			Name, Status           string
			Conclusion             string
			StartedAt, CompletedAt time.Time
		}
	}
	Annotations struct {
		Nodes []struct {
			Path     string
			Location struct {
				Start struct{ Line int }
			}
			AnnotationLevel, Message, Title, BlobURL string
		}
	}
}

// checkError tolerates a workflow run the token can't read; the job's
// steps still show.
func checkError(kind string, path []any) bool {
	return kind == "FORBIDDEN" && len(path) >= 2 && path[0] == "node" && path[1] == "checkSuite"
}

// Check reads a check run's steps, annotations, and output through the
// account's request queue and, for a failed GitHub Actions job, its log
// excerpt, all redacted. A completed run's detail is kept by check run ID
// and reused unless fresh; a running one, or one whose log the token can't
// read yet, is read each time.
func (f *Forge) Check(ctx context.Context, check changes.Check, fresh bool) (changes.CheckDetail, error) {
	return f.checks.Get(check.Run, fresh, func() (changes.CheckDetail, bool, error) { return f.readCheck(ctx, check) })
}

func (f *Forge) readCheck(ctx context.Context, check changes.Check) (changes.CheckDetail, bool, error) {
	c, err := f.s.authenticate(ctx)
	if err != nil {
		return changes.CheckDetail{}, false, err
	}
	response, err := f.s.request(ctx, c, http.MethodPost, f.s.endpoint(), map[string]any{"query": checkQuery, "variables": map[string]any{"id": check.ID}})
	if err != nil {
		return changes.CheckDetail{}, false, err
	}
	var result struct{ Node *checkNode }
	if err := decodeAllowing(response.Body, &result, checkError); err != nil {
		return changes.CheckDetail{}, false, err
	}
	if result.Node == nil {
		return changes.CheckDetail{}, false, source.Refuse("not-found", "check run unavailable")
	}
	n := result.Node
	d := changes.CheckDetail{Title: textutil.Line(n.Title), Summary: textutil.Sanitize(n.Summary)}
	actions := n.CheckSuite != nil && n.CheckSuite.App != nil && n.CheckSuite.App.Slug == "github-actions"
	if n.CheckSuite != nil && n.CheckSuite.WorkflowRun != nil {
		d.Workflow = n.CheckSuite.WorkflowRun.DatabaseID
	}
	for _, s := range n.Steps.Nodes {
		step := changes.Step{Number: s.Number, Name: textutil.Line(s.Name), Status: checkStatus(s.Status, s.Conclusion), StartedAt: s.StartedAt.UTC(), CompletedAt: s.CompletedAt.UTC()}
		if actions && check.URL != "" {
			step.URL = check.URL + "#step:" + strconv.Itoa(s.Number) + ":1"
		}
		d.Steps = append(d.Steps, step)
	}
	for _, a := range n.Annotations.Nodes {
		note := changes.Annotation{Path: textutil.Line(a.Path), Line: a.Location.Start.Line, Level: strings.ToLower(textutil.Line(a.AnnotationLevel)),
			Title: textutil.Line(a.Title), Message: textutil.Sanitize(a.Message), URL: textutil.Line(a.BlobURL)}
		if note.URL != "" && note.Line > 0 {
			note.URL += "#L" + strconv.Itoa(note.Line)
		}
		d.Annotations = append(d.Annotations, note)
	}
	status := checkStatus(n.Status, n.Conclusion)
	final := n.Status == "COMPLETED"
	switch {
	case !actions || changes.CheckCategory(status) != changes.ChecksFailing && final:
	case !final:
		d.LogUnavailable = "The job is still running."
	case f.s.o.Account.OAuth && !f.s.actionsRead:
		app := oauth.GitHubReadApp
		if c.HasScope("write") {
			app = oauth.GitHubWriteApp
		}
		d.LogUnavailable, final = app+" lacks Actions: read on "+f.s.o.Repo+" (docs/github.md)", false
	default:
		d.Log, d.LogUnavailable, final = f.readLog(ctx, c, check.Run)
	}
	data, err := json.Marshal(d)
	if err == nil {
		d = changes.CheckDetail{}
		err = json.Unmarshal([]byte(f.s.o.Secrets.Redact(string(data))), &d)
	}
	if err != nil {
		return changes.CheckDetail{}, false, source.Refuse("rejected", "invalid check run data")
	}
	return d, final, nil
}

// readLog reads an Actions job's log excerpt, or says why there is none and
// whether that is final. GitHub answers with a redirect to signed storage,
// which is read without the account's credential.
func (f *Forge) readLog(ctx context.Context, c remote.Credential, job int64) (string, string, bool) {
	client := f.s.client
	client.Refusals = true
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.s.base()+"/repos/"+f.s.o.Repo+"/actions/jobs/"+strconv.FormatInt(job, 10)+"/logs", nil)
	if err != nil {
		return "", "The log is unavailable.", false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Cache-Control", "no-cache")
	response, err := client.Do(ctx, req, c)
	if err != nil {
		return "", "The log is unavailable: " + textutil.Line(err.Error()), false
	}
	switch response.Status {
	case http.StatusFound, http.StatusTemporaryRedirect, http.StatusSeeOther, http.StatusMovedPermanently, http.StatusPermanentRedirect:
	case http.StatusForbidden:
		return "", "The token lacks Actions: read on " + f.s.o.Repo + " (docs/github.md).", false
	case http.StatusNotFound, http.StatusGone:
		return "", "GitHub no longer has this log.", true
	default:
		return "", "The log is unavailable.", false
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil || location.Scheme != "https" || location.Host == "" || location.User != nil {
		return "", "The log is unavailable.", false
	}
	blob := http.Client{Timeout: time.Minute}
	if f.s.o.HTTP != nil {
		blob = *f.s.o.HTTP
	}
	blob.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return "", "The log is unavailable.", false
	}
	resp, err := blob.Do(req)
	if err != nil {
		return "", "The log is unavailable.", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "The log is unavailable.", false
	}
	lines, err := changes.Excerpt(resp.Body)
	switch {
	case errors.Is(err, changes.ErrLogTooLarge):
		return "", "The log is over 32 MiB.", true
	case err != nil:
		return "", "The log is unavailable.", false
	}
	return strings.Join(lines, "\n"), "", true
}
