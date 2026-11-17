package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
	"github.com/brettinternet/backlog/internal/textutil"
)

// Forge lists a repository's pull requests for linked changes (design.md
// D43), through the account's request queue like the source it reuses. Each
// refresh reads every open pull request, whose checks change without touching
// its update time, then a few pages of the others, newest updated since the
// last refresh first, then continuing an earlier read.
type Forge struct {
	s       *Source
	cache   string
	mu      sync.Mutex
	reviews *changes.Reviews
	checks  *changes.Checks
}

// NewForge reads o.Repo's pull requests, caching them redacted at cachePath.
func NewForge(o Options, cachePath string) (*Forge, error) {
	s, err := New(o)
	if err != nil {
		return nil, err
	}
	if o.Repo == "" {
		return nil, source.Refuse("rejected", "a GitHub forge needs owner/repo")
	}
	return &Forge{s: s, cache: cachePath, reviews: changes.NewReviews(cachePath, o.Repo), checks: changes.NewChecks(cachePath, o.Repo)}, nil
}

func (f *Forge) Name() string { return "github " + f.s.o.Repo }
func (f *Forge) Web() string  { return "https://" + f.s.o.Host + "/" + f.s.o.Repo }

// pullsQuery reads mergeStateStatus, reviewDecision, and the checks rollup,
// which together quadruple a page's time, only when open is set: the list
// shows them only for an open pull request.
const pullsQuery = `query($owner:String!,$repo:String!,$first:Int!,$after:String,$states:[PullRequestState!],$open:Boolean!){repository(owner:$owner,name:$repo){pullRequests(first:$first,after:$after,states:$states,orderBy:{field:UPDATED_AT,direction:DESC}){pageInfo{hasNextPage endCursor} nodes{number title body url state isDraft isCrossRepository createdAt updatedAt mergedAt closedAt headRefName headRefOid author{login __typename} mergeCommit{oid} additions deletions reviewDecision @include(if:$open) mergeStateStatus @include(if:$open) closingIssuesReferences(first:10){nodes{id}} commits(last:1) @include(if:$open){nodes{commit{statusCheckRollup{state}}}}}}}}`

const (
	// openPullsPage and pullsPage are the open and the closed and merged
	// passes' page sizes. A page GitHub fails to serve, often a 502 or a
	// GraphQL error from a slow page on a large repository, is read again at
	// half the size, down to minPullsPage.
	openPullsPage = 50
	pullsPage     = 100
	minPullsPage  = 12
)

type pullNode struct {
	Number                     int
	Title, Body, URL, State    string
	IsDraft, IsCrossRepository bool
	CreatedAt, UpdatedAt       time.Time
	MergedAt, ClosedAt         *time.Time
	HeadRefName, HeadRefOid    string
	Author                     *struct {
		Login    string
		Typename string `json:"__typename"`
	}
	MergeCommit             *struct{ Oid string }
	Additions, Deletions    int
	ReviewDecision          string
	MergeStateStatus        string
	ClosingIssuesReferences struct{ Nodes []struct{ ID string } }
	Commits                 struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct{ State string }
			}
		}
	}
}

// PullRequests returns the cached list after merging in this refresh's
// reads. On failure it returns the cached list with the error, so its entries
// read as stale; with older pull requests left to read, *changes.PartialPulls.
func (f *Forge) PullRequests(ctx context.Context) ([]changes.PullRequest, error) {
	return f.refresh(ctx, 0)
}

// pullsAge is how long a refresh serves readers that don't own the refresh
// cadence, as often as the changes tracker refreshes.
const pullsAge = time.Minute

// Recent is PullRequests, but reuses a list refreshed in the last minute by
// any process, so the github-pulls source adds no request to the changes
// tracker's. Older pull requests left to read don't fail it: every open one
// was read.
func (f *Forge) Recent(ctx context.Context) ([]changes.PullRequest, error) {
	prs, err := f.refresh(ctx, pullsAge)
	if partial := (*changes.PartialPulls)(nil); errors.As(err, &partial) {
		err = nil
	}
	return prs, err
}

