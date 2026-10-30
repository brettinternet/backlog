// Package gitlab lists a GitLab project's merge requests for linked changes
// (design.md D43). bl has no GitLab source: a GitLab account serves only this
// forge.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

// Options name the project and the account that reads it. Repo is the
// project's full path, such as group/subgroup/app.
type Options struct {
	Host, Repo  string
	Account     project.AccountConfig
	HTTP        *http.Client
	Scheduler   *remote.Scheduler
	Credentials *remote.Credentials
	Secrets     *remote.Secrets
}

// Forge reads merge requests through the account's request queue. Each
// refresh reads every open merge request, whose pipeline changes without
// touching its update time, and any other updated since the last refresh.
type Forge struct {
	o      Options
	client remote.Client
	cache  string
	mu     sync.Mutex
	// reviews are the merge request reviews read, by number.
	reviews *changes.Reviews
}

// NewForge reads o.Repo's merge requests, caching them redacted at cachePath.
func NewForge(o Options, cachePath string) (*Forge, error) {
	if o.Host == "" {
		o.Host = "gitlab.com"
	}
	if !ValidRepo(o.Repo) || strings.ContainsAny(o.Host, "/\\ :@?#\t\r\n") || o.Account.Host != o.Host || o.Account.User == "" || o.Secrets == nil || o.Credentials == nil {
		return nil, source.Refuse("unavailable", "GitLab requires host, group/project, and a matching user account")
	}
	o.Account.Provider = "gitlab"
	return &Forge{o: o, cache: cachePath, reviews: changes.NewReviews(cachePath, o.Repo), client: remote.Client{Host: o.Host, Account: o.Account.User, HTTP: o.HTTP, Scheduler: o.Scheduler, Secrets: o.Secrets}}, nil
}

// ValidRepo reports whether repo is a GitLab project path: a namespace, any
// subgroups, and the project.
func ValidRepo(repo string) bool {
	parts := strings.Split(repo, "/")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.Trim(p, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-") != "" {
			return false
		}
	}
	return true
}

func (f *Forge) Name() string { return "gitlab " + f.o.Repo }

// Web is where the project's merge request, commit, and tree pages hang.
func (f *Forge) Web() string { return "https://" + f.o.Host + "/" + f.o.Repo + "/-" }

func (f *Forge) request(ctx context.Context, c remote.Credential, method, endpoint string, payload any) (remote.Response, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return remote.Response{}, source.Refuse("rejected", "invalid payload")
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return remote.Response{}, source.Refuse("unavailable", "invalid endpoint")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	// GitLab answers a repeated GET's If-None-Match with an empty 304, and
	// the scheduler keeps no body to reuse.
	req.Header.Set("Cache-Control", "no-cache")
	return f.client.Do(ctx, req, c)
}

func (f *Forge) query(ctx context.Context, c remote.Credential, q string, vars map[string]any, out any) error {
	response, err := f.request(ctx, c, http.MethodPost, "https://"+f.o.Host+"/api/graphql", map[string]any{"query": q, "variables": vars})
	if err != nil {
		return err
	}
	var result struct {
		Data   json.RawMessage
		Errors []json.RawMessage
	}
	if json.Unmarshal(response.Body, &result) != nil || len(result.Errors) > 0 || len(result.Data) == 0 || string(result.Data) == "null" || json.Unmarshal(result.Data, out) != nil {
		return source.Refuse("unavailable", "GitLab returned incomplete or invalid data")
	}
	return nil
}

// authenticate resolves the account's token and checks it belongs to the
// configured user.
func (f *Forge) authenticate(ctx context.Context) (remote.Credential, error) {
	return f.o.Credentials.Resolve(ctx, f.o.Account, func(ctx context.Context, c remote.Credential) (string, error) {
		var data struct{ CurrentUser *struct{ Username string } }
		if err := f.query(ctx, c, `query{currentUser{username}}`, nil, &data); err != nil {
			return "", err
		}
		if data.CurrentUser == nil || data.CurrentUser.Username == "" {
			return "", source.Refuse("unavailable", "invalid principal")
		}
		return data.CurrentUser.Username, nil
	})
}

