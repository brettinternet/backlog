package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
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
	oauth                                                                          bool
	state, assignee                                                                string
	priority                                                                       int
	labels                                                                         []string
	comments                                                                       []any
	size, requests, batches                                                        int
	changed, relationOverflow                                                      bool
	blocker, trashed                                                               bool   // TEST-1 blocks TEST-2; TEST-1 trashed
	archivedAt                                                                     string // TEST-1's archivedAt when trashed, without bumping updatedAt (BL-99/BL-102)
	cycleActive, wantCycle, noCycle                                                bool
	cycleQueries                                                                   int
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
		r["assignee"] = map[string]string{"id": f.assignee, "name": "Ada Lovelace"}
	}
	labels := []any{}
	for _, name := range f.labels {
		labels = append(labels, map[string]string{"id": labelID, "name": name})
	}
	r["labels"] = conn(labels, "")
	if f.wantCycle && n == 1 && !f.noCycle {
		r["cycle"] = map[string]any{"id": cycleID, "name": nil, "number": 7}
	}
	return r
}

const cycleID = "70000000-0000-0000-0000-000000000007"

func (f *fixture) cycles() map[string]any {
	return conn([]any{map[string]any{"id": cycleID, "number": 7, "name": nil, "startsAt": "2026-10-26T00:00:00Z", "endsAt": "2026-11-09T00:00:00Z", "isActive": f.cycleActive, "isFuture": !f.cycleActive}}, "")
}
func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	authorization := "linear-canary-secret"
	if f.oauth {
		authorization = "Bearer " + authorization
	}
	if r.Header.Get("Authorization") != authorization {
		f.t.Error("Linear credential used the wrong authorization scheme")
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
	f.requests++
	q, v := req.Query, req.Variables
	f.wantCycle = strings.Contains(q, "cycle{id name number}")
	q = strings.ReplaceAll(q, " cycle{id name number}", "")
	listCycles := strings.Contains(q, cycleFields)
	if listCycles {
		f.cycleQueries++
		q = strings.Replace(strings.Replace(q, "$cyclesAfter:DateTimeOrDuration!,", "", 1), cycleFields+" ", "", 1)
	}
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
			if v["since"] != "2026-09-23T00:00:00Z" && v["since"] != "2026-09-23T01:00:00Z" && f.size == 0 {
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
		if f.size > 0 {
			rows = []any{}
			next = ""
			if q == listQuery {
				offset, _ := strconv.Atoi(fmt.Sprint(v["after"]))
				for n := offset + 1; n <= min(offset+250, f.size); n++ {
					rows = append(rows, f.row(n))
				}
				if offset+250 < f.size {
					next = strconv.Itoa(offset + 250)
				}
			} else if f.changed {
				row := f.row(1)
				row["updatedAt"] = "2026-10-23T00:01:00Z"
				rows = append(rows, row)
			} else if f.trashed {
				// Trashing never bumps updatedAt (BL-99): TEST-1 matches the window
				// only through the archivedAt arm of the OR filter, same as Linear.
				since, sinceErr := time.Parse(time.RFC3339Nano, fmt.Sprint(v["since"]))
				archived, archivedErr := time.Parse(time.RFC3339Nano, f.archivedAt)
				if sinceErr != nil || archivedErr != nil {
					f.t.Fatalf("bad since/archivedAt: since=%v (%v) archivedAt=%v (%v)", v["since"], sinceErr, f.archivedAt, archivedErr)
				}
				if !archived.Before(since) {
					rows = append(rows, f.row(1))
				}
			}
		}
		c := conn(rows, next)
		if f.brokenPage {
			delete(c, "pageInfo")
		}
		team := map[string]any{"id": teamID, "issues": c}
		if listCycles {
			team["cycles"] = f.cycles()
		}
		emit(map[string]any{"team": team})
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
	case trashedRelationsQuery:
		nodes := []any{}
		if f.blocker && v["id"] == issueID(1) {
			nodes = append(nodes, map[string]any{"type": "blocks", "relatedIssue": entityRow(2)})
		}
		emit(map[string]any{"issue": map[string]any{"id": v["id"], "relations": conn(nodes, "")}})
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
			case "cycleId":
				f.noCycle = value == nil
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
		if strings.HasPrefix(q, "query($id0:String!") {
			f.batches++
			data := map[string]any{}
			for i := 0; i < len(v); i++ {
				id := v[fmt.Sprintf("id%d", i)].(string)
				n, _ := strconv.Atoi(id[24:])
				row := f.row(n)
				if f.changed && n == 1 {
					row["updatedAt"] = "2026-10-23T00:01:00Z"
				}
				out, in := []any{}, []any{}
				if f.trashed && n == 1 {
					row["trashed"] = true
				} else if f.blocker && !f.trashed && n == 1 {
					out = append(out, map[string]any{"type": "blocks", "relatedIssue": entityRow(2)})
				} else if f.blocker && !f.trashed && n == 2 {
					in = append(in, map[string]any{"type": "blocks", "issue": entityRow(1)})
				}
				if f.block && n == 1 {
					out = append(out, map[string]any{"type": "blocks", "relatedIssue": entityRow(2)})
					in = append(in, map[string]any{"type": "blocks", "issue": entityRow(2)})
				}
				row["relations"], row["inverseRelations"] = conn(out, ""), conn(in, "")
				if f.relationOverflow && n == 1 {
					row["relations"], row["inverseRelations"] = conn([]any{}, "rel-next"), conn([]any{}, "inverse-next")
				}
				if f.missingRelations {
					delete(row, "relations")
				}
				data[fmt.Sprintf("i%d", i)] = row
			}
			emit(data)
			return
		}
		f.t.Errorf("unexpected query: %s", q)
		w.WriteHeader(400)
	}
}