func (f *Forge) refresh(ctx context.Context, fresh time.Duration) ([]changes.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var c remote.Credential
	open := func() ([]changes.PullRequest, error) {
		var err error
		if c, err = f.s.authenticate(ctx); err != nil {
			return nil, err
		}
		open, _, err := f.fetch(ctx, c, "OPEN", "", time.Time{}, nil)
		return open, err
	}
	return changes.RefreshPulls(f.cache, f.s.o.Repo, fresh, f.s.o.Secrets.Redact, open, func(prev changes.PullProgress) ([]changes.PullRequest, changes.PullProgress, error) {
		pages := olderPages
		updated, after, err := f.fetch(ctx, c, "", "", prev.Cursor, &pages)
		if len(updated) == 0 && err != nil {
			return nil, prev, err
		}
		next := prev
		if len(updated) > 0 {
			next.Cursor = updated[0].Updated
		}
		if after != "" && len(updated) > 0 {
			// Stopped short of the cursor: resume down to it, or past it to
			// where an earlier interrupted read was going, rereading what
			// that read had reached.
			if prev.Resume == "" {
				next.Since = prev.Cursor
			}
			next.Resume, next.Oldest = after, updated[len(updated)-1].Updated
			return updated, next, err
		}
		if prev.Resume == "" || pages == 0 {
			return updated, next, nil
		}
		rest, after, err := f.fetch(ctx, c, "", prev.Resume, prev.Since, &pages)
		updated = append(updated, rest...)
		if after != "" {
			if len(rest) > 0 {
				next.Resume, next.Oldest = after, rest[len(rest)-1].Updated
			}
			return updated, next, err
		}
		next.Resume, next.Oldest, next.Since = "", time.Time{}, time.Time{}
		return updated, next, nil
	})
}

// olderPages bounds the closed and merged pages one refresh reads, so a
// large repository's first refresh returns its open pull requests promptly
// and later refreshes read the rest.
const olderPages = 3

// fetch pages newest updated first from the page cursor after, open pull
// requests or the rest, until it reaches since or changes.MaxPulls. With
// pages, it reads at most that many, counting them down, and fewer once the
// account's rate limit runs low; it then returns the page cursor to resume
// from. On failure it returns those read with the page cursor after them.
func (f *Forge) fetch(ctx context.Context, c remote.Credential, state, after string, since time.Time, pages *int) ([]changes.PullRequest, string, error) {
	vars := f.s.variables()
	vars["states"], vars["open"] = []string{"CLOSED", "MERGED"}, state == "OPEN"
	first := pullsPage
	if state != "" {
		vars["states"], first = []string{state}, openPullsPage
	}
	client := f.s.client
	client.Refusals = true
	var out []changes.PullRequest
	for {
		if pages != nil && *pages <= 0 {
			return out, after, nil
		}
		if after != "" {
			vars["after"] = after
		}
		vars["first"] = first
		var result struct {
			Repository *struct {
				PullRequests struct {
					PageInfo struct {
						HasNextPage bool
						EndCursor   string
					}
					Nodes []pullNode
				}
			}
		}
		response, err := f.s.requestWith(ctx, client, c, http.MethodPost, f.s.endpoint(), map[string]any{"query": pullsQuery, "variables": vars})
		if err == nil && response.Status >= 500 && first > minPullsPage {
			first /= 2
			continue
		}
		if err == nil && response.Status >= 300 {
			err = client.Refusal(response.Status, response.Body)
		}
		if err != nil {
			return out, after, err
		}
		if err := decodeAllowing(response.Body, &result, pullError); err != nil {
			if first > minPullsPage {
				first /= 2
				continue
			}
			return out, after, err
		}
		if result.Repository == nil {
			return out, after, source.Refuse("not-found", "repository unavailable")
		}
		page := result.Repository.PullRequests
		for _, n := range page.Nodes {
			// One updated in the cursor's own second may be newer than the read.
			if n.UpdatedAt.Before(since) {
				return out, "", nil
			}
			out = append(out, n.pull(f.s.o.Repo))
		}
		if !page.PageInfo.HasNextPage || page.PageInfo.EndCursor == "" || len(out) >= changes.MaxPulls {
			return out, "", nil
		}
		after = page.PageInfo.EndCursor
		if pages != nil {
			*pages--
			// Leave the rest of the hour's requests to interactive reads and
			// other tools sharing the token.
			limit, _ := strconv.Atoi(response.Header.Get("X-RateLimit-Limit"))
			if remaining, err := strconv.Atoi(response.Header.Get("X-RateLimit-Remaining")); err == nil && remaining*10 < limit {
				*pages = 0
			}
		}
	}
}

