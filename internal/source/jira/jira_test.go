package jira_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/jira"
	"github.com/brettinternet/backlog/internal/source/remote"
	"github.com/brettinternet/backlog/internal/testkit"
	"github.com/brettinternet/backlog/internal/worklease"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fixture struct {
	t                                                     *testing.T
	key, status, category, resolution, assignee           string
	comments                                              []any
	tokens                                                []string
	denied, empty, limited, badToken, required, failWrite bool
	limitWrite                                            bool
	cloudID                                               string
	wrongSite                                             bool
	writes, searches                                      int
	now                                                   time.Time
	last                                                  map[string]any
	sprints, boardSprints                                 []any
	agileDown                                             bool
	fieldCalls, boardCalls                                int
}

func doc(text string) map[string]any {
	return map[string]any{"type": "doc", "version": 1, "content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": text}}}}}
}
func (f *fixture) row(id string) map[string]any {
	var resolution any
	if f.resolution != "" {
		resolution = map[string]string{"id": "7", "name": f.resolution}
	}
	var assignee any
	if f.assignee != "" {
		assignee = map[string]string{"accountId": f.assignee}
	}
	links := []any{}
	if id == "10001" {
		links = append(links, map[string]any{"id": "9", "type": map[string]string{"id": "1", "name": "Blocks"}, "inwardIssue": map[string]string{"id": "10002", "key": "OTHER-2"}})
	}
	fields := map[string]any{"summary": "Synthetic", "description": doc("café & body"), "status": map[string]any{"id": "1", "name": f.status, "statusCategory": map[string]string{"key": f.category}}, "resolution": resolution, "assignee": assignee, "labels": []string{"synthetic"}, "created": "2026-10-22T09:39:39.500-0600", "updated": "2026-10-23T00:00:00.000+0000", "issuelinks": links}
	if id == "10001" && f.sprints != nil {
		fields["customfield_10020"] = f.sprints
	}
	return map[string]any{"id": id, "key": f.key, "fields": fields}
}
func setup(t *testing.T, cloudIDs ...string) (*jira.Source, *fixture, *remote.Secrets) {
	t.Helper()
	testkit.Isolate(t)
	t.Setenv("BL_JIRA_TEST_TOKEN", "jira-canary-secret")
	f := &fixture{t: t, key: "KAN-1", status: "To Do", category: "new", comments: []any{}, now: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}
	if len(cloudIDs) > 0 {
		f.cloudID = cloudIDs[0]
	}
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	transport := server.Client().Transport
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.URL.Host = u.Host
		return transport.RoundTrip(copy)
	})}
	secrets := &remote.Secrets{}
	s, err := jira.New(jira.Options{ID: "jira", Site: "https://Example.atlassian.net/", CloudID: f.cloudID, JQL: "project = KAN ORDER BY id ASC", HardLinkTypes: []string{"Blocks"}, AllowWrites: true, Account: project.AccountConfig{User: "user-1", Email: "user@example.test", Env: "BL_JIRA_TEST_TOKEN"}, HTTP: client, Scheduler: remote.NewSchedulerWithClock(func() time.Time { return f.now }), Secrets: secrets, Credentials: &remote.Credentials{Secrets: secrets}})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, secrets
}
func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	user, password, ok := r.BasicAuth()
	if !ok || user != "user@example.test" || password != "jira-canary-secret" {
		f.t.Error("missing explicit Basic credential")
		w.WriteHeader(401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	prefix := ""
	if f.cloudID != "" {
		prefix = "/ex/jira/" + f.cloudID
	}
	if board, ok := strings.CutPrefix(r.URL.Path, prefix+"/rest/agile/1.0/board/"); ok {
		f.boardCalls++
		if f.agileDown || board != "7/sprint" || r.URL.Query().Get("state") != "active,future" {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"values": f.boardSprints})
		return
	}
	if !strings.HasPrefix(r.URL.Path, prefix+"/rest/api/3/") {
		f.t.Errorf("wrong gateway path: %s", r.URL.Path)
	}
	path := strings.TrimPrefix(r.URL.Path, prefix+"/rest/api/3/")
	var result any
	switch {
	case path == "serverInfo":
		site := "https://example.atlassian.net"
		if f.wrongSite {
			site = "https://other.atlassian.net"
		}
		result = map[string]string{"baseUrl": site}
	case path == "field":
		f.fieldCalls++
		result = []any{
			map[string]any{"id": "customfield_10001", "name": "Sprint", "schema": map[string]string{"custom": "com.example:sprint-lookalike"}},
			map[string]any{"id": "customfield_10020", "name": "Iteration", "schema": map[string]string{"custom": "com.pyxis.greenhopper.jira:gh-sprint"}},
		}
	case path == "myself":
		result = map[string]any{"accountId": "user-1", "active": true}
	case path == "search/jql":
		f.searches++
		if f.limited {
			f.limited = false
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(429)
			return
		}
		if f.denied {
			w.WriteHeader(403)
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			f.t.Error("invalid search")
		}
		if body["jql"] != "project = KAN ORDER BY id ASC" {
			f.t.Error("JQL scope lost")
		}
		token, _ := body["nextPageToken"].(string)
		f.tokens = append(f.tokens, token)
		switch {
		case f.empty:
			result = map[string]any{"issues": []any{}, "isLast": true}
		case token == "":
			result = map[string]any{"issues": []any{f.row("10001")}, "isLast": false, "nextPageToken": "opaque/one+="}
		case token == "opaque/one+=":
			next := "terminal"
			if f.badToken {
				next = token
			}
			result = map[string]any{"issues": []any{f.row("10002")}, "isLast": false, "nextPageToken": next}
		default:
			result = map[string]any{"issues": []any{}, "isLast": true}
		}
	case strings.HasSuffix(path, "/transitions") && r.Method == http.MethodGet:
		fields := map[string]any{}
		if f.required {
			fields["customfield_1"] = map[string]any{"required": true, "hasDefaultValue": false}
		}
		result = map[string]any{"transitions": []any{map[string]any{"id": "21", "name": "Start", "to": map[string]any{"id": "2", "name": "In Progress", "statusCategory": map[string]string{"key": "indeterminate"}}, "fields": fields}}}
	case strings.HasSuffix(path, "/comment") && r.Method == http.MethodGet:
		result = map[string]any{"startAt": 0, "total": len(f.comments), "comments": f.comments}
	case r.Method == http.MethodGet:
		if f.denied {
			w.WriteHeader(404)
			return
		}
		result = f.row(strings.TrimPrefix(path, "issue/"))
	default:
		f.writes++
		if json.NewDecoder(r.Body).Decode(&f.last) != nil {
			f.t.Error("invalid write body")
		}
		if f.failWrite {
			w.WriteHeader(500)
			return
		}
		if f.limitWrite {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(429)
			return
		}
		switch {
		case strings.HasSuffix(path, "/transitions"):
			f.status, f.category = "In Progress", "indeterminate"
		case strings.HasSuffix(path, "/assignee"):
			f.assignee, _ = f.last["accountId"].(string)
		case strings.HasSuffix(path, "/comment"):
			f.comments = append(f.comments, map[string]any{"id": "10", "created": "2026-10-23T01:00:00.000+0000", "author": map[string]string{"accountId": "user-1"}, "body": f.last["body"]})
		}
		w.WriteHeader(204)
		return
	}
	if err := json.NewEncoder(w).Encode(result); err != nil {
		f.t.Error(err)
	}
}
func read(t *testing.T, s source.Source) model.Item {
	t.Helper()
	item, err := s.Read(context.Background(), "10001")
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func reason(err error) string {
	var refusal *source.Error
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	return ""
}

// A scoped token gateway must not silently bind the wrong site to claim keys.
func TestScopedGatewaySiteBinding(t *testing.T) {
	s, f, _ := setup(t, "cloud-1")
	item := read(t, s)
	if item.Fields["site"] != "https://example.atlassian.net" {
		t.Fatal("gateway replaced stable site identity")
	}
	f.wrongSite = true
	if _, err := s.Read(context.Background(), "10001"); err == nil {
		t.Fatal("wrong cloudId/site binding accepted")
	}
}

// Search tokens must be followed through the empty terminal page, while a
// permission loss or empty visible search must not delete cached issues.
func TestPagingCoveragePermissionAndRetry(t *testing.T) {
	s, f, secrets := setup(t)
	ctx := context.Background()
	wrapped := remote.New(s, cache.New(filepath.Join(t.TempDir(), "cache"), "project", "jira", "scope"), secrets)
	delta, err := wrapped.Load(ctx, source.Cursor{})
	if err != nil || delta.Coverage != source.Partial || len(delta.Items) != 2 || len(f.tokens) != 3 || f.tokens[1] != "opaque/one+=" {
		t.Fatalf("paging: %+v %v tokens=%v", delta, err, f.tokens)
	}
	if len(delta.Findings) == 0 || len(delta.Items[0].BlockedBy) != 1 || delta.Items[0].BlockedBy[0].ID != "10002" {
		t.Fatalf("missing partial evidence or hard edge: %+v", delta)
	}
	f.empty = true
	delta, err = wrapped.Load(ctx, source.Cursor{})
	if err != nil || len(delta.Items) != 2 || !delta.Stale[delta.Items[0].Ref] {
		t.Fatalf("empty search erased cached rows: %+v %v", delta, err)
	}
	f.empty = false
	f.denied = true
	_, err = wrapped.Load(ctx, source.Cursor{})
	if err == nil {
		t.Fatal("permission loss was not reported")
	}
	delta, err = wrapped.LoadInitial(ctx)
	if err != nil || delta.Coverage != source.Unknown || len(delta.Items) != 2 || !delta.Stale[delta.Items[0].Ref] {
		t.Fatalf("permission loss erased cache: %+v %v", delta, err)
	}
	if _, err = s.Read(ctx, "10001"); err == nil {
		t.Fatal("404 treated as valid issue")
	}
	f.denied = false
	f.limited = true
	_, err = s.Load(ctx, source.Cursor{})
	var diagnostic remote.ScheduleDiagnostic
	if !errors.As(err, &diagnostic) || diagnostic.Code != "rate-limited" || !diagnostic.RetryAt.Equal(f.now.Add(2*time.Second)) {
		t.Fatalf("rate limit: %v", err)
	}
	requests := f.searches
	if _, err = s.Load(ctx, source.Cursor{}); err == nil || f.searches != requests {
		t.Fatal("retry sent before deadline")
	}
	f.now = f.now.Add(2 * time.Second)
	if delta, err = s.Load(ctx, source.Cursor{}); err != nil || len(delta.Items) != 2 {
		t.Fatalf("retry after deadline: %+v %v", delta, err)
	}
	f.badToken = true
	if _, err = s.Load(ctx, source.Cursor{}); err == nil {
		t.Fatal("repeated page token accepted")
	}
}

// A cross-project display-key change must not rekey a claim. Resolution and
// dependency changes remain visible even if Jira's updated timestamp is unchanged.
func TestMoveIdentityAndUnsuccessfulResolution(t *testing.T) {
	s, f, _ := setup(t)
	before := read(t, s)
	cfg := project.Config{Settings: project.Settings{Sources: []project.SourceConfig{{ID: "jira", Type: "jira", Site: "https://EXAMPLE.atlassian.net/", Project: "KAN"}}}}
	first, err := worklease.New(cfg).Resource(before)
	if err != nil {
		t.Fatal(err)
	}
	f.key = "MOVED-72"
	f.status, f.category, f.resolution = "Closed", "done", "Won't Do"
	after := read(t, s)
	cfg.Sources[0].Project = "MOVED"
	second, err := worklease.New(cfg).Resource(after)
	want, _ := worklease.Key("generic", "https://example.atlassian.net", "10001")
	if err != nil || first != second || second != want || after.Category != model.Canceled || after.Status != "Closed" || after.Fields["key"] != "MOVED-72" || before.Version == after.Version {
		t.Fatalf("move/resolution: %s %s %s %+v %v", first, second, want, after, err)
	}
}

// Writes must refuse stale comments/relations/workflows, unoffered transitions,
// and unconfirmed single-assignee replacement before sending any mutation.
func TestDiscoveredTransitionsAndVersionedWrites(t *testing.T) {
	s, f, _ := setup(t)
	ctx := context.Background()
	item := read(t, s)
	mutation := model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "status", Value: "In Progress"}
	f.required = true
	if got := read(t, s).Fields["jiraTransitions"].(map[string]any); len(got) != 0 {
		t.Fatalf("required-field transition offered: %v", got)
	}
	if _, err := s.Apply(ctx, mutation); reason(err) != "conflict" || f.writes != 0 {
		t.Fatalf("changed workflow: %v", err)
	}
	f.required = false
	mutation.Value = "Done"
	if _, err := s.Apply(ctx, mutation); reason(err) != "rejected" || f.writes != 0 {
		t.Fatalf("unavailable transition: %v", err)
	}
	mutation.Value = "In Progress"
	updated, err := s.Apply(ctx, mutation)
	if err != nil || updated.Category != model.Active || f.writes != 1 {
		t.Fatalf("transition: %+v %v", updated, err)
	}
	if _, err = s.Apply(ctx, mutation); reason(err) != "conflict" || f.writes != 1 {
		t.Fatalf("stale transition: %v", err)
	}
	mutation = model.Mutation{Ref: item.Ref, ExpectedVersion: updated.Version, Op: model.Set, Field: "assignees", Value: []string{"new-user"}}
	if _, err = s.Apply(ctx, mutation); reason(err) != "rejected" || f.writes != 1 {
		t.Fatalf("missing confirmation: %v", err)
	}
	mutation.Confirmed = true
	updated, err = s.Apply(ctx, model.Mutation{Ref: item.Ref, ExpectedVersion: updated.Version, Op: model.Batch, Changes: []model.Mutation{mutation}, Confirmed: true})
	if err != nil || len(updated.Assignees) != 1 || updated.Assignees[0] != "new-user" {
		t.Fatalf("replacement: %+v %v", updated, err)
	}
	mutation = model.Mutation{Ref: item.Ref, ExpectedVersion: updated.Version, Op: model.AppendComment, Text: "hello & café\nsecond line"}
	updated, err = s.Apply(ctx, mutation)
	if err != nil || len(updated.Comments) != 1 || updated.Comments[0].Body != "hello & café\nsecond line" {
		t.Fatalf("ADF comment: %+v %v", updated, err)
	}
	if _, err = s.Apply(ctx, mutation); reason(err) != "conflict" || f.writes != 3 {
		t.Fatalf("comment state not versioned: %v", err)
	}
	f.failWrite = true
	mutation.ExpectedVersion = updated.Version
	if _, err = s.Apply(ctx, mutation); reason(err) != "unknown-outcome" || f.writes != 4 {
		t.Fatalf("uncertain mutation retried or hidden: %v writes=%d", err, f.writes)
	}
	// A 429 proves the write was not applied; report when to retry, not an unknown outcome.
	f.failWrite, f.limitWrite = false, true
	var diagnostic remote.ScheduleDiagnostic
	if _, err = s.Apply(ctx, mutation); !errors.As(err, &diagnostic) || diagnostic.Code != "rate-limited" {
		t.Fatalf("rate-limited write: %v", err)
	}
}

