package engine_test

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
	"testing"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

// Catches a pull request listed beside the item that links it, by any of
// linked changes' rules, or hidden by its own URL or by an item in another
// workspace project; show disagreeing with ls; a held claim on a pull
// request that can't be released once an item links it; and an adopting
// create that leaves its pull request listed.
func TestLinkedPullsHidden(t *testing.T) {
	testkit.Isolate(t)
	pulls := []map[string]any{
		{"number": 41, "title": "Fix thing", "headRefName": "bl-1-fix"},
		{"number": 42, "title": "BL-2 polish", "headRefName": "feature"},
		{"number": 43, "title": "Cleanup", "body": "Closes BL-3", "headRefName": "cleanup"},
		{"number": 44, "title": "Docs", "headRefName": "docs"},
		{"number": 45, "title": "BL-5 shared", "headRefName": "shared"},
		{"number": 46, "title": "Unrelated", "headRefName": "unrelated"},
		{"number": 47, "title": "Elsewhere", "headRefName": "elsewhere"},
	}
	for _, pr := range pulls {
		pr["url"] = fmt.Sprintf("https://github.com/owner/repo/pull/%d", pr["number"])
		pr["state"], pr["createdAt"], pr["updatedAt"] = "OPEN", "2026-10-22T00:00:00Z", "2026-10-22T00:00:00Z"
		pr["author"] = map[string]any{"login": "ann", "__typename": "User"}
		pr["commits"] = map[string]any{"nodes": []any{}}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Query string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "viewer") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"viewer": map[string]any{"login": "alice"}}})
			return
		}
		if !strings.Contains(req.Query, "pullRequests(") {
			t.Errorf("unexpected request %s %q", r.URL.Path, req.Query)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequests": map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": pulls}}}})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	original := http.DefaultTransport
	http.DefaultTransport = readRoundTrip(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.URL.Host = u.Host
		return server.Client().Transport.RoundTrip(copy)
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	t.Setenv("BL_PULLS_TOKEN", "pulls-test-credential")

	base := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	item := func(root, dir, id, extra string) {
		write(filepath.Join(root, dir, id+".md"), fmt.Sprintf("---\nid: %s\ntitle: Item %s\nstatus: todo\ncreated: 2026-10-22T00:00:00Z\nupdated: 2026-10-22T00:00:00Z\n%s---\n", id, id, extra))
	}
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	for _, root := range []string{a, b} {
		write(filepath.Join(root, ".git", "config"), "[core]\n bare = false\n")
	}
	write(filepath.Join(a, "backlog.yaml"), "version: 1\nproject: {id: a}\nsources: [{id: tasks, type: native, path: items, prefix: BL}, {id: more, type: native, path: more, prefix: BL}, {id: prs, type: github-pulls, repo: owner/repo}]\nstatuses: [{key: todo, name: Todo, category: todo}, {key: done, name: Done, category: done}]\ncoordination: {worklease: {}}\n")
	write(filepath.Join(b, "backlog.yaml"), "version: 1\nproject: {id: b}\nsources: [{id: tasks, type: native, path: items, prefix: BL}]\nstatuses: [{key: todo, name: Todo, category: todo}, {key: done, name: Done, category: done}]\n")
	for _, id := range []string{"BL-1", "BL-2", "BL-3", "BL-5"} {
		item(a, "items", id, "")
	}
	item(a, "items", "BL-4", "refs: [\"https://github.com/owner/repo/pull/44\"]\n")
	item(a, "more", "BL-5", "")
	item(b, "items", "BL-9", "refs: [\"https://github.com/owner/repo/pull/47\"]\n")
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	write(paths.ConfigFile, fmt.Sprintf("accounts:\n  github:\n    - name: work\n      host: github.com\n      user: alice\n      env: BL_PULLS_TOKEN\nworkspaces:\n  - name: all\n    projects: [%q, %q]\n", a, b))
	cfg, err := project.Resolve(paths, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.GrantTrust(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	visible := func(cfg project.Config) []string {
		t.Helper()
		reads, err := engine.LoadReads(ctx, cfg, false)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, i := range reads.Query.Items {
			if strings.HasPrefix(i.Ref.ID, "#") {
				out = append(out, i.Ref.ID)
			}
		}
		slices.Sort(out)
		return out
	}
	if got := visible(cfg); !slices.Equal(got, []string{"#46", "#47"}) {
		t.Fatalf("project pull requests: %v", got)
	}
	// Adoption is one create carrying the URL in refs, as bl new --ref,
	// MCP create, and the TUI send it.
	adopted := model.Item{Title: "Adopt", Status: "todo", Refs: []string{"https://github.com/owner/repo/pull/46"}}
	if _, err := engine.ApplyWrite(ctx, cfg, model.Mutation{Op: model.Create, Ref: model.Ref{Source: "tasks"}, Item: &adopted}); err != nil {
		t.Fatal(err)
	}
	if got := visible(cfg); !slices.Equal(got, []string{"#47"}) {
		t.Fatalf("pull requests after adoption: %v", got)
	}
	workspace, err := project.OpenWorkspace(cfg, "all", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := visible(workspace); !slices.Equal(got, []string{"#47"}) {
		t.Fatalf("workspace pull requests: %v", got)
	}

	// show refuses a hidden pull request, naming every linker; release still
	// resolves it.
	hidden, err := engine.ReadDetail(ctx, cfg, "prs:#45")
	if err != nil || len(hidden) != 1 {
		t.Fatalf("raw read of a hidden pull request: %v %+v", err, hidden)
	}
	if err := engine.HiddenPull(ctx, cfg, hidden[0]); engine.ErrorReason(err) != "not-found" || !strings.Contains(err.Error(), "prs:#45 is linked to more:BL-5, tasks:BL-5") {
		t.Fatalf("show of a hidden pull request: %v", err)
	}
	shown, _ := engine.ReadDetail(ctx, cfg, "prs:#47")
	if err := engine.HiddenPull(ctx, cfg, shown[0]); err != nil {
		t.Fatalf("show of an unlinked pull request: %v", err)
	}
	c := &coordinator{state: model.SessionState{State: "held", Claim: &model.ClaimInfo{ID: "claim", Resources: []string{"prs:#45"}, Session: "loop"}}}
	if result := engine.ReleaseItem(engine.WithCoordination(ctx, c, model.CallerContext{Session: "loop"}), hidden[0]); result.Outcome != "released" {
		t.Fatalf("release of a hidden held pull request: %+v", result)
	}
}