// Catches requesting Linear's rejected 251+ page size or silently truncating
// a team with more issues than one provider page.
// Prevents quota exhaustion from per-issue reads on every minute, unnoticed
// relation-only edits, and change windows turning unchanged ready rows stale.
func TestRefreshBudgetAndRetainedRows(t *testing.T) {
	s, f := fixtureSource(t)
	f.size = 1000
	ctx := context.Background()
	now := s.o.Now()
	s.o.Now = func() time.Time { return now }
	store := cache.New(t.TempDir(), "budget", "lin", "scope")
	wrapped := remote.New(s, store, s.o.Secrets)
	d, err := wrapped.Load(ctx, source.Cursor{})
	if err != nil || len(d.Items) != 1000 || f.requests != 106 || len(d.Stale) != 0 {
		t.Fatalf("initial scan: rows=%d requests=%d stale=%d err=%v", len(d.Items), f.requests, len(d.Stale), err)
	}
	before := d.Items[0]
	f.block = true // updatedAt stays unchanged
	now = now.Add(time.Minute)
	requests := f.requests
	// A new adapter and wrapper prove that persisted cursors retain the schedule.
	restarted, err := New(s.o)
	if err != nil {
		t.Fatal(err)
	}
	wrapped = remote.New(restarted, store, s.o.Secrets)
	d, err = wrapped.Load(ctx, source.Cursor{})
	if err != nil || f.requests-requests != 2 || len(d.Items) != 1000 || len(d.Stale) != 0 || d.Items[0].Version != before.Version {
		t.Fatalf("unchanged window: rows=%d requests=%d stale=%d err=%v", len(d.Items), f.requests-requests, len(d.Stale), err)
	}
	f.changed = true
	now = now.Add(time.Minute)
	requests = f.requests
	d, err = wrapped.Load(ctx, source.Cursor{})
	if err != nil || f.requests-requests != 3 || len(d.Items) != 1000 || len(d.Stale) != 0 || d.Items[0].Updated == before.Updated {
		t.Fatalf("one changed issue: requests=%d stale=%d err=%v", f.requests-requests, len(d.Stale), err)
	}
	f.changed = false
	f.block = false
	now = now.Add(reconcileInterval - 2*time.Minute)
	requests = f.requests
	d, err = wrapped.Load(ctx, source.Cursor{})
	if err != nil || f.requests-requests != 106 || len(d.Items) != 1000 || len(d.Stale) != 0 || len(d.Items[0].BlockedBy) != 0 || d.Items[0].Version != before.Version {
		t.Fatalf("relation-only reconciliation: requests=%d stale=%d err=%v", f.requests-requests, len(d.Stale), err)
	}
	// A failed reconciliation must not advance its schedule or poison cached rows.
	now = now.Add(reconcileInterval)
	f.missingRelations = true
	if _, err = wrapped.Load(ctx, source.Cursor{}); err == nil {
		t.Fatal("incomplete batch committed")
	}
	f.missingRelations = false
	f.block = true
	requests = f.requests
	d, err = wrapped.Load(ctx, source.Cursor{})
	if err != nil || f.requests-requests != 106 || len(d.Stale) != 0 || len(d.Items[0].BlockedBy) != 1 || d.Items[0].Version == before.Version {
		t.Fatalf("failed reconciliation advanced cursor: requests=%d stale=%d err=%v", f.requests-requests, len(d.Stale), err)
	}
	// A relation edit is invisible to the change window. A fresh read (show,
	// detail, write preflight) refreshes the cached row, and a requested
	// reconciliation rescans everything without waiting for the hour.
	f.block = false
	now = now.Add(time.Minute)
	requests = f.requests
	if d, err = wrapped.Load(ctx, source.Cursor{}); err != nil || f.requests-requests != 2 || len(d.Items[0].BlockedBy) != 1 {
		t.Fatalf("window saw relation edit: requests=%d err=%v", f.requests-requests, err)
	}
	if fresh, err := wrapped.Read(ctx, d.Items[0].Ref.ID); err != nil || len(fresh.BlockedBy) != 0 {
		t.Fatalf("fresh read: %+v %v", fresh, err)
	}
	if d, err = wrapped.Load(ctx, source.Cursor{}); err != nil || len(d.Items) != 1000 || len(d.Stale) != 0 || len(d.Items[0].BlockedBy) != 0 {
		t.Fatalf("fresh read did not refresh its cached row: %v", err)
	}
	f.block = true
	requests = f.requests
	if d, err = wrapped.Load(ctx, source.Cursor{Reconcile: true}); err != nil || f.requests-requests != 106 || len(d.Items[0].BlockedBy) != 1 {
		t.Fatalf("requested reconciliation: requests=%d err=%v", f.requests-requests, err)
	}
	t.Log("1000 issues: unchanged refresh=2 requests; one change=3; hourly or requested reconciliation=106; no stale retained rows")
}

