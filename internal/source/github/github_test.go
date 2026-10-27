package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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
	option, state, title, body                      string
	labels, assignees                               []string
	failWrites                                      bool
	writes, lists, hints, notModified, projectPages int
	limited, readOnly                               bool
	oauthApp, issuePermission, projectPermission    string
	repositoryMissing                               bool
	personalProject                                 bool
	// iterationFieldID, secondIterationFieldID, iterations and
	// completedIterations configure the Projects v2 iteration field returned
	// by discovery; a project with none left unset is not sprint-capable.
	// itemIterationID assigns the single project item to one of them.
	iterationFieldID, secondIterationFieldID string
	iterations, completedIterations          []map[string]any
	itemIterationID                          string
	lastFieldsQuery, lastSprintFieldName     string
}

func newFixture(t *testing.T) (*Source, *fixture) {
	t.Helper()
	testkit.Isolate(t)
	t.Setenv("BL_GITHUB_TEST_TOKEN", "fake-secret")
	f := &fixture{option: "todo", state: "OPEN", title: "Issue", body: "Body"}
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
	return map[string]any{"id": fmt.Sprint("I_", n), "number": n, "title": f.title, "body": f.body, "state": f.state, "stateReason": "COMPLETED", "updatedAt": "2026-01-01T00:00:00Z", "repository": map[string]string{"nameWithOwner": "owner/repo"}, "labels": nodes(labels), "assignees": nodes(assignees), "blockedBy": nodes([]any{map[string]any{"id": "I_2", "number": 2, "repository": map[string]string{"nameWithOwner": "owner/repo"}}}), "subIssues": nodes([]any{}), "parent": map[string]any{"id": "I_2", "number": 2, "repository": map[string]string{"nameWithOwner": "owner/repo"}}}
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
		if r.URL.Path == "/user/installations" {
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "installations": []any{map[string]any{"id": 42, "app_slug": f.oauthApp, "account": map[string]string{"login": "owner", "type": "Organization"}, "permissions": map[string]string{"issues": f.issuePermission, "organization_projects": f.projectPermission}}}})
			return
		}
		if r.URL.Path == "/user/installations/42/repositories" {
			repos := []any{}
			if !f.repositoryMissing {
				repos = append(repos, map[string]any{"id": 1, "full_name": "owner/repo"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(repos), "repositories": repos})
			return
		}
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
		if f.failWrites {
			w.WriteHeader(502)
			return
		}
		var payload map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&payload)
		// Add and remove endpoints change one entry and keep the rest.
		if _, entry, ok := strings.Cut(r.URL.Path, "/issues/1/"); ok && (strings.HasPrefix(entry, "labels") || entry == "assignees") {
			field := &f.assignees
			var values []string
			if strings.HasPrefix(entry, "labels") {
				field = &f.labels
				_ = json.Unmarshal(payload["labels"], &values)
				if name, found := strings.CutPrefix(entry, "labels/"); found {
					values = []string{name}
				}
			} else {
				_ = json.Unmarshal(payload["assignees"], &values)
			}
			for _, v := range values {
				*field = slices.DeleteFunc(*field, func(e string) bool { return e == v })
				if r.Method == http.MethodPost {
					*field = append(*field, v)
				}
			}
			fmt.Fprint(w, `{}`)
			return
		}
		for key, target := range map[string]*string{"title": &f.title, "body": &f.body} {
			if v := payload[key]; v != nil {
				_ = json.Unmarshal(v, target)
			}
		}
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
		f.lastFieldsQuery = q
		owner := "alice"
		if f.oauthApp != "" {
			owner = "owner"
		}
		kind := "Organization"
		if f.personalProject {
			kind = "User"
		}
		nodes := []any{}
		if f.iterationFieldID != "" {
			nodes = append(nodes, map[string]any{"id": f.iterationFieldID, "name": "Sprint", "configuration": map[string]any{"iterations": f.iterations, "completedIterations": f.completedIterations}})
		}
		if f.secondIterationFieldID != "" {
			nodes = append(nodes, map[string]any{"id": f.secondIterationFieldID, "name": "Sprint2", "configuration": map[string]any{"iterations": []any{}, "completedIterations": []any{}}})
		}
		nodes = append(nodes, map[string]any{"id": "PVTSSF_status", "name": "Status", "options": []any{map[string]string{"id": "todo", "name": "Todo"}, map[string]string{"id": "active", "name": "Active"}}})
		emit(map[string]any{"node": map[string]any{"id": "PVT_test", "number": 9, "viewerCanUpdate": !f.readOnly, "owner": map[string]string{"login": owner, "__typename": kind}, "fields": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}}})
	case strings.Contains(q, "items(first:"):
		f.projectPages++
		rows := []any{}
		next := v["after"] == nil
		if next {
			row := map[string]any{"id": "PVTI_1", "updatedAt": "2026-01-01T00:00:00Z", "content": map[string]any{"__typename": "Issue", "id": "I_1", "number": 1, "repository": map[string]string{"nameWithOwner": "owner/repo"}}, "fieldValueByName": map[string]any{"optionId": f.option, "name": map[string]string{"todo": "Todo", "active": "In Progress"}[f.option], "field": map[string]string{"id": "PVTSSF_status"}}}
			if strings.Contains(q, "ProjectV2ItemFieldIterationValue") {
				row["sprint"] = map[string]any{"iterationId": f.itemIterationID}
				f.lastSprintFieldName, _ = v["sprintFieldName"].(string)
			}
			rows = append(rows, row)
		}
		owner := "alice"
		if f.oauthApp != "" {
			owner = "owner"
		}
		emit(map[string]any{"node": map[string]any{"id": "PVT_test", "number": 9, "owner": map[string]string{"login": owner}, "items": map[string]any{"nodes": rows, "pageInfo": map[string]any{"hasNextPage": next, "endCursor": "project-next"}}}})
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
	}{{"labels", []string{"bug"}}, {"assignees", []string{"alice"}}, {"title", "Renamed"}, {"status", "todo"}, {"status", "closed"}} {
		current, err = apply(change.field, change.value, current.Version)
		if err != nil {
			t.Fatalf("%s: %v", change.field, err)
		}
	}
	current, err = s.Apply(ctx, model.Mutation{Ref: current.Ref, Op: model.AppendComment, Text: "hello", ExpectedVersion: current.Version})
	if err != nil {
		t.Fatal(err)
	}
	if current.Category != model.Done || len(current.Labels) != 1 || len(current.Assignees) != 1 || current.Title != "Renamed" {
		t.Fatalf("write read-back: %+v", current)
	}
	if _, err = apply("milestone", "not supported", current.Version); !reason(err, "rejected") {
		t.Fatal(err)
	}
	s.o.Project.AllowWrites = false
	if _, err = apply("status", "active", current.Version); !reason(err, "rejected") {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 6 {
		t.Fatalf("writes=%d", f.writes)
	}
}