func sprintRow(id int, state, start, complete string) map[string]any {
	row := map[string]any{"id": id, "name": "Sprint " + strconv.Itoa(id), "state": state, "boardId": 7}
	if start != "" {
		row["startDate"] = start
	}
	if complete != "" {
		row["completeDate"] = complete
	}
	return row
}

// Catches a multi-sprint issue resolving to a stale closed sprint or a later
// future one, a hardcoded sprint field, and sprint requests or version drift
// when no caller asked for sprints.
func TestSprintSelectionAndCatalog(t *testing.T) {
	s, f, _ := setup(t)
	ctx := context.Background()
	f.sprints = []any{
		sprintRow(40, "closed", "2026-09-01T00:00:00.000Z", "2026-09-14T00:00:00.000Z"),
		sprintRow(43, "future", "2026-11-15T00:00:00.000Z", ""),
		sprintRow(42, "future", "2026-11-01T00:00:00.000Z", ""),
		sprintRow(41, "closed", "2026-09-15T00:00:00.000Z", "2026-10-01T00:00:00.000Z"),
	}
	f.boardSprints = []any{sprintRow(42, "future", "2026-11-01T00:00:00.000Z", ""), sprintRow(43, "future", "2026-11-15T00:00:00.000Z", ""), sprintRow(44, "future", "2026-11-29T00:00:00.000Z", "")}
	plain, err := s.Load(ctx, source.Cursor{})
	if err != nil || f.fieldCalls+f.boardCalls != 0 || plain.Sprints != nil || plain.Items[0].Sprint != "" {
		t.Fatalf("sprint work without demand: field=%d board=%d %+v %v", f.fieldCalls, f.boardCalls, plain.Sprints, err)
	}
	d, err := s.Load(ctx, source.Cursor{Sprints: true})
	if err != nil {
		t.Fatal(err)
	}
	item := d.Items[0]
	if item.Sprint != "example.atlassian.net/sprint/42" || item.Names[item.Sprint] != "Sprint 42" || item.Version != plain.Items[0].Version {
		t.Fatalf("selected %q (%q), version changed %v", item.Sprint, item.Names[item.Sprint], item.Version != plain.Items[0].Version)
	}
	states := map[string]model.SprintState{}
	for _, sprint := range d.Sprints {
		states[strings.TrimPrefix(sprint.Key, "example.atlassian.net/sprint/")] = sprint.State
	}
	if len(states) != 5 || states["44"] != model.SprintFuture || states["41"] != model.SprintClosed || f.fieldCalls != 1 || f.boardCalls != 1 {
		t.Fatalf("catalog %v field=%d board=%d", states, f.fieldCalls, f.boardCalls)
	}
	// Active wins over future; with only closed sprints the latest closed wins.
	f.sprints = append(f.sprints, sprintRow(45, "active", "2026-10-20T00:00:00.000Z", ""))
	if item = read(t, s); item.Sprint != "example.atlassian.net/sprint/45" {
		t.Fatalf("active sprint not selected: %q", item.Sprint)
	}
	f.sprints = []any{sprintRow(41, "closed", "", "2026-10-01T00:00:00.000Z"), sprintRow(40, "closed", "", "2026-09-14T00:00:00.000Z")}
	if item = read(t, s); item.Sprint != "example.atlassian.net/sprint/41" {
		t.Fatalf("latest closed sprint not selected: %q", item.Sprint)
	}
	// A closed board sprint list degrades to issue-embedded states with a warning.
	f.agileDown = true
	d, err = s.Load(ctx, source.Cursor{Sprints: true})
	if err != nil || len(d.Sprints) != 2 || f.fieldCalls != 1 || !slices.ContainsFunc(d.Findings, func(f source.Finding) bool { return f.Code == "jira-agile-unavailable" }) {
		t.Fatalf("agile fallback: %+v %+v %v", d.Sprints, d.Findings, err)
	}
}