// BL-90: trashing a blocker bumps only the trashed issue and silently drops the
// survivor's inverse relation, so the blocked issue would stay Unknown (blocker
// missing from the default view) until the hourly reconciliation.
func TestTrashedBlockerRereadsBlockedIssue(t *testing.T) {
	s, f := fixtureSource(t)
	f.size, f.blocker = 3, true
	ctx := context.Background()
	now := s.o.Now()
	s.o.Now = func() time.Time { return now }
	wrapped := remote.New(s, cache.New(t.TempDir(), "trash", "lin", "scope"), s.o.Secrets)
	blocked := func(d source.Delta) *model.Item {
		for i := range d.Items {
			if d.Items[i].Ref.ID == "TEST-2" {
				return &d.Items[i]
			}
		}
		t.Fatal("blocked issue missing")
		return nil
	}
	d, err := wrapped.Load(ctx, source.Cursor{})
	if err != nil || len(blocked(d).BlockedBy) != 1 {
		t.Fatalf("initial blocker: %v", err)
	}
	// Trashing bumps only archivedAt, never updatedAt (BL-99/BL-90): the window
	// must surface TEST-1 through the archivedAt arm of the OR filter alone.
	f.trashed = true
	f.archivedAt = now.Format(time.RFC3339Nano)
	// The window's overlap keeps matching a trashed row for a few more minutes
	// (gte:$since against a watermark that only moves forward), then the
	// watermark passes archivedAt and it drops out instead of re-reading
	// forever. want tracks that expected match/no-match sequence.
	for minute, want := range []bool{true, true, true, false} {
		now = now.Add(time.Minute)
		requests := f.requests
		d, err = wrapped.Load(ctx, source.Cursor{})
		if err != nil {
			t.Fatal(err)
		}
		if got := f.requests-requests > 2; got != want {
			t.Fatalf("minute %d: window matched trashed row=%v want=%v (requests=%d)", minute+1, got, want, f.requests-requests)
		}
		if len(d.Stale) != 0 {
			t.Fatalf("minute %d: incremental window marked a row stale: %v", minute+1, d.Stale)
		}
		if !want {
			continue
		}
		// The cached blocker row must be flagged too, or the default view keeps it as open work.
		if item := blocked(d); len(item.BlockedBy) != 0 || len(item.Edges) != 0 || !d.Archived[model.Ref{Source: "lin", ID: "TEST-1"}] {
			t.Fatalf("minute %d: trashed blocker left blocked issue stale: %+v archived=%v", minute+1, item.BlockedBy, d.Archived)
		}
	}
}