// Add and remove must change one entry without replacing the list, so an entry
// another writer added survives; a write whose result is lost is never resent.
func TestEntryEditsAndUnknownOutcome(t *testing.T) {
	s, f := newFixture(t)
	ctx := context.Background()
	f.labels = []string{"bug"}
	current, err := s.Read(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	edit := func(m model.Mutation) {
		t.Helper()
		m.Ref, m.ExpectedVersion = current.Ref, current.Version
		if current, err = s.Apply(ctx, m); err != nil {
			t.Fatalf("%s %s: %v", m.Op, m.Field, err)
		}
	}
	edit(model.Mutation{Op: model.Add, Field: "labels", Value: "docs"})
	f.mu.Lock()
	f.labels = append(f.labels, "concurrent")
	f.mu.Unlock()
	if current, err = s.Read(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	edit(model.Mutation{Op: model.Remove, Field: "labels", Value: "bug"})
	edit(model.Mutation{Op: model.Remove, Field: "labels", Value: "absent"})
	edit(model.Mutation{Op: model.Add, Field: "assignees", Value: "alice"})
	edit(model.Mutation{Op: model.Add, Field: "assignees", Value: "bob"})
	edit(model.Mutation{Op: model.Remove, Field: "assignees", Value: "alice"})
	edit(model.Mutation{Op: model.ReplaceSection, Section: "Description", Text: "New body"})
	if !slices.Equal(current.Labels, []string{"docs", "concurrent"}) || !slices.Equal(current.Assignees, []string{"bob"}) || current.Description != "New body" {
		t.Fatalf("entry read-back: labels=%v assignees=%v body=%q", current.Labels, current.Assignees, current.Description)
	}
	f.mu.Lock()
	writes := f.writes
	f.failWrites = true
	f.mu.Unlock()
	if writes != 6 {
		t.Fatalf("no-op remove dispatched: writes=%d", writes)
	}
	_, err = s.Apply(ctx, model.Mutation{Ref: current.Ref, ExpectedVersion: current.Version, Op: model.Set, Field: "title", Value: "Lost"})
	if !reason(err, "unknown-outcome") {
		t.Fatalf("lost write: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != writes+1 {
		t.Fatalf("lost write resent: writes=%d", f.writes-writes)
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

// Iteration states are classified once, when the catalog is fetched from
// discovery's configuration lists, never during query evaluation: the fixture
// clock below is read only by classify, and items with/without sprint demand
// must still agree on version for --if-version.
func TestSprintCatalogClassification(t *testing.T) {
	s, f := newFixture(t)
	f.mu.Lock()
	f.iterationFieldID = "PVTIF_sprint"
	f.completedIterations = []map[string]any{{"id": "I_past", "title": "Sprint 0", "startDate": "2025-12-01", "duration": 14}}
	f.iterations = []map[string]any{
		{"id": "I_current", "title": "Sprint 1", "startDate": "2026-01-01", "duration": 14},
		{"id": "I_next", "title": "Sprint 2", "startDate": "2026-01-15", "duration": 14},
	}
	f.itemIterationID = "I_current"
	f.mu.Unlock()
	s.o.Now = func() time.Time { return time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC) }

	ctx := context.Background()
	delta, err := s.Load(ctx, source.Cursor{Sprints: true})
	if err != nil {
		t.Fatal(err)
	}
	if !delta.Cursor.Sprints {
		t.Fatal("cursor should record sprint demand")
	}
	states := map[string]model.SprintState{}
	for _, sp := range delta.Sprints {
		states[sp.Key] = sp.State
	}
	if len(states) != 3 || states["I_past"] != model.SprintClosed || states["I_current"] != model.SprintActive || states["I_next"] != model.SprintFuture {
		t.Fatalf("classification: %+v", states)
	}
	item := itemByID(t, delta.Items, "1")
	if item.Sprint != "I_current" || item.Names["I_current"] != "Sprint 1" {
		t.Fatalf("item sprint: %+v", item)
	}

	without, err := s.Load(ctx, source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	withoutItem := itemByID(t, without.Items, "1")
	if withoutItem.Sprint != "" {
		t.Fatal("sprint leaked without demand")
	}
	if withoutItem.Version != item.Version {
		t.Fatal("sprint demand changed item version; reads must agree for --if-version")
	}
}

func itemByID(t *testing.T, items []model.Item, id string) model.Item {
	t.Helper()
	for _, item := range items {
		if item.Ref.ID == id {
			return item
		}
	}
	t.Fatalf("no item with id %q in %+v", id, items)
	return model.Item{}
}

// A break between iterations - none covering the clock - leaves every entry
// future rather than inventing a past state: GitHub's own iterations list
// holds only current and upcoming entries, never elapsed ones.
func TestSprintCatalogBreakLeavesIterationsFuture(t *testing.T) {
	s, f := newFixture(t)
	f.mu.Lock()
	f.iterationFieldID = "PVTIF_sprint"
	f.iterations = []map[string]any{
		{"id": "I_a", "title": "Sprint A", "startDate": "2026-01-01", "duration": 10},
		{"id": "I_b", "title": "Sprint B", "startDate": "2026-01-20", "duration": 10},
	}
	f.mu.Unlock()
	s.o.Now = func() time.Time { return time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC) }

	delta, err := s.Load(context.Background(), source.Cursor{Sprints: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Sprints) != 2 {
		t.Fatalf("catalog: %+v", delta.Sprints)
	}
	for _, sp := range delta.Sprints {
		if sp.State != model.SprintFuture {
			t.Fatalf("expected future during a break, got %s=%s", sp.Key, sp.State)
		}
	}
}

// A project with no iteration field at all is not sprint-capable: Load must
// succeed with an empty catalog, not error.
func TestSprintCatalogAbsentIterationField(t *testing.T) {
	s, _ := newFixture(t)
	delta, err := s.Load(context.Background(), source.Cursor{Sprints: true})
	if err != nil {
		t.Fatal(err)
	}
	if delta.Sprints != nil || itemByID(t, delta.Items, "1").Sprint != "" {
		t.Fatalf("expected no catalog: %+v", delta)
	}
}

// A sprint-free load must make no iteration-shaped request.
func TestSprintDemandGating(t *testing.T) {
	s, f := newFixture(t)
	f.mu.Lock()
	f.iterationFieldID = "PVTIF_sprint"
	f.iterations = []map[string]any{{"id": "I_current", "title": "Sprint 1", "startDate": "2026-01-01", "duration": 14}}
	f.mu.Unlock()
	s.o.Now = func() time.Time { return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) }

	delta, err := s.Load(context.Background(), source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(f.lastFieldsQuery, "ProjectV2IterationField") {
		t.Fatal("iteration field requested without sprint demand")
	}
	if delta.Sprints != nil || itemByID(t, delta.Items, "1").Sprint != "" {
		t.Fatalf("sprints leaked without demand: %+v", delta)
	}
}

// Several Projects v2 iteration fields with no configured choice is a
// refusal, not a guess.
func TestSprintMultipleIterationFieldsRefused(t *testing.T) {
	s, f := newFixture(t)
	f.mu.Lock()
	f.iterationFieldID = "PVTIF_sprint"
	f.secondIterationFieldID = "PVTIF_sprint2"
	f.iterations = []map[string]any{{"id": "I_current", "title": "Sprint 1", "startDate": "2026-01-01", "duration": 14}}
	f.mu.Unlock()
	if _, err := s.Load(context.Background(), source.Cursor{Sprints: true}); !reason(err, "rejected") {
		t.Fatalf("expected refusal for ambiguous iteration field: %v", err)
	}
}

// A configured iterationFieldId selects among several candidates instead of
// guessing.
func TestSprintConfiguredIterationField(t *testing.T) {
	s, f := newFixture(t)
	s.o.Project.IterationFieldID = "PVTIF_sprint2"
	f.mu.Lock()
	f.iterationFieldID = "PVTIF_sprint"
	f.secondIterationFieldID = "PVTIF_sprint2"
	f.iterations = []map[string]any{{"id": "I_wrong", "title": "Wrong", "startDate": "2026-01-01", "duration": 14}}
	f.mu.Unlock()

	delta, err := s.Load(context.Background(), source.Cursor{Sprints: true})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastSprintFieldName != "Sprint2" {
		t.Fatalf("expected the configured field's name, got %q", f.lastSprintFieldName)
	}
	if len(delta.Sprints) != 0 {
		t.Fatalf("expected the configured (second, empty) field's catalog, not the other field's: %+v", delta.Sprints)
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
