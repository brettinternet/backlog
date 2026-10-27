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
	"strconv"
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
	option, state, stateReason, title, body         string
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
	// issueCount sets the repository size (default 2); updated and itemUpdated
	// override the fixed timestamps of issues and of issue 1's project item.
	issueCount  int
	updated     map[int]time.Time
	itemUpdated time.Time
	requests    int
	// mutations records GraphQL follow-up and delete mutations by name with
	// their variables; failMutation fails the named one after dispatch.
	mutations             []string
	mutationVars          []map[string]any
	failMutation, created string
	canDelete             bool
	// missing is an issue number the detail query reports as NOT_FOUND.
	missing int
}

func newFixture(t *testing.T, configure ...func(*Options)) (*Source, *fixture) {
	t.Helper()
	testkit.Isolate(t)
	t.Setenv("BL_GITHUB_TEST_TOKEN", "fake-secret")
	f := &fixture{option: "todo", state: "OPEN", stateReason: "COMPLETED", title: "Issue", body: "Body", issueCount: 2, updated: map[int]time.Time{}, itemUpdated: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
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
	o := Options{ID: "gh", Repo: "owner/repo", Account: project.AccountConfig{Host: "github.com", User: "alice", Env: "BL_GITHUB_TEST_TOKEN"}, Project: &project.GitHubProjectConfig{ID: "PVT_test", Owner: "alice", Number: 9, FieldID: "PVTSSF_status", Options: map[string]string{"todo": "todo", "active": "active"}, AllowWrites: true}, HTTP: client, Scheduler: remote.NewScheduler(), Secrets: secrets, Credentials: &remote.Credentials{Secrets: secrets}}
	for _, c := range configure {
		c(&o)
	}
	s, err := New(o)
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
	projectItems := []any{}
	if n == 1 {
		item := f.projectItem()
		item["project"] = map[string]string{"id": "PVT_test"}
		projectItems = append(projectItems, item)
	}
	return map[string]any{"id": fmt.Sprint("I_", n), "number": n, "title": f.title, "body": f.body, "state": f.state, "stateReason": f.stateReason, "updatedAt": f.issueUpdated(n), "projectItems": nodes(projectItems), "repository": map[string]string{"nameWithOwner": "owner/repo"}, "labels": nodes(labels), "assignees": nodes(assignees), "blockedBy": nodes([]any{map[string]any{"id": "I_2", "number": 2, "repository": map[string]string{"nameWithOwner": "owner/repo"}}}), "subIssues": nodes([]any{}), "parent": map[string]any{"id": "I_2", "number": 2, "repository": map[string]string{"nameWithOwner": "owner/repo"}}}
}
func (f *fixture) issueUpdated(n int) time.Time {
	if t, ok := f.updated[n]; ok {
		return t
	}
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

// projectItem is issue 1's entry in the bound project.
func (f *fixture) projectItem() map[string]any {
	return map[string]any{"id": "PVTI_1", "updatedAt": f.itemUpdated, "content": map[string]any{"__typename": "Issue", "id": "I_1", "number": 1, "repository": map[string]string{"nameWithOwner": "owner/repo"}}, "fieldValueByName": map[string]any{"optionId": f.option, "name": map[string]string{"todo": "Todo", "active": "In Progress"}[f.option], "field": map[string]string{"id": "PVTSSF_status"}}, "sprint": map[string]any{"iterationId": f.itemIterationID}}
}
func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
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
		if r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues" {
			raw, _ := json.Marshal(payload)
			f.created = string(raw)
			fmt.Fprint(w, `{"number":3,"node_id":"I_3"}`)
			return
		}
		if v := payload["state_reason"]; v != nil {
			var reason string
			_ = json.Unmarshal(v, &reason)
			f.stateReason = strings.ToUpper(reason)
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
	for _, name := range []string{"addSubIssue", "addBlockedBy", "addProjectV2ItemById", "updateProjectV2ItemFieldValue", "deleteIssue"} {
		if strings.Contains(q, name+"(") {
			f.mutations = append(f.mutations, name)
			f.mutationVars = append(f.mutationVars, v)
			if name == f.failMutation {
				w.WriteHeader(502)
				return
			}
		}
	}
	switch {
	case strings.Contains(q, "addSubIssue"):
		emit(map[string]any{"addSubIssue": map[string]any{"issue": map[string]any{"id": v["parent"]}, "subIssue": map[string]any{"id": v["child"]}}})
	case strings.Contains(q, "addBlockedBy"):
		emit(map[string]any{"addBlockedBy": map[string]any{"issue": map[string]any{"id": v["id"]}, "blockingIssue": map[string]any{"id": v["blocker"]}}})
	case strings.Contains(q, "addProjectV2ItemById"):
		emit(map[string]any{"addProjectV2ItemById": map[string]any{"item": map[string]any{"id": "PVTI_3"}}})
	case strings.Contains(q, "deleteIssue"):
		emit(map[string]any{"deleteIssue": map[string]any{"repository": map[string]any{"id": "R_test"}}})
	case strings.Contains(q, "viewerCanDelete"):
		n := int(v["number"].(float64))
		emit(map[string]any{"repository": map[string]any{"id": "R_test", "issue": map[string]any{"id": fmt.Sprint("I_", n), "number": n, "viewerCanDelete": f.canDelete}}})
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
		emit(map[string]any{"updateProjectV2ItemFieldValue": map[string]any{"projectV2Item": map[string]any{"id": v["itemID"]}}})
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
	case strings.Contains(q, "query:$filter"):
		// Change-window project items: the filter is date-granular.
		since, _ := time.Parse("updated:>=2006-01-02", v["filter"].(string))
		rows := []any{}
		if !f.itemUpdated.Before(since) {
			rows = append(rows, f.projectItem())
		}
		emit(map[string]any{"node": map[string]any{"id": "PVT_test", "number": 9, "owner": map[string]string{"login": "alice"}, "items": map[string]any{"nodes": rows, "pageInfo": map[string]any{"hasNextPage": false}}}})
	case strings.Contains(q, "items(first:"):
		f.projectPages++
		rows := []any{}
		next := v["after"] == nil
		if next {
			row := f.projectItem()
			if strings.Contains(q, "ProjectV2ItemFieldIterationValue") {
				f.lastSprintFieldName, _ = v["sprintFieldName"].(string)
			} else {
				delete(row, "sprint")
			}
			rows = append(rows, row)
		}
		owner := "alice"
		if f.oauthApp != "" {
			owner = "owner"
		}
		emit(map[string]any{"node": map[string]any{"id": "PVT_test", "number": 9, "owner": map[string]string{"login": owner}, "items": map[string]any{"nodes": rows, "pageInfo": map[string]any{"hasNextPage": next, "endCursor": "project-next"}}}})
	case strings.Contains(q, "filterBy:{since:$since}"):
		f.lists++
		since, _ := time.Parse(time.RFC3339, v["since"].(string))
		rows := []any{}
		for n := 1; n <= f.issueCount; n++ {
			if !f.issueUpdated(n).Before(since) {
				rows = append(rows, f.row(n))
			}
		}
		emit(map[string]any{"repository": map[string]any{"id": "R_test", "nameWithOwner": "owner/repo", "issues": map[string]any{"nodes": rows, "pageInfo": map[string]any{"hasNextPage": false}}}})
	case strings.Contains(q, "issues(first:"):
		// One issue per page, so reconciliation cost grows with the repository.
		f.lists++
		n := 1
		if after, ok := v["after"].(string); ok {
			n, _ = strconv.Atoi(strings.TrimPrefix(after, "issue-"))
			n++
		}
		emit(map[string]any{"repository": map[string]any{"id": "R_test", "nameWithOwner": "owner/repo", "issues": map[string]any{"nodes": []any{f.row(n)}, "pageInfo": map[string]any{"hasNextPage": n < f.issueCount, "endCursor": fmt.Sprint("issue-", n)}}}})
	case strings.Contains(q, "i0:repository("):
		data := map[string]any{}
		for i := 0; v[fmt.Sprint("n", i)] != nil; i++ {
			data[fmt.Sprint("i", i)] = map[string]any{"id": "R_test", "nameWithOwner": "owner/repo", "issue": f.row(int(v[fmt.Sprint("n", i)].(float64)))}
		}
		emit(data)
	case strings.Contains(q, "issue(number:"):
		n := int(v["number"].(float64))
		if n == f.missing {
			fmt.Fprint(w, `{"data":{"repository":{"id":"R_test","nameWithOwner":"owner/repo","issue":null}},"errors":[{"type":"NOT_FOUND","path":["repository","issue"],"message":"Could not resolve to an Issue"}]}`)
			return
		}
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
	// An open issue outside the project, or with an unmapped option, is never ready.
	if second.Items[1].Status != "unknown" || second.Items[1].Category != model.Backlog {
		t.Fatalf("issue outside project: %+v", second.Items[1])
	}
	f.mu.Lock()
	if f.notModified != 1 || f.lists != 4 || f.projectPages != 4 {
		t.Fatalf("pagination/304: %+v", f)
	}
	f.option = "unmapped"
	f.mu.Unlock()
	unmapped, err := s.Load(ctx, source.Cursor{})
	if err != nil || unmapped.Items[0].Status != "unknown" || unmapped.Items[0].Category != model.Backlog {
		t.Fatalf("unmapped option: %+v, %v", unmapped.Items, err)
	}
	f.mu.Lock()
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

// A warm change window costs the same requests whatever the repository size,
// yet sees issue edits, new issues and project-only option changes. It never
// drops unseen rows, and reconciliation still runs hourly and on request.
func TestChangeWindowSync(t *testing.T) {
	ctx := context.Background()
	load := func(s *Source, cursor source.Cursor, incremental bool, count int) source.Delta {
		t.Helper()
		delta, err := s.Load(ctx, cursor)
		if err != nil || delta.Incremental != incremental || delta.RetainUnseen != incremental || delta.Coverage != source.Complete || len(delta.Items) != count {
			t.Fatalf("load (incremental %v): %+v, %v", incremental, delta, err)
		}
		return delta
	}
	warm := map[int]int{}
	for _, count := range []int{2, 40} {
		s, f := newFixture(t)
		now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
		s.o.Now = func() time.Time { return now }
		f.issueCount = count
		full := load(s, source.Cursor{}, false, count)
		now = now.Add(time.Minute)
		f.mu.Lock()
		f.requests = 0
		f.mu.Unlock()
		empty := load(s, full.Cursor, true, 0)
		if merged := source.Merge(full, empty); len(merged.Items) != count || len(merged.Stale) != 0 {
			t.Fatalf("change window dropped or staled rows: %+v", merged)
		}
		f.mu.Lock()
		warm[count] = f.requests
		f.mu.Unlock()
	}
	if warm[2] != warm[40] {
		t.Fatalf("warm requests grew with issue count: %v", warm)
	}

	s, f := newFixture(t)
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	s.o.Now = func() time.Time { return now }
	cursor := load(s, source.Cursor{}, false, 2).Cursor
	// A project option edit bumps only the project item.
	now = now.Add(time.Minute)
	f.mu.Lock()
	f.option, f.itemUpdated = "active", now.Add(-10*time.Second)
	f.mu.Unlock()
	now = now.Add(time.Minute)
	delta := load(s, cursor, true, 1)
	if item := delta.Items[0]; item.Ref.ID != "1" || item.Category != model.Active || item.Fields["projectOptionID"] != "active" {
		t.Fatalf("project option change missed: %+v", item)
	}
	// The overlap rereads it once more; the window after that starts past it.
	now = now.Add(5 * time.Minute)
	delta = load(s, delta.Cursor, true, 1)
	// Edited, closed, relabelled and new issues appear by their own updatedAt.
	now = now.Add(5 * time.Minute)
	f.mu.Lock()
	f.title, f.state, f.labels, f.issueCount = "Edited", "CLOSED", []string{"bug"}, 3
	f.updated[2], f.updated[3] = now.Add(-10*time.Second), now.Add(-5*time.Second)
	f.mu.Unlock()
	now = now.Add(time.Minute)
	delta = load(s, delta.Cursor, true, 2)
	for i, item := range delta.Items {
		if item.Ref.ID != strconv.Itoa(i+2) || item.Title != "Edited" || item.Category != model.Done || !slices.Equal(item.Labels, []string{"bug"}) {
			t.Fatalf("issue change missed: %+v", item)
		}
	}
	now = now.Add(time.Hour)
	delta = load(s, delta.Cursor, false, 3)
	delta.Cursor.Reconcile = true
	load(s, delta.Cursor, false, 3)
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
	// The TUI offers create from capabilities, so a read-only repository hides it.
	if s.Info().Capabilities.Allows(model.Create) {
		t.Fatal("read-only repository still offers create")
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

// Label statuses fail closed: several or no mapped labels are never ready, and
// a status write swaps only mapped labels in one mutation.
func TestLabelStatuses(t *testing.T) {
	s, f := newFixture(t, func(o *Options) {
		o.Project, o.StatusLabels = nil, map[string]string{"ready": "todo", "in-progress": "active"}
	})
	ctx := context.Background()
	f.labels = []string{"Ready", "bug"}
	current, err := s.Read(ctx, "1")
	if err != nil || current.Status != "ready" || current.Category != model.Todo || !slices.Equal(current.Labels, []string{"Ready", "bug"}) {
		t.Fatalf("mapped label: %+v %v", current, err)
	}
	apply := func(value string) (model.Item, error) {
		return s.Apply(ctx, model.Mutation{Ref: current.Ref, Op: model.Set, Field: "status", Value: value, ExpectedVersion: current.Version})
	}
	if current, err = apply("in-progress"); err != nil || current.Status != "in-progress" || current.Category != model.Active || !slices.Equal(f.labels, []string{"bug", "in-progress"}) {
		t.Fatalf("label status write: %+v %v %v", current, err, f.labels)
	}
	if current, err = apply("unknown"); err != nil || current.Status != "unknown" || current.Category != model.Backlog || !slices.Equal(f.labels, []string{"bug"}) {
		t.Fatalf("unknown write: %+v %v %v", current, err, f.labels)
	}
	f.labels = []string{"ready", "In-Progress"}
	delta, err := s.Load(ctx, source.Cursor{})
	if err != nil || delta.Items[0].Status != "unknown" || delta.Items[0].Category != model.Backlog || len(delta.Findings) != 2 || !strings.Contains(delta.Findings[0].Message, "(ready, in-progress)") {
		t.Fatalf("several mapped labels: %+v %v", delta, err)
	}
	f.state = "CLOSED"
	if current, err = s.Read(ctx, "1"); err != nil || current.Status != "closed" || current.Category != model.Done {
		t.Fatalf("closed issue: %+v %v", current, err)
	}
	if _, err = apply("ready"); !reason(err, "rejected") || f.writes != 2 {
		t.Fatalf("closed label write: %v writes=%d", err, f.writes)
	}
	if current, err = apply("open"); err != nil || current.Status != "unknown" || f.state != "OPEN" {
		t.Fatalf("reopen: %+v %v", current, err)
	}
	// A create status sends its label in the issue POST, replacing other mapped labels.
	if _, err = s.Apply(ctx, model.Mutation{Ref: model.Ref{Source: "gh"}, Op: model.Create, Item: &model.Item{Title: "New", Status: "In-Progress", Labels: []string{"Ready", "bug"}}}); err != nil || f.created != `{"body":"","labels":["in-progress","bug"],"title":"New"}` {
		t.Fatalf("create with label status: %v %s", err, f.created)
	}
	if _, err = s.Apply(ctx, model.Mutation{Ref: model.Ref{Source: "gh"}, Op: model.Create, Item: &model.Item{Title: "New", Status: "active"}}); !reason(err, "rejected") {
		t.Fatalf("create with unmapped status: %v", err)
	}
}

// Unqualified shorthand asks every source; a native ID or a missing issue
// number must read as absent, or one GitHub source breaks every bl show BL-1.
func TestReadAbsentIssue(t *testing.T) {
	s, f := newFixture(t)
	f.missing = 404
	ctx := context.Background()
	if _, err := s.Read(ctx, "BL-1"); !reason(err, "invalid-id") {
		t.Fatalf("native ID: %v", err)
	}
	if _, err := s.Read(ctx, "404"); !reason(err, "not-found") {
		t.Fatalf("missing issue: %v", err)
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

// A create sends one issue POST, then each follow-up once. A failure after
// dispatch names the created issue and is never retried (writes count the
// issue POST and the project field update); undo deletes only an
// unchanged issue the viewer can delete and otherwise closes it as not planned.
func TestCreateFollowUpsAndUndo(t *testing.T) {
	s, f := newFixture(t)
	ctx := context.Background()
	two := model.Ref{Source: "gh", ID: "2"}
	template := func() *model.Item {
		return &model.Item{Title: "New", Description: "Text", Labels: []string{"bug"}, Assignees: []string{"alice"}, Status: "active", Parent: &two, BlockedBy: []model.Ref{two}}
	}
	created, err := s.Apply(ctx, model.Mutation{Ref: model.Ref{Source: "gh"}, Op: model.Create, Item: template()})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if created.Ref.ID != "3" || created.Version == "" || f.created != `{"assignees":["alice"],"body":"Text","labels":["bug"],"title":"New"}` || f.writes != 2 {
		t.Fatalf("create: ref=%v payload=%s writes=%d", created.Ref, f.created, f.writes)
	}
	if !slices.Equal(f.mutations, []string{"addSubIssue", "addBlockedBy", "addProjectV2ItemById", "updateProjectV2ItemFieldValue"}) || f.mutationVars[0]["parent"] != "I_2" || f.mutationVars[1]["blocker"] != "I_2" || f.mutationVars[2]["contentID"] != "I_3" || f.mutationVars[3]["optionID"] != "active" || f.mutationVars[3]["itemID"] != "PVTI_3" {
		t.Fatalf("follow-ups: %v %v", f.mutations, f.mutationVars)
	}
	f.mutations, f.writes, f.failMutation = nil, 0, "addBlockedBy"
	f.mu.Unlock()
	_, err = s.Apply(ctx, model.Mutation{Ref: model.Ref{Source: "gh"}, Op: model.Create, Item: template()})
	var refusal *source.Error
	if !errors.As(err, &refusal) || refusal.Reason != "unknown-outcome" || refusal.Created == nil || *refusal.Created != (model.Ref{Source: "gh", ID: "3"}) {
		t.Fatalf("failed follow-up: %v", err)
	}
	f.mu.Lock()
	if f.writes != 1 || !slices.Equal(f.mutations, []string{"addSubIssue", "addBlockedBy"}) {
		t.Fatalf("failed follow-up resent or continued: writes=%d %v", f.writes, f.mutations)
	}
	f.writes, f.failMutation, f.failWrites = 0, "", true
	f.mu.Unlock()
	_, err = s.Apply(ctx, model.Mutation{Ref: model.Ref{Source: "gh"}, Op: model.Create, Item: template()})
	if !errors.As(err, &refusal) || refusal.Reason != "unknown-outcome" || refusal.Created != nil {
		t.Fatalf("lost create: %v", err)
	}
	f.mu.Lock()
	if f.writes != 1 {
		t.Fatalf("lost create resent: writes=%d", f.writes)
	}
	f.writes, f.failWrites = 0, false
	f.mu.Unlock()
	for _, item := range []model.Item{{Title: "x", Priority: model.High}, {Title: "x", Milestone: "m-1"}, {Title: "x", Status: "closed"}, {Title: "x", Parent: &model.Ref{Source: "tasks", ID: "BL-1"}}} {
		if _, err := s.Apply(ctx, model.Mutation{Ref: model.Ref{Source: "gh"}, Op: model.Create, Item: &item}); !reason(err, "rejected") {
			t.Fatalf("unsupported %+v: %v", item, err)
		}
	}

	// The second create's version was never recorded: undo closes it.
	f.mu.Lock()
	f.canDelete, f.mutations = true, nil
	f.mu.Unlock()
	undo := func(version string) (model.Item, error) {
		return s.Apply(ctx, model.Mutation{Ref: created.Ref, Op: model.Delete, ExpectedVersion: version})
	}
	s.created = map[string]string{}
	kept, err := undo(created.Version)
	if err != nil || kept.Version == "" || kept.Category != model.Canceled || f.mutations != nil {
		t.Fatalf("undo of an unrecorded create: %+v %v %v", kept, err, f.mutations)
	}
	f.mu.Lock()
	f.state, f.stateReason = "OPEN", "COMPLETED"
	f.mu.Unlock()
	s.created = map[string]string{"3": created.Version}
	f.canDelete = false
	if kept, err = undo(created.Version); err != nil || kept.Category != model.Canceled || f.mutations != nil {
		t.Fatalf("undo without delete permission: %+v %v %v", kept, err, f.mutations)
	}
	f.mu.Lock()
	f.state, f.stateReason, f.canDelete = "OPEN", "COMPLETED", true
	f.mu.Unlock()
	s.created = map[string]string{"3": created.Version}
	deleted, err := undo(created.Version)
	if err != nil || deleted.Version != "" || deleted.Ref != created.Ref || !slices.Equal(f.mutations, []string{"deleteIssue"}) || f.mutationVars[len(f.mutationVars)-1]["id"] != "I_3" {
		t.Fatalf("undo delete: %+v %v %v", deleted, err, f.mutations)
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