// BL-102: the incremental filter widens to archivedAt so a trash or archive
// alone (no updatedAt bump) is reachable from the minute change window.
func TestIncrementalQueryMatchesArchivedAt(t *testing.T) {
	const want = `filter:{or:[{updatedAt:{gte:$since}},{archivedAt:{gte:$since}}]}`
	if !strings.Contains(incrementalQuery, want) {
		t.Fatalf("incrementalQuery missing archivedAt OR filter %q: %s", want, incrementalQuery)
	}
}

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
	relationReads := f.batches
	now := s.o.Now().Add(reconcileInterval)
	s.o.Now = func() time.Time { return now }
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
	if len(after.BlockedBy) != 1 || len(after.Blocks) != 1 || after.BlockedBy[0].ID != "TEST-2" || after.Updated != before.Updated || after.Version == before.Version || f.batches <= relationReads {
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
		// Display names ride alongside the stable IDs that writes, queries and @me use.
		if change.field == "assignees" && len(change.value.([]string)) == 1 && (!slices.Equal(item.Assignees, []string{userID}) || item.Names[userID] != "Ada Lovelace" || item.Status != activeID || item.Names[activeID] != "Started") {
			t.Fatalf("assignee identity: %v %v %v", item.Assignees, item.Status, item.Names)
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

// Versions exclude the cycle, so a stale observed cycle must be caught before
// dispatch even when the caller's read never asked for cycles.
func TestSprintWrites(t *testing.T) {
	s, f := fixtureSource(t)
	ctx := context.Background()
	item, err := s.Read(ctx, "TEST-1")
	if err != nil {
		t.Fatal(err)
	}
	set := func(value string, observed *string) (model.Item, error) {
		return s.Apply(ctx, model.Mutation{Ref: item.Ref, Op: model.Set, Field: "sprint", Value: value, ExpectedVersion: item.Version, ExpectedSprint: observed})
	}
	for _, bad := range []struct {
		value    string
		observed *string
		reason   string
	}{{"cycle-7", new(cycleID), "rejected"}, {"", nil, "rejected"}, {"", new(""), "conflict"}} {
		var e *source.Error
		if _, err = set(bad.value, bad.observed); !errors.As(err, &e) || e.Reason != bad.reason || f.writes != 0 {
			t.Fatalf("%q: expected %s without dispatch: %v writes=%d", bad.value, bad.reason, err, f.writes)
		}
	}
	if item, err = set("", new(cycleID)); err != nil || item.Sprint != "" || f.writes != 1 {
		t.Fatalf("remove: %q %v", item.Sprint, err)
	}
	if item, err = set(cycleID, new("")); err != nil || item.Sprint != cycleID || f.writes != 2 {
		t.Fatalf("move: %q %v", item.Sprint, err)
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
	// A blocker beyond the batched first page must still affect readiness.
	f.missingRelations = false
	f.relationOverflow, f.block = true, true
	reads := f.relations
	d, err := s.Load(context.Background(), source.Cursor{})
	if err != nil || len(d.Items) != 3 || len(d.Items[0].BlockedBy) != 1 || len(d.Items[0].Blocks) != 1 || f.relations-reads != 2 {
		t.Fatalf("batched relation overflow lost edges: %+v %v", d.Items, err)
	}
}

type scopeProvider struct{}

func (scopeProvider) ID() string                         { return "linear" }
func (scopeProvider) ReadScopes(oauth.Account) []string  { return []string{"read"} }
func (scopeProvider) WriteScopes(oauth.Account) []string { return []string{"write"} }
func (scopeProvider) Authorize(_ context.Context, _ oauth.Account, scopes []string, _ *oauth.Tokens, _ oauth.Interaction) (oauth.Tokens, error) {
	return oauth.Tokens{AccessToken: "linear-canary-secret", GrantedScopes: scopes}, nil
}
func (scopeProvider) Refresh(context.Context, oauth.Account, oauth.Tokens) (oauth.Tokens, error) {
	panic("non-expiring fixture")
}
func (scopeProvider) Verify(context.Context, oauth.Account, string) (oauth.Principal, error) {
	return oauth.Principal{ID: userID, Login: userID}, nil
}
func (scopeProvider) Rediscover(context.Context, oauth.Account, string) error { return nil }
func (scopeProvider) Revoke(context.Context, oauth.Account, oauth.Tokens) (bool, error) {
	return false, nil
}

// Read-only OAuth cannot inherit source allowWrites. After explicit consent,
// both fresh and already-running adapters rediscover scopes and use Bearer;
// repeated resolves must still verify organization, team and active principal.
func TestOAuthScopesGateLinearWrites(t *testing.T) {
	s, f := fixtureSource(t)
	f.oauth = true
	ctx := context.Background()
	account := oauth.Account{Provider: "linear", Name: "work", User: userID, OptIn: true}
	manager := oauth.NewManager(oauth.Store{StateDir: filepath.Join(t.TempDir(), "state")}, "", oauth.Registry{"linear": scopeProvider{}}, s.o.Secrets)
	interaction := oauth.Interaction{In: strings.NewReader(""), Out: &strings.Builder{}}
	if err := manager.Login(ctx, account, interaction); err != nil {
		t.Fatal(err)
	}
	o := s.o
	o.Account = project.AccountConfig{Provider: "linear", Name: "work", User: userID, OAuth: true}
	o.Credentials = &remote.Credentials{Secrets: o.Secrets, OAuth: manager}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Info().Capabilities.Allows(model.Set) {
		t.Fatal("read-only grant advertised writes")
	}
	mutation := model.Mutation{Ref: model.Ref{Source: "lin", ID: "TEST-1"}, Op: model.Set, Field: "priority", Value: "high"}
	if _, err = s.Apply(ctx, mutation); err == nil || f.writes != 0 {
		t.Fatal("read-only grant dispatched a write")
	}
	if err = manager.EnableWrites(ctx, account, interaction); err != nil {
		t.Fatal(err)
	}
	if err = s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.Info().Capabilities.Allows(model.Set) {
		t.Fatal("write consent not rediscovered")
	}
	fresh, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err = fresh.Initialize(ctx); err != nil || !fresh.Info().Capabilities.Allows(model.Set) {
		t.Fatalf("fresh process scope discovery: %v", err)
	}
	item, err := s.Read(ctx, "TEST-1")
	if err != nil {
		t.Fatal(err)
	}
	mutation.ExpectedVersion = item.Version
	if _, err = s.Apply(ctx, mutation); err != nil || f.writes != 1 {
		t.Fatalf("authorized write failed: %v writes=%d", err, f.writes)
	}
	f.wrongOrg = true
	if _, err = s.Read(ctx, "TEST-1"); err == nil {
		t.Fatal("usable token skipped organization verification")
	}
	if s.Info().Capabilities.Allows(model.Set) {
		t.Fatal("failed identity check retained write capabilities")
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

// Catches sprint:current going stale when only the cycle catalog changes: an
// incremental load sees no updated issue, yet the cycle became active. Also
// catches the catalog costing an extra request or cycles altering versions.
func TestCycleCatalogRefreshesWithoutIssueUpdate(t *testing.T) {
	s, f := fixtureSource(t)
	f.size = 2
	ctx := context.Background()
	now := s.o.Now()
	s.o.Now = func() time.Time { return now }
	wrapped := remote.New(s, cache.New(t.TempDir(), "cycles", "lin", "scope"), s.o.Secrets)
	plain, err := wrapped.Load(ctx, source.Cursor{})
	if err != nil || f.cycleQueries != 0 || plain.Sprints != nil || plain.Items[0].Sprint != "" {
		t.Fatalf("cycle work without demand: queries=%d %v", f.cycleQueries, err)
	}
	now = now.Add(time.Minute)
	d, err := wrapped.Load(ctx, source.Cursor{Sprints: true})
	if err != nil || f.cycleQueries != 1 || len(d.Sprints) != 1 || d.Sprints[0].Name != "Cycle 7" {
		t.Fatalf("enabling cycles: queries=%d sprints=%+v %v", f.cycleQueries, d.Sprints, err)
	}
	item := d.Items[slices.IndexFunc(d.Items, func(i model.Item) bool { return i.Ref.ID == "TEST-1" })]
	if item.Sprint != cycleID || item.Version != plain.Items[0].Version {
		t.Fatalf("cached row did not gain its cycle or version changed: %+v", item)
	}
	current, err := query.Parse("sprint:current", query.Options{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func(d source.Delta) query.Snapshot {
		return query.Snapshot{Settings: project.Settings{Sources: []project.SourceConfig{{ID: "lin", Type: "linear"}}}, Sprints: d.Sprints}
	}
	if current.Match(item, snapshot(d), nil) {
		t.Fatal("future cycle matched sprint:current")
	}
	f.cycleActive = true // no issue updatedAt changes
	now = now.Add(time.Minute)
	requests := f.requests
	// A process without sprint demand shares the cache; demand stays sticky.
	d, err = wrapped.Load(ctx, source.Cursor{})
	if err != nil || f.requests-requests != 2 { // same as an unchanged window without cycles
		t.Fatalf("incremental load: requests=%d %v", f.requests-requests, err)
	}
	item = d.Items[slices.IndexFunc(d.Items, func(i model.Item) bool { return i.Ref.ID == "TEST-1" })]
	if item.Sprint != cycleID || !current.Match(item, snapshot(d), nil) {
		t.Fatalf("sprint:current missed the activated cycle: sprint=%q catalog=%+v", item.Sprint, d.Sprints)
	}
	s.sprints.Store(false) // a fresh process reading one item, as bl show does
	if item, err = wrapped.Read(ctx, "TEST-1"); err != nil || item.Sprint != cycleID {
		t.Fatalf("single read lost the cycle: %q %v", item.Sprint, err)
	}
}