// PullRequests returns the cached list after merging in every merge request
// updated since the last refresh. On failure it returns the cached list with
// the error, so its entries read as stale.
func (f *Forge) PullRequests(ctx context.Context) ([]changes.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return changes.RefreshPulls(f.cache, f.o.Repo, 0, f.o.Secrets.Redact, func(since time.Time) ([]changes.PullRequest, []changes.PullRequest, time.Time, error) {
		c, err := f.authenticate(ctx)
		if err != nil {
			return nil, nil, since, err
		}
		open, _, err := f.fetch(ctx, c, "opened", time.Time{})
		if err != nil {
			return nil, nil, since, err
		}
		updated, cursor, err := f.fetch(ctx, c, "", since)
		return open, updated, cursor, err
	})
}

// Fields every GitLab edition serves: approval counts and rules are Premium.
// detailedMergeStatus names only the first blocking check, and is often
// UNCHECKED, so review reads approved and reviewer states instead.
const mergeRequestsQuery = `query($path:ID!,$state:MergeRequestState,$updatedAfter:Time,$after:String){project(fullPath:$path){id mergeRequests(state:$state,updatedAfter:$updatedAfter,sort:UPDATED_DESC,first:100,after:$after){pageInfo{hasNextPage endCursor} nodes{iid title description webUrl state draft sourceBranch diffHeadSha mergeCommitSha sourceProjectId targetProjectId createdAt updatedAt mergedAt squashOnMerge detailedMergeStatus approved diffStatsSummary{additions deletions} approvedBy{nodes{username}} reviewers{nodes{mergeRequestInteraction{reviewState}}} headPipeline{status}}}}}`

type mergeRequest struct {
	IID, Title, Description, WebURL, State string
	Draft                                  bool
	SourceBranch, DiffHeadSha              string
	MergeCommitSha                         *string
	SourceProjectID, TargetProjectID       *int
	CreatedAt, UpdatedAt                   time.Time
	MergedAt                               *time.Time
	SquashOnMerge, Approved                bool
	DetailedMergeStatus                    string
	Reviewers                              *struct {
		Nodes []struct {
			MergeRequestInteraction *struct{ ReviewState string }
		}
	}
	DiffStatsSummary *struct{ Additions, Deletions int }
	ApprovedBy       *struct{ Nodes []struct{ Username string } }
	HeadPipeline     *struct{ Status string }
}

// fetch pages newest updated first, in state or all, until it reaches since
// or changes.MaxPulls. The cursor is the newest update time read.
func (f *Forge) fetch(ctx context.Context, c remote.Credential, state string, since time.Time) ([]changes.PullRequest, time.Time, error) {
	vars := map[string]any{"path": f.o.Repo}
	if state != "" {
		vars["state"] = state
	}
	if !since.IsZero() {
		vars["updatedAfter"] = since.UTC().Format(time.RFC3339)
	}
	var out []changes.PullRequest
	cursor := since
	for {
		var result struct {
			Project *struct {
				MergeRequests struct {
					PageInfo struct {
						HasNextPage bool
						EndCursor   string
					}
					Nodes []mergeRequest
				}
			}
		}
		if err := f.query(ctx, c, mergeRequestsQuery, vars, &result); err != nil {
			return nil, since, err
		}
		if result.Project == nil {
			return nil, since, source.Refuse("not-found", "project unavailable")
		}
		page, start, done := result.Project.MergeRequests, len(out), false
		for _, n := range page.Nodes {
			// One updated in the cursor's own second may be newer than the read.
			if done = n.UpdatedAt.Before(since); done {
				break
			}
			if n.UpdatedAt.After(cursor) {
				cursor = n.UpdatedAt
			}
			out = append(out, n.pull(f.o.Repo))
		}
		if err := f.squashes(ctx, c, out[start:]); err != nil {
			return nil, since, err
		}
		if done || !page.PageInfo.HasNextPage || page.PageInfo.EndCursor == "" || len(out) >= changes.MaxPulls {
			return out, cursor, nil
		}
		vars["after"] = page.PageInfo.EndCursor
	}
}