// Probe reads one page of pull requests with their checks and closing
// references, without caching it, for bl doctor. It names each part the
// token can't read, or fails when the pull requests themselves can't be.
func (f *Forge) Probe(ctx context.Context) ([]string, error) {
	c, err := f.s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	vars := f.s.variables()
	vars["first"], vars["merge"] = minPullsPage, false
	client := f.s.client
	client.Refusals = true
	response, err := f.s.requestWith(ctx, client, c, http.MethodPost, f.s.endpoint(), map[string]any{"query": pullsQuery, "variables": vars})
	if err == nil && response.Status >= 300 {
		err = client.Refusal(response.Status, response.Body)
	}
	if err != nil {
		return nil, err
	}
	var result struct{ Repository *struct{} }
	parts := map[any]string{"commits": "checks", "closingIssuesReferences": "closing references"}
	denied := map[string]bool{}
	if err := decodeAllowing(response.Body, &result, func(kind string, path []any) bool {
		if !pullError(kind, path) {
			return false
		}
		denied[parts[path[4]]] = true
		return true
	}); err != nil {
		return nil, err
	}
	if result.Repository == nil {
		return nil, source.Refuse("not-found", "repository unavailable")
	}
	var unreadable []string
	for _, part := range []string{"checks", "closing references"} {
		if denied[part] {
			unreadable = append(unreadable, part)
		}
	}
	return unreadable, nil
}

// pullError tolerates a pull request whose closing issues or checks the
// token can't read; its other fields still link it.
func pullError(kind string, path []any) bool {
	if kind != "FORBIDDEN" && kind != "NOT_FOUND" || len(path) < 5 || fmt.Sprint(path[:3]) != "[repository pullRequests nodes]" {
		return false
	}
	return path[4] == "closingIssuesReferences" || path[4] == "commits"
}

func (n pullNode) pull(repo string) changes.PullRequest {
	pr := changes.PullRequest{
		Repo: repo, Number: n.Number, Title: n.Title, Body: n.Body, URL: n.URL,
		State: strings.ToLower(n.State), Draft: n.IsDraft, CrossRepository: n.IsCrossRepository,
		Head: n.HeadRefName, HeadSHA: n.HeadRefOid, Additions: n.Additions, Deletions: n.Deletions,
		Created: n.CreatedAt.UTC(), Updated: n.UpdatedAt.UTC(),
		Review:     map[string]string{"APPROVED": changes.ReviewApproved, "CHANGES_REQUESTED": changes.ReviewChanges, "REVIEW_REQUIRED": changes.ReviewRequested}[n.ReviewDecision],
		MergeState: strings.ToLower(textutil.Line(n.MergeStateStatus)),
	}
	if n.MergedAt != nil {
		pr.Merged = n.MergedAt.UTC()
	}
	if n.ClosedAt != nil {
		pr.Closed = n.ClosedAt.UTC()
	}
	if n.Author != nil {
		pr.Author, pr.Bot = n.Author.Login, n.Author.Typename == "Bot"
	}
	if n.MergeCommit != nil {
		pr.MergeSHA = n.MergeCommit.Oid
	}
	for _, issue := range n.ClosingIssuesReferences.Nodes {
		pr.Closes = append(pr.Closes, issue.ID)
	}
	if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		pr.Checks = map[string]string{"SUCCESS": changes.ChecksPassing, "FAILURE": changes.ChecksFailing, "ERROR": changes.ChecksFailing, "PENDING": changes.ChecksPending, "EXPECTED": changes.ChecksPending}[n.Commits.Nodes[0].Commit.StatusCheckRollup.State]
	}
	return pr
}

