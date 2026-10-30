package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

// maxPulls bounds the pull requests a forge keeps, the newest updated.
const maxPulls = 1000

// Forge lists a repository's pull requests for linked changes (design.md
// D43), through the account's request queue like the source it reuses. Each
// refresh reads every open pull request, whose checks change without touching
// its update time, and any other updated since the last refresh.
type Forge struct {
	s     *Source
	cache string
	mu    sync.Mutex
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
	return &Forge{s: s, cache: cachePath}, nil
}

func (f *Forge) Name() string { return "github " + f.s.o.Repo }
func (f *Forge) Web() string  { return "https://" + f.s.o.Host + "/" + f.s.o.Repo }

type pullCache struct {
	Repo   string
	Cursor time.Time
	Pulls  []changes.PullRequest
}

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
	var cached pullCache
	if data, err := os.ReadFile(f.cache); err == nil && json.Unmarshal(data, &cached) == nil && cached.Repo != f.s.o.Repo {
		cached = pullCache{}
	}
	c, err := f.s.authenticate(ctx)
	if err != nil {
		return cached.Pulls, err
	}
	open, _, err := f.fetch(ctx, c, []string{"OPEN"}, time.Time{})
	if err != nil {
		return cached.Pulls, err
	}
	updated, cursor, err := f.fetch(ctx, c, nil, cached.Cursor)
	if err != nil {
		return cached.Pulls, err
	}
	// The later read wins: a pull request merged between the two reads is
	// merged.
	seen := map[int]bool{}
	var merged []changes.PullRequest
	for _, list := range [][]changes.PullRequest{updated, open, cached.Pulls} {
		for _, pr := range list {
			if !seen[pr.Number] {
				seen[pr.Number] = true
				merged = append(merged, pr)
			}
		}
	}
	slices.SortStableFunc(merged, func(a, b changes.PullRequest) int { return b.Updated.Compare(a.Updated) })
	merged = merged[:min(len(merged), maxPulls)]
	data, err := json.Marshal(pullCache{Repo: f.s.o.Repo, Cursor: cursor, Pulls: merged})
	if err == nil {
		// Redact before anything is kept or shown, as provider activity is.
		data = []byte(f.s.o.Secrets.Redact(string(data)))
		var redacted pullCache
		if err = json.Unmarshal(data, &redacted); err == nil {
			merged = redacted.Pulls
		}
	}
	if err != nil {
		return nil, source.Refuse("rejected", "invalid pull request data")
	}
	if f.cache != "" {
		cache.WriteFile(f.cache, data)
	}
	return merged, nil
}

// fetch pages newest updated first, in states or all, until it reaches since
// or maxPulls. The cursor is the newest update time read.
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
		if !page.PageInfo.HasNextPage || page.PageInfo.EndCursor == "" || len(out) >= maxPulls {
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
