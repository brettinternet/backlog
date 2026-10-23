package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
	"github.com/brettinternet/backlog/internal/testkit"
)

type fixture struct {
	mu                                              sync.Mutex
	option, state                                   string
	labels, assignees                               []string
	writes, lists, hints, notModified, projectPages int
	limited, readOnly                               bool
}

func newFixture(t *testing.T) (*Source, *fixture) {
	t.Helper()
	testkit.Isolate(t)
	t.Setenv("BL_GITHUB_TEST_TOKEN", "fake-secret")
	f := &fixture{option: "todo", state: "OPEN"}
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	transport := server.Client().Transport
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.URL.Host = u.Host
		return transport.RoundTrip(copy)
	})}
	secrets := &remote.Secrets{}
	s, err := New(Options{ID: "gh", Repo: "owner/repo", Account: project.AccountConfig{Host: "github.com", User: "alice", Env: "BL_GITHUB_TEST_TOKEN"}, Project: &project.GitHubProjectConfig{ID: "PVT_test", Owner: "alice", Number: 9, FieldID: "PVTSSF_status", Options: map[string]string{"todo": "todo", "active": "active"}, AllowWrites: true}, HTTP: client, Scheduler: remote.NewScheduler(), Secrets: secrets, Credentials: &remote.Credentials{Secrets: secrets}})
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func (f *fixture) row(n int) map[string]any {
	nodes := func(v any) map[string]any {
		return map[string]any{"nodes": v, "pageInfo": map[string]any{"hasNextPage": false}}
	}
	labels := []map[string]string{}
	for _, v := range f.labels {
		labels = append(labels, map[string]string{"name": v})
	}
	assignees := []map[string]string{}
	for _, v := range f.assignees {
		assignees = append(assignees, map[string]string{"login": v})
	}
	return map[string]any{"id": fmt.Sprint("I_", n), "number": n, "title": "Issue", "body": "Body", "state": f.state, "stateReason": "COMPLETED", "updatedAt": "2026-01-01T00:00:00Z", "repository": map[string]string{"nameWithOwner": "owner/repo"}, "labels": nodes(labels), "assignees": nodes(assignees), "blockedBy": nodes([]any{map[string]any{"id": "I_2", "number": 2, "repository": map[string]string{"nameWithOwner": "owner/repo"}}}), "subIssues": nodes([]any{}), "parent": map[string]any{"id": "I_2", "number": 2, "repository": map[string]string{"nameWithOwner": "owner/repo"}}}
}
func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer fake-secret" {
		w.WriteHeader(401)
		return
	}
	w.Header().Set("X-OAuth-Scopes", "repo, project")
	if r.Method == http.MethodGet {
		f.hints++
		if f.limited {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"message":"You have exceeded a secondary rate limit"}`)
			return
		}
		if r.Header.Get("If-None-Match") == `"hint"` {
			f.notModified++
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", `"hint"`)
		fmt.Fprint(w, `[]`)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "graphql") {
		f.writes++
		var payload map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if v := payload["state"]; v != nil {
			var state string
			_ = json.Unmarshal(v, &state)
			f.state = strings.ToUpper(state)
		}
		if v := payload["labels"]; v != nil {
			_ = json.Unmarshal(v, &f.labels)
		}
		if v := payload["assignees"]; v != nil {
			_ = json.Unmarshal(v, &f.assignees)
		}
		fmt.Fprint(w, `{}`)
		return
	}
	var request struct {
		Query     string
		Variables map[string]any
	}
	_ = json.NewDecoder(r.Body).Decode(&request)
	q, v := request.Query, request.Variables
	emit := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": data}) }
	switch {
	case strings.Contains(q, "viewer {login}"):
		emit(map[string]any{"viewer": map[string]string{"login": "alice"}})
	case strings.Contains(q, "viewerPermission"):
		permission := "WRITE"
		if f.readOnly {
			permission = "READ"
		}
		emit(map[string]any{"repository": map[string]string{"id": "R_test", "nameWithOwner": "owner/repo", "viewerPermission": permission}})
	case strings.Contains(q, "updateProjectV2ItemFieldValue"):
		f.writes++
		f.option = v["optionID"].(string)
		emit(map[string]any{"updateProjectV2ItemFieldValue": map[string]any{"projectV2Item": map[string]string{"id": "PVTI_1"}}})
	case strings.Contains(q, "fields(first:"):
		emit(map[string]any{"node": map[string]any{"id": "PVT_test", "number": 9, "owner": map[string]string{"login": "alice"}, "fields": map[string]any{"nodes": []any{map[string]any{"id": "PVTSSF_status", "name": "Status", "options": []any{map[string]string{"id": "todo", "name": "Todo"}, map[string]string{"id": "active", "name": "Active"}}}}, "pageInfo": map[string]any{"hasNextPage": false}}}})
	case strings.Contains(q, "items(first:"):
		f.projectPages++
		rows := []any{}
		next := v["after"] == nil
		if next {
			rows = append(rows, map[string]any{"id": "PVTI_1", "updatedAt": "2026-01-01T00:00:00Z", "content": map[string]any{"__typename": "Issue", "id": "I_1", "number": 1, "repository": map[string]string{"nameWithOwner": "owner/repo"}}, "fieldValueByName": map[string]any{"optionId": f.option, "name": map[string]string{"todo": "Todo", "active": "In Progress"}[f.option], "field": map[string]string{"id": "PVTSSF_status"}}})
		}
		emit(map[string]any{"node": map[string]any{"id": "PVT_test", "number": 9, "owner": map[string]string{"login": "alice"}, "items": map[string]any{"nodes": rows, "pageInfo": map[string]any{"hasNextPage": next, "endCursor": "project-next"}}}})
	case strings.Contains(q, "issues(first:"):
		f.lists++
		n := 1
		next := v["after"] == nil
		if !next {
			n = 2
		}
		emit(map[string]any{"repository": map[string]any{"id": "R_test", "nameWithOwner": "owner/repo", "issues": map[string]any{"nodes": []any{f.row(n)}, "pageInfo": map[string]any{"hasNextPage": next, "endCursor": "issue-next"}}}})
	case strings.Contains(q, "issue(number:"):
		n := int(v["number"].(float64))
		emit(map[string]any{"repository": map[string]any{"id": "R_test", "nameWithOwner": "owner/repo", "issue": f.row(n)}})
	default:
		w.WriteHeader(400)
	}
}

// A REST 304 or unchanged issue timestamp must not hide a project-only edit,
// pagination must not drop issues/items, and secondary limits must halt dispatch.
func TestReconciliationPaginationETagAndBackoff(t *testing.T) {
	s, f := newFixture(t)
	ctx := context.Background()
	first, err := s.Load(ctx, source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Coverage != source.Complete || first.Items[0].Category != model.Todo || len(first.Items[0].BlockedBy) != 1 || first.Items[0].Parent == nil {
		t.Fatalf("incomplete read: %+v", first)
	}
	f.mu.Lock()
	f.option = "active"
	f.mu.Unlock()
	second, err := s.Load(ctx, source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Items[0].Category != model.Active || second.Items[0].Version == first.Items[0].Version || !second.Items[0].Updated.Equal(first.Items[0].Updated) {
		t.Fatal("project-only update missed")
	}
	f.mu.Lock()
	if f.notModified != 1 || f.lists != 4 || f.projectPages != 4 {
		t.Fatalf("pagination/304: %+v", f)
	}
	f.limited = true
	f.mu.Unlock()
	_, err = s.Load(ctx, source.Cursor{})
	var limited remote.ScheduleDiagnostic
	if !errors.As(err, &limited) || !limited.RetryAt.After(time.Now()) {
		t.Fatalf("secondary limit: %v", err)
	}
	f.mu.Lock()
	calls := f.hints
	f.mu.Unlock()
	_, err = s.Load(ctx, source.Cursor{})
	if !errors.As(err, &limited) {
		t.Fatalf("backoff missing: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hints != calls {
		t.Fatal("request escaped shared account backoff")
	}
}

// Stale project-only versions, missing versions, denied capabilities and unknown
// fields must never dispatch writes. Accepted writes exercise each public surface.
func TestVersionedCapabilityGatedWrites(t *testing.T) {
	s, f := newFixture(t)
	ctx := context.Background()
	current, err := s.Read(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	apply := func(field string, value any, version string) (model.Item, error) {
		return s.Apply(ctx, model.Mutation{Ref: current.Ref, Op: model.Set, Field: field, Value: value, ExpectedVersion: version})
	}
	f.mu.Lock()
	f.option = "active"
	f.mu.Unlock()
	if _, err = apply("status", "closed", current.Version); !reason(err, "conflict") {
		t.Fatalf("stale project version: %v", err)
	}
	if _, err = apply("status", "closed", ""); !reason(err, "conflict") {
		t.Fatalf("missing version: %v", err)
	}
	current, err = s.Read(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.readOnly = true
	f.mu.Unlock()
	if _, err = apply("status", "closed", current.Version); !reason(err, "rejected") {
		t.Fatalf("permission: %v", err)
	}
	f.mu.Lock()
	f.readOnly = false
	if f.writes != 0 {
		t.Fatal("refused writes dispatched")
	}
	f.mu.Unlock()
	for _, change := range []struct {
		field string
		value any
	}{{"labels", []string{"bug"}}, {"assignees", []string{"alice"}}, {"status", "todo"}, {"status", "closed"}} {
		current, err = apply(change.field, change.value, current.Version)
		if err != nil {
			t.Fatalf("%s: %v", change.field, err)
		}
	}
	current, err = s.Apply(ctx, model.Mutation{Ref: current.Ref, Op: model.AppendComment, Text: "hello", ExpectedVersion: current.Version})
	if err != nil {
		t.Fatal(err)
	}
	if current.Category != model.Done || len(current.Labels) != 1 || len(current.Assignees) != 1 {
		t.Fatalf("write read-back: %+v", current)
	}
	if _, err = apply("title", "not supported", current.Version); !reason(err, "rejected") {
		t.Fatal(err)
	}
	s.o.Project.AllowWrites = false
	if _, err = apply("status", "active", current.Version); !reason(err, "rejected") {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 5 {
		t.Fatalf("writes=%d", f.writes)
	}
}

// Display names must not replace immutable option IDs in queries or inverses.
func TestProjectStatusQueryAndInverse(t *testing.T) {
	backend, f := newFixture(t)
	s := remote.New(backend, cache.New(t.TempDir(), "test", "gh", "owner/repo"), backend.o.Secrets)
	ctx := context.Background()
	before, err := s.Read(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	q, err := query.Parse("status:todo", query.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != "todo" || before.Fields["projectStatus"] != "Todo" || !q.Match(before, query.Snapshot{}, nil) {
		t.Fatalf("project status ID/name: %+v", before)
	}
	m := model.Mutation{Ref: before.Ref, ExpectedVersion: before.Version, Op: model.Set, Field: "status", Value: "active"}
	prepared, err := s.Prepare(ctx, m)
	if err != nil || prepared.Inverse.Value != "todo" {
		t.Fatalf("inverse: %+v %v", prepared, err)
	}
	after, err := s.Apply(ctx, m)
	if err != nil || after.Status != "active" || after.Fields["projectStatus"] != "In Progress" {
		t.Fatalf("status write: %+v %v", after, err)
	}
	inverse := prepared.Inverse
	inverse.ExpectedVersion = after.Version
	restored, err := s.Apply(ctx, inverse)
	if err != nil || restored.Status != "todo" || restored.Fields["projectStatus"] != "Todo" {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.option != "todo" || f.writes != 2 {
		t.Fatalf("inverse did not restore option ID: option=%s writes=%d", f.option, f.writes)
	}
}

func reason(err error, want string) bool {
	var e *source.Error
	return errors.As(err, &e) && e.Reason == want
}
func FuzzProviderJSON(f *testing.F) {
	f.Add([]byte(`{"data":{"repository":{"issue":{"id":"I_1","number":1}}}}`))
	f.Add([]byte(`{"errors":[{"message":"forbidden"}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var data struct {
			Repository *struct{ Issue *issue }
			Node       *struct{ Items struct{ Nodes []projectItem } }
		}
		_ = decode(raw, &data)
	})
}