const (
	// maxPullFiles and maxPullCommits are the most GitHub lists for one pull
	// request.
	maxPullFiles   = 3000
	maxPullCommits = 250
)

// Pull reads one pull request's files, with their patches, and commits through the account's
// request queue, redacted like the pull request list, newest commit first.
func (f *Forge) Pull(ctx context.Context, number int) (changes.Detail, error) {
	c, err := f.s.authenticate(ctx)
	if err != nil {
		return changes.Detail{}, err
	}
	base := f.s.base() + "/repos/" + f.s.o.Repo + "/pulls/" + strconv.Itoa(number)
	d := changes.Detail{Patched: true}
	for page := 1; len(d.Files) < maxPullFiles; page++ {
		var files []struct {
			Filename, Status, Patch string
			PreviousFilename        string `json:"previous_filename"`
			Additions, Deletions    int
		}
		if err := f.get(ctx, c, base+"/files?per_page=100&page="+strconv.Itoa(page), &files); err != nil {
			return changes.Detail{}, err
		}
		for _, n := range files {
			file := changes.File{Path: n.Filename, Additions: n.Additions, Deletions: n.Deletions, Status: changes.FileModified}
			switch n.Status {
			case "added", "copied":
				file.Status = changes.FileAdded
			case "removed":
				file.Status = changes.FileDeleted
			case "renamed":
				file.Status, file.From = changes.FileRenamed, n.PreviousFilename
			}
			// GitHub sends no patch for a binary file, nor for one whose diff
			// is too large, which still counts its lines.
			file.Patch, file.Cut = changes.TrimPatch(n.Patch)
			switch {
			case n.Patch == "" && n.Additions == 0 && n.Deletions == 0 && n.Status != "renamed":
				file.Status = changes.FileBinary
			case n.Patch == "" && n.Additions+n.Deletions > 0:
				file.Cut = "GitHub left out this file's diff"
			}
			d.Files = append(d.Files, file)
		}
		if len(files) < 100 {
			break
		}
	}
	for page := 1; len(d.Commits) < maxPullCommits; page++ {
		var commits []struct {
			SHA    string
			Commit struct {
				Message   string
				Author    struct{ Name string }
				Committer struct{ Date time.Time }
			}
		}
		if err := f.get(ctx, c, base+"/commits?per_page=100&page="+strconv.Itoa(page), &commits); err != nil {
			return changes.Detail{}, err
		}
		for _, n := range commits {
			subject, body, _ := strings.Cut(n.Commit.Message, "\n")
			d.Commits = append(d.Commits, changes.Commit{SHA: n.SHA, Subject: subject, Body: strings.Trim(body, "\n"), Author: n.Commit.Author.Name, Time: n.Commit.Committer.Date.UTC()})
		}
		if len(commits) < 100 {
			break
		}
	}
	// GitHub lists commits oldest first.
	slices.Reverse(d.Commits)
	data, err := json.Marshal(d)
	if err == nil {
		err = json.Unmarshal([]byte(f.s.o.Secrets.Redact(string(data))), &d)
	}
	if err != nil {
		return changes.Detail{}, source.Refuse("rejected", "invalid pull request data")
	}
	return d, nil
}

func (f *Forge) get(ctx context.Context, c remote.Credential, endpoint string, out any) error {
	response, err := f.s.request(ctx, c, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if json.Unmarshal(response.Body, out) != nil {
		return source.Refuse("unavailable", "GitHub returned incomplete or invalid data")
	}
	return nil
}