func (n mergeRequest) pull(repo string) changes.PullRequest {
	number, _ := strconv.Atoi(n.IID)
	pr := changes.PullRequest{
		Repo: repo, Number: number, Title: n.Title, Body: n.Description, URL: n.WebURL,
		Draft: n.Draft, Head: n.SourceBranch, HeadSHA: n.DiffHeadSha,
		CrossRepository: n.SourceProjectID != nil && n.TargetProjectID != nil && *n.SourceProjectID != *n.TargetProjectID,
		Created:         n.CreatedAt.UTC(), Updated: n.UpdatedAt.UTC(),
	}
	switch n.State {
	case "merged":
		pr.State = changes.StateMerged
	case "closed":
		// GitLab's update time is the close unless the closed merge request
		// was edited since.
		pr.State, pr.Closed = changes.StateClosed, n.UpdatedAt.UTC()
	default:
		// locked is a merge request mid-merge.
		pr.State = changes.StateOpen
	}
	if n.MergedAt != nil {
		pr.Merged = n.MergedAt.UTC()
	}
	if n.MergeCommitSha != nil {
		pr.MergeSHA = *n.MergeCommitSha
	} else if pr.State == changes.StateMerged && !n.SquashOnMerge {
		// A fast-forward merge lands the head commit itself; a squashed one
		// lands a squash commit only REST names (squashes).
		pr.MergeSHA = n.DiffHeadSha
	}
	if n.DiffStatsSummary != nil {
		pr.Additions, pr.Deletions = n.DiffStatsSummary.Additions, n.DiffStatsSummary.Deletions
	}
	approvals := 0
	if n.ApprovedBy != nil {
		approvals = len(n.ApprovedBy.Nodes)
	}
	changesRequested := n.DetailedMergeStatus == "REQUESTED_CHANGES"
	if n.Reviewers != nil {
		for _, r := range n.Reviewers.Nodes {
			changesRequested = changesRequested || r.MergeRequestInteraction != nil && r.MergeRequestInteraction.ReviewState == "REQUESTED_CHANGES"
		}
	}
	// approved is every required approval on Premium, any approval otherwise.
	switch {
	case changesRequested:
		pr.Review = changes.ReviewChanges
	case n.Approved && approvals > 0:
		pr.Review = changes.ReviewApproved
	case n.DetailedMergeStatus == "NOT_APPROVED" || approvals > 0:
		pr.Review = changes.ReviewRequested
	}
	if n.HeadPipeline != nil {
		switch n.HeadPipeline.Status {
		case "SUCCESS":
			pr.Checks = changes.ChecksPassing
		case "FAILED", "CANCELED", "CANCELING":
			pr.Checks = changes.ChecksFailing
		case "CREATED", "WAITING_FOR_RESOURCE", "PREPARING", "PENDING", "RUNNING", "MANUAL", "SCHEDULED", "WAITING_FOR_CALLBACK":
			pr.Checks = changes.ChecksPending
		}
	}
	return pr
}

const (
	// maxPullFiles and maxPullCommits bound one merge request's detail.
	maxPullFiles   = 3000
	maxPullCommits = 250
)

