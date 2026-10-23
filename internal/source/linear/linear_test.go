package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
	"github.com/brettinternet/backlog/internal/testkit"
	"github.com/brettinternet/backlog/internal/worklease"
)

const orgID = "10000000-0000-0000-0000-000000000001"
const teamID = "20000000-0000-0000-0000-000000000001"
const userID = "30000000-0000-0000-0000-000000000001"
const stateID = "40000000-0000-0000-0000-000000000001"
const activeID = "40000000-0000-0000-0000-000000000002"
const labelID = "50000000-0000-0000-0000-000000000001"

func issueID(n int) string { return fmt.Sprintf("60000000-0000-0000-0000-%012d", n) }

type fixture struct {
	t                                                                              *testing.T
	moving, block, active, wrongOrg, brokenPage, missingRelations, failWrite, bulk bool
	writes, full, overlap, relations                                               int
	state, assignee                                                                string
	priority                                                                       int
	labels                                                                         []string
	comments                                                                       []any
}
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureSource(t *testing.T) (*Source, *fixture) {
	t.Helper()
	testkit.Isolate(t)
	t.Setenv("BL_LINEAR_TEST_TOKEN", "linear-canary-secret")
	f := &fixture{t: t, active: true, state: stateID, comments: []any{}}
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
	s, err := New(Options{ID: "lin", Organization: orgID, Team: teamID, Account: project.AccountConfig{User: userID, Env: "BL_LINEAR_TEST_TOKEN"}, AllowWrites: true, HTTP: client, Scheduler: remote.NewScheduler(), Secrets: secrets, Credentials: &remote.Credentials{Secrets: secrets}, Now: func() time.Time { return time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}
func conn(nodes any, next string) map[string]any {
	return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": next != "", "endCursor": next}}
}
func entityRow(n int) map[string]any {
	return map[string]any{"id": issueID(n), "identifier": fmt.Sprintf("TEST-%d", n), "team": map[string]string{"id": teamID}}
}
func (f *fixture) row(n int) map[string]any {
	r := entityRow(n)
	r["title"] = "Synthetic"
	r["description"] = "Body"
	r["updatedAt"] = "2026-10-22T00:00:00Z"
	r["priority"] = f.priority
	r["state"] = map[string]string{"id": f.state, "name": "Todo", "type": "unstarted"}
	if f.state == activeID {
		r["state"] = map[string]string{"id": activeID, "name": "Started", "type": "started"}
	}
	if f.assignee != "" {
		r["assignee"] = map[string]string{"id": f.assignee}
	}
	labels := []any{}
	for _, name := range f.labels {
		labels = append(labels, map[string]string{"id": labelID, "name": name})
	}
	r["labels"] = conn(labels, "")
	return r
}
func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "linear-canary-secret" {
		f.t.Error("Linear personal key must use raw authorization")
		w.WriteHeader(401)
		return
	}
	var req struct {
		Query     string
		Variables map[string]any
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		f.t.Error("bad request")
		return
	}
	q, v := req.Query, req.Variables
	emit := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": data}) }
	if v["count"] != nil && v["count"] != float64(250) {
		f.t.Errorf("page cap not 250: %v", v["count"])
	}
	switch q {
	case identityQuery:
		org := orgID
		if f.wrongOrg {
			org = teamID
		}
		emit(map[string]any{"viewer": map[string]any{"id": userID, "active": f.active, "organization": map[string]string{"id": org}}, "team": map[string]string{"id": teamID}})
	case listQuery, incrementalQuery:
		rows := []any{}
		next := ""
		if q == listQuery {
			f.full++
			if v["after"] == nil {
				rows = []any{f.row(1)}
				next = "page-2"
			} else {
				rows = []any{f.row(3)}
				if !f.moving {
					rows = append(rows, f.row(2))
				}
			}
		} else {
			f.overlap++
			if v["since"] != "2026-09-23T00:00:00Z" {
				f.t.Errorf("lost tagged overlap: %v", v["since"])
			}
			if f.moving {
				rows = []any{f.row(2)}
			}
		}
		if f.bulk && q == listQuery {
			rows = []any{}
			if v["after"] == nil {
				for i := 1; i <= 250; i++ {
					rows = append(rows, f.row(i))
				}
				next = "page-2"
			} else {
				rows = append(rows, f.row(251))
				next = ""
			}
		}
		c := conn(rows, next)
		if f.brokenPage {
			delete(c, "pageInfo")
		}
		emit(map[string]any{"team": map[string]any{"id": teamID, "issues": c}})
	case detailQuery:
		id, _ := v["id"].(string)
		n, _ := strconv.Atoi(strings.TrimPrefix(id, "TEST-"))
		if n == 0 && len(id) == 36 {
			n, _ = strconv.Atoi(id[24:])
		}
		emit(map[string]any{"issue": f.row(n)})
	case relationsQuery, inverseQuery:
		f.relations++
		nodes := []any{}
		key := "relations"
		if q == inverseQuery {
			key = "inverseRelations"
		}
		if f.block && v["id"] == issueID(1) {
			relatedKey := "relatedIssue"
			if q == inverseQuery {
				relatedKey = "issue"
			}
			nodes = append(nodes, map[string]any{"type": "blocks", relatedKey: entityRow(2)})
		}
		connection := conn(nodes, "")
		if f.missingRelations {
			delete(connection, "nodes")
		}
		emit(map[string]any{"issue": map[string]any{"id": v["id"], key: connection}})
	case commentsQuery:
		emit(map[string]any{"issue": map[string]any{"id": v["id"], "comments": conn(f.comments, "")}})
	case statesQuery:
		emit(map[string]any{"team": map[string]any{"id": teamID, "states": conn([]any{map[string]string{"id": stateID, "name": "Todo", "type": "unstarted"}, map[string]string{"id": activeID, "name": "Started", "type": "started"}}, "")}})
	case labelsQuery:
		emit(map[string]any{"issueLabels": conn([]any{map[string]string{"id": labelID, "name": "bug"}}, "")})
	case updateMutation:
		f.writes++
		if f.failWrite {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		input := v["input"].(map[string]any)
		if len(input) != 1 {
			f.t.Error("write changed multiple fields")
		}
		for k, value := range input {
			switch k {
			case "stateId":
				f.state = value.(string)
			case "priority":
				f.priority = int(value.(float64))
			case "assigneeId":
				f.assignee = ""
				if value != nil {
					f.assignee = value.(string)
				}
			case "labelIds":
				f.labels = nil
				for range value.([]any) {
					f.labels = append(f.labels, "bug")
				}
			default:
				f.t.Error("unknown write field")
			}
		}
		emit(map[string]any{"issueUpdate": map[string]any{"success": true, "issue": map[string]any{"id": v["id"]}}})
	case commentMutation:
		f.writes++
		id := "70000000-0000-0000-0000-000000000001"
		f.comments = append(f.comments, map[string]any{"id": id, "body": v["body"], "user": map[string]string{"id": userID}})
		emit(map[string]any{"commentCreate": map[string]any{"success": true, "comment": map[string]string{"id": id}}})
	default:
		f.t.Errorf("unexpected query: %s", q)
		w.WriteHeader(400)
	}
}

// Catches requesting Linear's rejected 251+ page size or silently truncating
// a team with more issues than one provider page.
func TestPageCap(t *testing.T) {
	s, f := fixtureSource(t)
	f.bulk = true
	delta, err := s.Load(context.Background(), source.Cursor{})
	if err != nil || len(delta.Items) != 251 || f.full != 2 {
		t.Fatalf("capped enumeration lost rows: %v rows=%d pages=%d", err, len(delta.Items), f.full)
	}
}

// Realistic failure: updatedAt ordering moves an unseen row ahead of page two;
// an overlapping scan must recover that exact omitted issue, without deletions.
func TestMovingCursorAndRelationReconciliation(t *testing.T) {
	s, f := fixtureSource(t)
	f.moving = true
	wrapped := remote.New(s, cache.New(t.TempDir(), "test", "lin", "scope"), s.o.Secrets)
	d, err := wrapped.Load(context.Background(), source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, i := range d.Items {
		ids[i.Ref.ID] = true
	}
	if len(ids) != 3 || !ids["TEST-2"] || f.overlap != 1 || d.Coverage != source.Partial {
		t.Fatalf("moving row lost or false absence proof: ids=%v coverage=%s overlap=%d", ids, d.Coverage, f.overlap)
	}
	before := d.Items[0]
	f.moving = false
	f.block = true
	relationReads := f.relations
	d, err = wrapped.Load(context.Background(), d.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	var after model.Item
	for _, i := range d.Items {
		if i.Ref == before.Ref {
			after = i
		}
	}
	if len(after.BlockedBy) != 1 || len(after.Blocks) != 1 || after.BlockedBy[0].ID != "TEST-2" || after.Updated != before.Updated || after.Version == before.Version || f.relations <= relationReads {
		t.Fatal("relation-only change not reconciled directionally")
	}
	coordinator := &worklease.Coordinator{Config: project.Config{Settings: project.Settings{Sources: []project.SourceConfig{{ID: "lin", Type: "linear", Site: orgID}}}}}
	key, err := coordinator.Resource(after)
	want, _ := worklease.Key("linear", orgID, issueID(1))
	if err != nil || key != want {
		t.Fatalf("unstable claim identity: %s %v", key, err)
	}
}

// Catches accidental dispatch on stale relation fingerprints, denied writes,
// unsupported fields, inactive accounts, or credential organization drift.
func TestVersionCapabilityAndWrites(t *testing.T) {
	s, f := fixtureSource(t)
	ctx := context.Background()
	item, err := s.Read(ctx, "TEST-1")
	if err != nil {
		t.Fatal(err)
	}
	refuse := func(m model.Mutation, reason string) {
		t.Helper()
		before := f.writes
		_, err := s.Apply(ctx, m)
		var e *source.Error
		if !errors.As(err, &e) || e.Reason != reason || f.writes != before {
			t.Fatalf("expected %s without dispatch: %v writes=%d", reason, err, f.writes)
		}
	}
	mutation := func(field string, value any) model.Mutation {
		return model.Mutation{Ref: item.Ref, Op: model.Set, Field: field, Value: value, ExpectedVersion: item.Version}
	}
	f.block = true
	refuse(mutation("priority", "high"), "conflict")
	f.block = false
	m := mutation("priority", "high")
	m.ExpectedVersion = ""
	refuse(m, "conflict")
	s.o.AllowWrites = false
	refuse(mutation("priority", "high"), "rejected")
	s.o.AllowWrites = true
	f.active = false
	refuse(mutation("priority", "high"), "rejected")
	f.active = true
	refuse(mutation("title", "bad"), "rejected")
	refuse(mutation("assignees", []string{userID, userID}), "rejected")
	refuse(mutation("status", issueID(9)), "rejected")
	f.wrongOrg = true
	before := f.writes
	if _, err = s.Apply(ctx, mutation("priority", "high")); err == nil || f.writes != before {
		t.Fatal("organization drift wrote")
	}
	f.wrongOrg = false
	for _, change := range []struct {
		field string
		value any
	}{{"status", activeID}, {"assignees", []string{userID}}, {"priority", "high"}, {"labels", []string{"bug"}}, {"assignees", []string{}}, {"labels", []string{}}} {
		item, err = s.Apply(ctx, mutation(change.field, change.value))
		if err != nil {
			t.Fatalf("%s: %v", change.field, err)
		}
	}
	item, err = s.Apply(ctx, model.Mutation{Ref: item.Ref, Op: model.AppendComment, Text: "progress", ExpectedVersion: item.Version})
	if err != nil || len(item.Comments) != 1 || item.Comments[0].Body != "progress" || item.Priority != model.High || item.Status != activeID || len(item.Assignees) != 0 || len(item.Labels) != 0 || f.writes != 7 {
		t.Fatalf("write roundtrip failed: %v writes=%d", err, f.writes)
	}
	f.failWrite = true
	if _, err = s.Apply(ctx, mutation("priority", "low")); err == nil || f.writes != 8 {
		t.Fatalf("uncertain write retried: %v writes=%d", err, f.writes)
	}
}
func TestIncompletePaginationDoesNotAdvance(t *testing.T) {
	s, f := fixtureSource(t)
	f.brokenPage = true
	if _, err := s.Load(context.Background(), source.Cursor{}); err == nil {
		t.Fatal("missing pageInfo accepted")
	}
	f.brokenPage = false
	f.missingRelations = true
	if _, err := s.Read(context.Background(), "TEST-1"); err == nil {
		t.Fatal("missing relation nodes treated as no prerequisites")
	}
}

// Provider errors can echo authorization; never expose their text to consumers.
func FuzzProviderJSON(f *testing.F) {
	f.Add([]byte(`{"data":{"issue":null}}`))
	f.Add([]byte(`{"errors":[{"message":"private-token"}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var out struct{ Issue *issue }
		err := decode(raw, &out)
		if err != nil && strings.Contains(err.Error(), "private-token") {
			t.Fatal("provider error leaked")
		}
	})
}
