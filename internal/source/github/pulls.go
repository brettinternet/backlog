package github

import (
	"context"
	"encoding/json"
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
)

// Forge lists a repository's pull requests for linked changes (design.md
// D43), through the account's request queue like the source it reuses. Each
// refresh reads every open pull request, whose checks change without touching
// its update time, and any other updated since the last refresh.
type Forge struct {
	s       *Source
	cache   string
	mu      sync.Mutex
	reviews *changes.Reviews
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
	return &Forge{s: s, cache: cachePath, reviews: changes.NewReviews(cachePath, o.Repo)}, nil
}

func (f *Forge) Name() string { return "github " + f.s.o.Repo }
func (f *Forge) Web() string  { return "https://" + f.s.o.Host + "/" + f.s.o.Repo }

const pullsQuery = `query($owner:String!,$repo:String!,$after:String,$states:[PullRequestState!]){repository(owner:$owner,name:$repo){pullRequests(first:50,after:$after,states:$states,orderBy:{field:UPDATED_AT,direction:DESC}){pageInfo{hasNextPage endCursor} nodes{number title body url state isDraft isCrossRepository createdAt updatedAt mergedAt closedAt headRefName headRefOid mergeCommit{oid} additions deletions reviewDecision closingIssuesReferences(first:10){nodes{id}} commits(last:1){nodes{commit{statusCheckRollup{state}}}}}}}}`

type pullNode struct {
	Number                     int
	Title, Body, URL, State    string
	IsDraft, IsCrossRepository bool
	CreatedAt, UpdatedAt       time.Time
	MergedAt, ClosedAt         *time.Time
	HeadRefName, HeadRefOid    string
	MergeCommit                *struct{ Oid string }
	Additions, Deletions       int
	ReviewDecision             string
	ClosingIssuesReferences    struct{ Nodes []struct{ ID string } }
	Commits                    struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct{ State string }
			}
		}
	}
}

// PullRequests returns the cached list after merging in every pull request
// updated since the last refresh. On failure it returns the cached list with
// the error, so its entries read as stale.
func (f *Forge) PullRequests(ctx context.Context) ([]changes.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return changes.RefreshPulls(f.cache, f.s.o.Repo, f.s.o.Secrets.Redact, func(since time.Time) ([]changes.PullRequest, []changes.PullRequest, time.Time, error) {
		c, err := f.s.authenticate(ctx)
		if err != nil {
			return nil, nil, since, err
		}
		open, _, err := f.fetch(ctx, c, []string{"OPEN"}, time.Time{})
		if err != nil {
			return nil, nil, since, err
		}
		updated, cursor, err := f.fetch(ctx, c, nil, since)
		return open, updated, cursor, err
	})
}

// fetch pages newest updated first, in states or all, until it reaches since
// or changes.MaxPulls. The cursor is the newest update time read.
func (f *Forge) fetch(ctx context.Context, c remote.Credential, states []string, since time.Time) ([]changes.PullRequest, time.Time, error) {
	vars := f.s.variables()
	vars["states"] = states
	var out []changes.PullRequest
	cursor := since
	for {
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
		response, err := f.s.request(ctx, c, http.MethodPost, f.s.endpoint(), map[string]any{"query": pullsQuery, "variables": vars})
		if err != nil {
			return nil, since, err
		}
		if err := decodeAllowing(response.Body, &result, pullError); err != nil {
			return nil, since, err
		}
		if result.Repository == nil {
			return nil, since, source.Refuse("not-found", "repository unavailable")
		}
		page := result.Repository.PullRequests
		for _, n := range page.Nodes {
			// One updated in the cursor's own second may be newer than the read.
			if n.UpdatedAt.Before(since) {
				return out, cursor, nil
			}
			if n.UpdatedAt.After(cursor) {
				cursor = n.UpdatedAt
			}
			out = append(out, n.pull(f.s.o.Repo))
		}
		if !page.PageInfo.HasNextPage || page.PageInfo.EndCursor == "" || len(out) >= changes.MaxPulls {
			return out, cursor, nil
		}
		vars["after"] = page.PageInfo.EndCursor
	}
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
		Review: map[string]string{"APPROVED": changes.ReviewApproved, "CHANGES_REQUESTED": changes.ReviewChanges, "REVIEW_REQUIRED": changes.ReviewRequested}[n.ReviewDecision],
	}
	if n.MergedAt != nil {
		pr.Merged = n.MergedAt.UTC()
	}
	if n.ClosedAt != nil {
		pr.Closed = n.ClosedAt.UTC()
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
			subject, _, _ := strings.Cut(n.Commit.Message, "\n")
			d.Commits = append(d.Commits, changes.Commit{SHA: n.SHA, Subject: subject, Author: n.Commit.Author.Name, Time: n.Commit.Committer.Date.UTC()})
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