// Pull reads one merge request's files and commits through the account's
// request queue with each file's patch, redacted like the merge request list,
// newest commit first.
func (f *Forge) Pull(ctx context.Context, number int) (changes.Detail, error) {
	c, err := f.authenticate(ctx)
	if err != nil {
		return changes.Detail{}, err
	}
	base := f.api() + "/merge_requests/" + strconv.Itoa(number)
	d := changes.Detail{Patched: true}
	for page := 1; len(d.Files) < maxPullFiles; page++ {
		var diffs []struct {
			OldPath     string `json:"old_path"`
			NewPath     string `json:"new_path"`
			Diff        string
			NewFile     bool `json:"new_file"`
			RenamedFile bool `json:"renamed_file"`
			DeletedFile bool `json:"deleted_file"`
			TooLarge    bool `json:"too_large"`
			Collapsed   bool
		}
		if err := f.get(ctx, c, base+"/diffs?per_page=100&page="+strconv.Itoa(page), &diffs); err != nil {
			return changes.Detail{}, err
		}
		for _, n := range diffs {
			file := changes.File{Path: n.NewPath, Status: changes.FileModified}
			for line := range strings.SplitSeq(n.Diff, "\n") {
				switch {
				case strings.HasPrefix(line, "+"):
					file.Additions++
				case strings.HasPrefix(line, "-"):
					file.Deletions++
				}
			}
			switch {
			case n.NewFile:
				file.Status = changes.FileAdded
			case n.DeletedFile:
				file.Status, file.Path = changes.FileDeleted, n.OldPath
			case n.RenamedFile:
				file.Status, file.From = changes.FileRenamed, n.OldPath
			}
			file.Patch, file.Cut = changes.TrimPatch(n.Diff)
			switch {
			// GitLab leaves out a diff too large or collapsed, and sends no
			// hunks for a binary file.
			case n.Diff == "" && (n.TooLarge || n.Collapsed):
				file.Cut = "GitLab left out this file's diff"
			case !strings.Contains(n.Diff, "@@") && !n.RenamedFile:
				file.Status = changes.FileBinary
			}
			d.Files = append(d.Files, file)
		}
		if len(diffs) < 100 {
			break
		}
	}
	for page := 1; len(d.Commits) < maxPullCommits; page++ {
		var commits []struct {
			ID            string
			Title         string
			Message       string
			AuthorName    string    `json:"author_name"`
			CommittedDate time.Time `json:"committed_date"`
		}
		if err := f.get(ctx, c, base+"/commits?per_page=100&page="+strconv.Itoa(page), &commits); err != nil {
			return changes.Detail{}, err
		}
		// GitLab lists commits newest first.
		for _, n := range commits {
			_, body, _ := strings.Cut(n.Message, "\n")
			d.Commits = append(d.Commits, changes.Commit{SHA: n.ID, Subject: n.Title, Body: strings.Trim(body, "\n"), Author: n.AuthorName, Time: n.CommittedDate.UTC()})
		}
		if len(commits) < 100 {
			break
		}
	}
	data, err := json.Marshal(d)
	if err == nil {
		err = json.Unmarshal([]byte(f.o.Secrets.Redact(string(data))), &d)
	}
	if err != nil {
		return changes.Detail{}, source.Refuse("rejected", "invalid merge request data")
	}
	return d, nil
}

// squashes sets the merge SHA of prs merged as one squash commit without a
// merge commit, which only REST names, in one request.
func (f *Forge) squashes(ctx context.Context, c remote.Credential, prs []changes.PullRequest) error {
	query := url.Values{"per_page": {"100"}}
	for _, pr := range prs {
		if pr.State == changes.StateMerged && pr.MergeSHA == "" {
			query.Add("iids[]", strconv.Itoa(pr.Number))
		}
	}
	if len(query["iids[]"]) == 0 {
		return nil
	}
	var found []struct {
		IID             int    `json:"iid"`
		SquashCommitSHA string `json:"squash_commit_sha"`
	}
	if err := f.get(ctx, c, f.api()+"/merge_requests?"+query.Encode(), &found); err != nil {
		return err
	}
	for _, n := range found {
		for i := range prs {
			if prs[i].Number == n.IID && prs[i].MergeSHA == "" {
				prs[i].MergeSHA = n.SquashCommitSHA
			}
		}
	}
	return nil
}

// api is the project's REST v4 root, its path one escaped segment.
func (f *Forge) api() string {
	return "https://" + f.o.Host + "/api/v4/projects/" + strings.ReplaceAll(url.PathEscape(f.o.Repo), "/", "%2F")
}

func (f *Forge) get(ctx context.Context, c remote.Credential, endpoint string, out any) error {
	response, err := f.request(ctx, c, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if json.Unmarshal(response.Body, out) != nil {
		return source.Refuse("unavailable", "GitLab returned incomplete or invalid data")
	}
	return nil
}
