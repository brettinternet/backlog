package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source/remote"
	"github.com/brettinternet/backlog/internal/testkit"
)

// fake serves GitLab's GraphQL merge request list and REST merge request
// detail for grp/sub/app.
type fake struct {
	mu       sync.Mutex
	open     []map[string]any
	all      []map[string]any
	fail     bool
	after    []string
	requests []string
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer fake-secret" {
		w.WriteHeader(401)
		return
	}
	f.requests = append(f.requests, r.URL.EscapedPath())
	if r.Method == http.MethodGet && r.Header.Get("Cache-Control") != "no-cache" {
		w.Header().Set("ETag", `W/"same"`)
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(304)
			return
		}
	}
	switch r.URL.EscapedPath() {
	case "/api/v4/projects/grp%2Fsub%2Fapp/merge_requests":
		if r.URL.Query()["iids[]"][0] != "5" {
			w.WriteHeader(400)
			return
		}
		fmt.Fprint(w, `[{"iid":5,"squash_commit_sha":"sss"}]`)
	case "/api/graphql":
		var body struct {
			Query     string
			Variables map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(body.Query, "currentUser") {
			fmt.Fprint(w, `{"data":{"currentUser":{"username":"alice"}}}`)
			return
		}
		if f.fail {
			w.WriteHeader(502)
			return
		}
		if strings.Contains(body.Query, "discussions") {
			fmt.Fprint(w, `{"data":{"project":{"mergeRequest":{"description":"token fake-secret","diffHeadSha":"aaa",
"reviewers":{"nodes":[{"username":"bob","mergeRequestInteraction":{"reviewState":"REQUESTED_CHANGES"}}]},
"approvedBy":{"nodes":[{"username":"cy"}]},
"headPipeline":{"jobs":{"nodes":[{"name":"test","status":"FAILED","allowFailure":false,"webPath":"/grp/sub/app/-/jobs/1"},{"name":"lint","status":"FAILED","allowFailure":true}]}},
"discussions":{"nodes":[
{"resolvable":false,"notes":{"nodes":[{"system":true,"body":"added 1 commit","author":{"username":"bob"},"createdAt":"2026-01-01T00:00:00Z"}]}},
{"resolvable":false,"notes":{"nodes":[{"body":"looks fine","author":{"username":"cy"},"createdAt":"2026-01-01T00:00:00Z"}]}},
{"resolvable":true,"resolved":false,"notes":{"nodes":[{"body":"fix this","author":{"username":"bob"},"createdAt":"2026-01-02T00:00:00Z","position":{"filePath":"new.go","newLine":null,"oldLine":3}},{"body":"done","author":{"username":"alice"},"createdAt":"2026-01-03T00:00:00Z"}]}}]}}}}}`)
			return
		}
		nodes := f.all
		if body.Variables["state"] == "opened" {
			nodes = f.open
		} else if after, _ := body.Variables["updatedAfter"].(string); after != "" {
			f.after = append(f.after, after)
		}
		data, _ := json.Marshal(map[string]any{"data": map[string]any{"project": map[string]any{"id": "gid://gitlab/Project/1", "mergeRequests": map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": nodes}}}})
		w.Write(data)
	case "/api/v4/projects/grp%2Fsub%2Fapp/merge_requests/7/diffs":
		fmt.Fprint(w, `[{"old_path":"old.go","new_path":"new.go","renamed_file":true,"diff":"@@ -1 +1 @@\n-a\n+b\n+c\n"},{"old_path":"logo.png","new_path":"logo.png","new_file":true,"diff":""},{"old_path":"gone.go","new_path":"gone.go","deleted_file":true,"diff":"@@ -1,2 +0,0 @@\n-x\n-y\n"}]`)
	case "/api/v4/projects/grp%2Fsub%2Fapp/merge_requests/7/commits":
		fmt.Fprint(w, `[{"id":"c2","title":"second fake-secret","author_name":"Bo","committed_date":"2026-01-02T00:00:00Z"},{"id":"c1","title":"first","author_name":"Ann","committed_date":"2026-01-01T00:00:00Z"}]`)
	default:
		w.WriteHeader(404)
	}
}

// Catches a merge request's state, draft, pipeline, approval, or size read
// wrongly, review read from GitLab's first blocking check, a squash merge
// landing its head rather than its squash commit, a repeated detail read
// failing on an empty 304, a review's system notes shown, an allowed job
// failure failing, a thread losing its file line, checks left stale because an open merge request's update time
// didn't move, a nested project path not escaped as one API path segment,
// the token reaching the cache or detail, the cursor not narrowing the next
// refresh, and a failed refresh dropping what was cached.
func TestForge(t *testing.T) {
	testkit.Isolate(t)
	t.Setenv("BL_GITLAB_TEST_TOKEN", "fake-secret")
	f := &fake{}
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	transport := server.Client().Transport
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Host = u.Host
		return transport.RoundTrip(r)
	})}
	secrets := &remote.Secrets{}
	cache := filepath.Join(t.TempDir(), "forge.pulls.json")
	forge, err := NewForge(Options{Repo: "grp/sub/app", Account: project.AccountConfig{Host: "gitlab.com", User: "alice", Env: "BL_GITLAB_TEST_TOKEN"}, HTTP: client, Scheduler: remote.NewScheduler(), Secrets: secrets, Credentials: &remote.Credentials{Secrets: secrets}}, cache)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := func(status string) map[string]any { return map[string]any{"status": status} }
	open := map[string]any{"iid": "7", "title": "BL-12 fix", "description": "token fake-secret", "webUrl": "https://gitlab.com/grp/sub/app/-/merge_requests/7", "state": "opened", "draft": true, "sourceBranch": "bl-12-fix", "diffHeadSha": "aaa", "sourceProjectId": 1, "targetProjectId": 1, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z", "detailedMergeStatus": "NOT_APPROVED", "diffStatsSummary": map[string]any{"additions": 3, "deletions": 1}, "approvedBy": map[string]any{"nodes": []any{}}, "headPipeline": pipeline("RUNNING")}
	merged := map[string]any{"iid": "6", "title": "Old", "webUrl": "https://gitlab.com/grp/sub/app/-/merge_requests/6", "state": "merged", "sourceBranch": "old", "diffHeadSha": "fff", "sourceProjectId": 2, "targetProjectId": 1, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-03T00:00:00Z", "mergedAt": "2026-01-03T00:00:00Z", "approvedBy": map[string]any{"nodes": []any{map[string]any{"username": "bob"}}}, "headPipeline": pipeline("FAILED")}
	reviewers := func(state string) map[string]any {
		return map[string]any{"nodes": []any{map[string]any{"mergeRequestInteraction": map[string]any{"reviewState": state}}}}
	}
	squashed := map[string]any{"iid": "5", "title": "Squashed", "webUrl": "https://gitlab.com/grp/sub/app/-/merge_requests/5", "state": "merged", "sourceBranch": "sq", "diffHeadSha": "hhh", "squashOnMerge": true, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-02T00:00:00Z", "mergedAt": "2026-01-02T00:00:00Z", "detailedMergeStatus": "UNCHECKED", "approvedBy": map[string]any{"nodes": []any{map[string]any{"username": "bob"}}}, "reviewers": reviewers("REQUESTED_CHANGES")}
	partial := map[string]any{"iid": "4", "title": "Partial", "webUrl": "https://gitlab.com/grp/sub/app/-/merge_requests/4", "state": "opened", "sourceBranch": "p", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z", "detailedMergeStatus": "UNCHECKED", "approved": false, "approvedBy": map[string]any{"nodes": []any{map[string]any{"username": "bob"}}}, "reviewers": reviewers("REVIEWED")}
	merged["approved"] = true
	f.open, f.all = []map[string]any{open, partial}, []map[string]any{merged, squashed, open, partial}
	read := func() map[int]changes.PullRequest {
		t.Helper()
		prs, err := forge.PullRequests(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		out := map[int]changes.PullRequest{}
		for _, pr := range prs {
			out[pr.Number] = pr
		}
		return out
	}
	prs := read()
	if pr := prs[7]; pr.State != changes.StateOpen || !pr.Draft || pr.Head != "bl-12-fix" || pr.HeadSHA != "aaa" || pr.Review != changes.ReviewRequested || pr.Checks != changes.ChecksPending || pr.Additions != 3 || pr.Deletions != 1 || pr.CrossRepository || strings.Contains(pr.Body, "fake-secret") {
		t.Fatalf("open: %+v", pr)
	}
	if pr := prs[6]; pr.State != changes.StateMerged || pr.MergeSHA != "fff" || pr.Review != changes.ReviewApproved || pr.Checks != changes.ChecksFailing || !pr.CrossRepository || pr.Merged.IsZero() {
		t.Fatalf("merged: %+v", pr)
	}
	if pr := prs[5]; pr.MergeSHA != "sss" || pr.Review != changes.ReviewChanges {
		t.Fatalf("squashed: %+v", pr)
	}
	if pr := prs[4]; pr.Review != changes.ReviewRequested {
		t.Fatalf("partly approved: %+v", pr)
	}
	if data, err := os.ReadFile(cache); err != nil || strings.Contains(string(data), "fake-secret") {
		t.Fatalf("cache: %v %s", err, data)
	}
	open["headPipeline"], f.all = pipeline("SUCCESS"), nil
	if prs = read(); prs[7].Checks != changes.ChecksPassing || len(prs) != 4 {
		t.Fatalf("after refresh: %+v", prs)
	}
	if !slices.Equal(f.after, []string{"2026-01-03T00:00:00Z"}) {
		t.Fatalf("cursor: %v", f.after)
	}
	f.fail = true
	if list, err := forge.PullRequests(context.Background()); err == nil || len(list) != 4 {
		t.Fatalf("failed refresh: %v %+v", err, list)
	}
	d, err := forge.Pull(context.Background(), 7)
	if err != nil {
		t.Fatalf("pull: %v %v", err, f.requests)
	}
	if d, err = forge.Pull(context.Background(), 7); err != nil {
		t.Fatalf("repeated pull: %v", err)
	}
	want := []changes.File{{Path: "new.go", From: "old.go", Status: changes.FileRenamed, Additions: 2, Deletions: 1, Patch: "@@ -1 +1 @@\n-a\n+b\n+c\n"}, {Path: "logo.png", Status: changes.FileBinary}, {Path: "gone.go", Status: changes.FileDeleted, Deletions: 2, Patch: "@@ -1,2 +0,0 @@\n-x\n-y\n"}}
	if !slices.Equal(d.Files, want) || len(d.Commits) != 2 || d.Commits[0].SHA != "c2" || strings.Contains(d.Commits[0].Subject, "fake-secret") || d.Commits[1].Author != "Ann" {
		t.Fatalf("pull detail: %+v", d)
	}
	f.fail = false
	r, ok, err := forge.Review(context.Background(), 7, "aaa", false)
	if err != nil || !ok || strings.Contains(r.Body, "fake-secret") || r.Head != "aaa" {
		t.Fatalf("review: %v %+v", err, r)
	}
	wantReviewers := []changes.Reviewer{{Login: "bob", State: changes.ReviewChanges}, {Login: "cy", State: changes.ReviewApproved}}
	wantChecks := []changes.Check{{Name: "test", State: changes.ChecksFailing, URL: "https://gitlab.com/grp/sub/app/-/jobs/1"}, {Name: "lint", State: changes.ChecksSkipped}}
	if !slices.Equal(r.Reviewers, wantReviewers) || !slices.Equal(r.Checks, wantChecks) || len(r.Comments) != 1 || r.Comments[0].Body != "looks fine" {
		t.Fatalf("review: %+v", r)
	}
	if len(r.Threads) != 1 || r.Threads[0].Path != "new.go" || r.Threads[0].Line != 3 || r.Threads[0].Resolved || len(r.Threads[0].Comments) != 2 {
		t.Fatalf("threads: %+v", r.Threads)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
