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
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

type nextCoordinator struct {
	*coordinator
	sequence     []string
	outcomes     []model.ClaimResult
	afterAcquire func()
	inspections  int
	raceState    *model.SessionState
}

func (c *nextCoordinator) InspectSession(ctx context.Context, caller model.CallerContext) model.SessionState {
	c.inspections++
	if c.raceState != nil && c.inspections > 1 {
		return *c.raceState
	}
	return c.coordinator.InspectSession(ctx, caller)
}
func (c *nextCoordinator) Acquire(ctx context.Context, caller model.CallerContext, item model.Item) model.ClaimResult {
	c.sequence = append(c.sequence, item.Ref.ID)
	if c.state.State != model.SessionAbsent {
		panic("acquire while holding ownership")
	}
	if len(c.outcomes) > 0 {
		r := c.outcomes[0]
		c.outcomes = c.outcomes[1:]
		if r.Outcome != model.ClaimAcquired {
			return r
		}
	}
	r := c.coordinator.Acquire(ctx, caller, item)
	if c.afterAcquire != nil {
		c.afterAcquire()
	}
	return r
}
func (c *nextCoordinator) ResumeAcquire(ctx context.Context, caller model.CallerContext, ref model.RecoveryRef) model.ClaimResult {
	r := c.coordinator.ResumeAcquire(ctx, caller, ref)
	if engine.ClaimSucceeded(r) {
		c.state = model.SessionState{State: model.SessionHeld, Claim: r.Claim}
	}
	return r
}
func nextFixture(t *testing.T) (project.Config, *nextCoordinator, context.Context) {
	t.Helper()
	root := t.TempDir()
	items := filepath.Join(root, "items")
	if err := os.MkdirAll(items, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := project.Config{Settings: project.Settings{
		Project:      project.ProjectConfig{ID: "test"},
		Sources:      []project.SourceConfig{{ID: "tasks", Type: "native", Path: items, Prefix: "BL"}},
		Statuses:     []project.StatusConfig{{Key: "todo", Category: "todo"}, {Key: "doing", Category: "active"}, {Key: "done", Category: "done"}},
		Workflow:     project.WorkflowConfig{Start: "doing", Done: "done"},
		Ready:        project.ReadyConfig{Statuses: []string{"todo"}, SatisfiedBy: []string{"done"}},
		Coordination: project.CoordinationConfig{Worklease: &struct{}{}},
	}, Discovery: project.Discovery{ControlRoot: root, CommonDir: filepath.Join(root, ".git")}}
	for _, id := range []string{"BL-1", "BL-2", "BL-3"} {
		data, err := format.Serialize(model.Item{Ref: model.Ref{ID: id}, Title: id, Status: "todo"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(items, id+".md"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := &nextCoordinator{coordinator: &coordinator{state: model.SessionState{State: model.SessionAbsent}}}
	return cfg, c, engine.WithCoordination(context.Background(), c, model.CallerContext{Session: "loop"})
}

// The walk must be bounded by one snapshot, skip only known contention, and
// never attempt another resource after a grant or an ambiguous mutation.
func TestNextClaimBoundedWalk(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"contended", "exhausted", "unknown", "unavailable", "query", "inspection-race", "held-race", "pending-race", "pending-unknown-race"} {
		t.Run(mode, func(t *testing.T) {
			cfg, c, ctx := nextFixture(t)
			options := engine.NextClaimOptions{}
			contended := model.ClaimResult{Outcome: model.ClaimContended, Reason: "contention"}
			want := []string{"BL-1", "BL-2"}
			wantReason, wantItem := "", "BL-2"
			wantResumed := false
			switch mode {
			case "contended":
				c.outcomes = []model.ClaimResult{contended}
			case "exhausted":
				c.outcomes = []model.ClaimResult{contended, contended, contended}
				want, wantItem = []string{"BL-1", "BL-2", "BL-3"}, ""
			case "unknown":
				c.outcomes = []model.ClaimResult{{Outcome: model.ClaimUncertain, Reason: "unavailable"}}
				want, wantReason, wantItem = []string{"BL-1"}, "unknown-outcome", ""
			case "unavailable":
				c.outcomes = []model.ClaimResult{{Outcome: model.ClaimUnavailable, Reason: "unavailable"}}
				want, wantReason, wantItem = []string{"BL-1"}, "unavailable", ""
			case "query":
				options.Query, want = "id:2", []string{"BL-2"}
			case "inspection-race":
				c.raceState = &model.SessionState{State: model.SessionHeld, Claim: &model.ClaimInfo{ID: "other", Resources: []string{"tasks:BL-3"}}}
				want, wantReason, wantItem = nil, "claim-held", ""
			case "held-race", "pending-race", "pending-unknown-race":
				claim := &model.ClaimInfo{ID: "claim", Resources: []string{"tasks:BL-1"}}
				c.raceState = &model.SessionState{State: model.SessionHeld, Claim: claim}
				want, wantItem, wantResumed = nil, "BL-1", true
				if mode != "held-race" {
					c.raceState.State = model.SessionPending
					c.acquire = model.ClaimResult{Outcome: model.ClaimAcquired, Claim: claim}
				}
				if mode == "pending-unknown-race" {
					c.acquire = model.ClaimResult{Outcome: model.ClaimUncertain}
					wantReason, wantItem, wantResumed = "unknown-outcome", "", false
				}
			}
			result, err := engine.NextClaim(ctx, cfg, options)
			if (err != nil) != (wantReason != "") || err != nil && engine.ErrorReason(err) != wantReason {
				t.Fatalf("error %v, wanted %s; %+v", err, wantReason, result)
			}
			if result.Resumed != wantResumed {
				t.Fatalf("resumed: %+v wanted %v", result, wantResumed)
			}
			if !slices.Equal(c.sequence, want) || c.lists != 0 {
				t.Fatalf("walk: %+v lists:%d want:%v", c.sequence, c.lists, want)
			}
			if wantItem == "" && len(result.Items) != 0 || wantItem != "" && (len(result.Items) != 1 || result.Items[0].Ref.ID != wantItem) {
				t.Fatalf("selection: %+v", result)
			}
			if mode == "unknown" && (len(result.Attempts) != 1 || result.Attempts[0].Ref.ID != "BL-1" || result.Attempts[0].Claim.Reason != "unknown-outcome") {
				t.Fatalf("unknown attempt lost: %+v", result)
			}
		})
	}
}

// Restart must resolve retained ownership, not select a new ready item. Missing
// or closed identities and uncertain replay must never fall into the fresh walk.
func TestNextClaimSessionAndRunFirst(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"held", "interim-label", "pending", "pending-unknown", "replay-mismatch", "closed", "missing", "unresolved", "run", "run-closed", "run-mismatch", "missing-session"} {
		t.Run(mode, func(t *testing.T) {
			cfg, c, ctx := nextFixture(t)
			claim := &model.ClaimInfo{ID: "claim", Resources: []string{"tasks:BL-2"}, Item: "tasks:BL-2", Session: "loop"}
			c.state = model.SessionState{State: model.SessionHeld, Claim: claim}
			wantReason, wantResume := "", true
			switch mode {
			case "interim-label":
				// Direct generic acquisition labels contain a colon, but are
				// not backlog refs. Resolve their verified resource instead.
				claim.Item = "generic:#c5470a94"
			case "pending", "pending-unknown", "replay-mismatch":
				c.state.State = model.SessionPending
				c.acquire = model.ClaimResult{Outcome: model.ClaimAcquired, Claim: claim}
				if mode == "pending-unknown" {
					c.acquire = model.ClaimResult{Outcome: model.ClaimUncertain}
					wantReason, wantResume = "unknown-outcome", false
				}
				if mode == "replay-mismatch" {
					c.acquire.Claim = &model.ClaimInfo{ID: "changed"}
					wantReason, wantResume = "unknown-outcome", false
				}
			case "closed", "run-closed":
				if mode == "run-closed" {
					ctx = engine.WithCoordination(context.Background(), c, model.CallerContext{Session: "loop", Run: "run"})
					c.run = model.ClaimResult{Outcome: model.ClaimRunManaged, Claim: claim}
				}
				item, err := engine.ReadItem(ctx, cfg, model.Ref{Source: "tasks", ID: "BL-2"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = engine.ApplyWrite(context.Background(), uncoordinated(cfg), model.Mutation{Ref: item.Ref, Op: model.Set, Field: "status", Value: "done", ExpectedVersion: item.Version})
				if err != nil {
					t.Fatal(err)
				}
				wantReason, wantResume = "claim-held", false
			case "missing":
				claim.Item = "tasks:BL-99"
				wantReason, wantResume = "unavailable", false
			case "unresolved":
				c.state.State = model.SessionUnresolved
				wantReason, wantResume = "unavailable", false
			case "run", "run-mismatch":
				claim.Item = "run supervisor-label"
				ctx = engine.WithCoordination(context.Background(), c, model.CallerContext{Session: "loop", Run: "run"})
				c.run = model.ClaimResult{Outcome: model.ClaimRunManaged, Claim: claim}
				if mode == "run-mismatch" {
					c.run = model.ClaimResult{Outcome: model.ClaimUnavailable, Reason: "run-mismatch"}
					wantReason, wantResume = "run-mismatch", false
				}
			case "missing-session":
				ctx = engine.WithCoordination(context.Background(), c, model.CallerContext{})
				wantReason, wantResume = "session-required", false
			}
			result, err := engine.NextClaim(ctx, cfg, engine.NextClaimOptions{Query: "id:1"})
			if (err != nil) != (wantReason != "") || err != nil && engine.ErrorReason(err) != wantReason {
				t.Fatalf("%v wanted %s; %+v", err, wantReason, result)
			}
			if result.Resumed != wantResume || len(c.sequence) != 0 || c.lists != 0 {
				t.Fatalf("fell through to selection: %+v %+v", result, c)
			}
			if wantResume && (len(result.Items) != 1 || result.Items[0].Ref.ID != "BL-2") {
				t.Fatalf("lost retained item: %+v", result)
			}
			if (mode == "run" || mode == "run-closed") && c.inspections != 0 || mode == "missing-session" && c.inspections != 0 {
				t.Fatalf("unexpected inspection: %d", c.inspections)
			}
			if (mode == "pending" || mode == "pending-unknown" || mode == "replay-mismatch") && c.replayed != 1 {
				t.Fatalf("replay calls: %d", c.replayed)
			}
		})
	}
}
func uncoordinated(cfg project.Config) project.Config { cfg.Coordination.Worklease = nil; return cfg }

// Claim precedes the version-bound status write; a conflicting source edit
// keeps ownership and reports failure separately. Done must synchronously free
// the session so the next call can immediately acquire another item.
func TestNextClaimStartDoneReuse(t *testing.T) {
	t.Parallel()
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "loop", true: "conflict"}[conflict], func(t *testing.T) {
			cfg, c, ctx := nextFixture(t)
			c.afterAcquire = func() {
				item, err := engine.ReadItem(ctx, cfg, model.Ref{Source: "tasks", ID: "BL-1"})
				if err != nil || item.Status != "todo" {
					t.Fatalf("status changed before claim: %+v %v", item, err)
				}
				if conflict {
					_, err = engine.ApplyWrite(ctx, cfg, model.Mutation{Ref: item.Ref, Op: model.AppendNote, Text: "concurrent edit", ExpectedVersion: item.Version})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := engine.NextClaim(ctx, cfg, engine.NextClaimOptions{Start: true})
			if result.Claim == nil || result.Claim.Outcome != model.ClaimAcquired {
				t.Fatalf("claim outcome: %+v %v", result, err)
			}
			if conflict {
				if err == nil || engine.ErrorReason(err) != "conflict" || result.Status != "failed" || c.state.State != model.SessionHeld {
					t.Fatalf("separate outcomes: %+v %v", result, err)
				}
				return
			}
			if err != nil || result.Status != "written" || result.Items[0].Status != "doing" {
				t.Fatalf("start: %+v %v", result, err)
			}
			_, err = engine.ApplyWrite(ctx, cfg, model.Mutation{Ref: result.Items[0].Ref, Op: model.Set, Field: "status", Value: "done", ExpectedVersion: result.Items[0].Version})
			if err != nil || c.released != 1 {
				t.Fatalf("done: %v releases:%d", err, c.released)
			}
			c.afterAcquire = nil
			result, err = engine.NextClaim(ctx, cfg, engine.NextClaimOptions{})
			if err != nil || len(result.Items) != 1 || result.Items[0].Ref.ID != "BL-2" || !slices.Equal(c.sequence, []string{"BL-1", "BL-2"}) {
				t.Fatalf("reuse: %+v %v calls:%v", result, err, c.sequence)
			}
		})
	}
}

// Malformed files and duplicate native identities must not become usable
// snapshots merely because partial remote observations are now accepted.
func TestInvalidNativeReadsFailClosed(t *testing.T) {
	t.Parallel()
	cfg, c, ctx := nextFixture(t)
	if err := os.WriteFile(filepath.Join(cfg.Sources[0].Path, "BL-2.md"), []byte("---\nid: [\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.LoadReads(ctx, cfg, false); err == nil {
		t.Fatal("list accepted malformed native source")
	}
	if _, err := engine.GoToItems(ctx, cfg); err == nil {
		t.Fatal("Go To accepted malformed native source")
	}
	if _, err := engine.NextClaim(ctx, cfg, engine.NextClaimOptions{}); err == nil || len(c.sequence) != 0 {
		t.Fatalf("claimed from malformed native source: %v attempts=%v", err, c.sequence)
	}
	valid := []byte("---\nid: BL-2\ntitle: Duplicate\nstatus: todo\n---\n")
	if err := os.WriteFile(filepath.Join(cfg.Sources[0].Path, "BL-2.md"), valid, 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(cfg.Sources[0].Path, "archive")
	if err := os.MkdirAll(archive, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "BL-2.md"), valid, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.LoadReads(ctx, cfg, true); err == nil {
		t.Fatal("archive list accepted duplicate native identities")
	}
	if _, err := engine.GoToItems(ctx, cfg); err == nil {
		t.Fatal("Go To accepted duplicate native identities")
	}
}

type readRoundTrip func(*http.Request) (*http.Response, error)

func (f readRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Linear never claims complete coverage. Fresh observations must remain usable
// for lists, next and Go To without making unseen cached rows ready or allowing
// a formerly done (now stale) blocker to satisfy a dependency.
func TestPartialLinearReadsAndNext(t *testing.T) {
	testkit.Isolate(t)
	cfg, c, ctx := nextFixture(t)
	const org = "10000000-0000-0000-0000-000000000001"
	const team = "20000000-0000-0000-0000-000000000001"
	const user = "30000000-0000-0000-0000-000000000001"
	const state = "40000000-0000-0000-0000-000000000001"
	id := func(n int) string { return fmt.Sprintf("60000000-0000-0000-0000-%012d", n) }
	entity := func(n int) map[string]any {
		return map[string]any{"id": id(n), "identifier": fmt.Sprintf("TEST-%d", n), "team": map[string]string{"id": team}}
	}
	conn := func(rows []any) map[string]any {
		return map[string]any{"nodes": rows, "pageInfo": map[string]any{"hasNextPage": false}}
	}
	detail := func(n int) map[string]any {
		row := entity(n)
		category := "unstarted"
		if n == 2 {
			category = "completed"
		}
		row["title"], row["updatedAt"] = "Synthetic", "2026-10-22T00:00:00Z"
		row["state"] = map[string]string{"id": state, "name": category, "type": category}
		row["labels"] = conn([]any{})
		if n == 6 {
			row["archivedAt"] = "2026-10-22T00:00:00Z"
		}
		return row
	}
	missing := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string
			Variables map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		q := req.Query
		var data any
		switch {
		case strings.Contains(q, "viewer{"):
			data = map[string]any{"viewer": map[string]any{"id": user, "active": true, "organization": map[string]string{"id": org}}, "team": map[string]string{"id": team}}
		case strings.Contains(q, "issues(first:"):
			rows := []any{}
			for n := 1; n <= 6; n++ {
				if missing && (n == 2 || n == 5) {
					continue
				}
				row := entity(n)
				row["updatedAt"] = "2026-10-22T00:00:00Z"
				rows = append(rows, row)
			}
			data = map[string]any{"team": map[string]any{"id": team, "issues": conn(rows)}}
		case strings.Contains(q, "i0:issue("):
			batch := map[string]any{}
			for i := 0; i < len(req.Variables); i++ {
				var n int
				_, _ = fmt.Sscanf(req.Variables[fmt.Sprintf("id%d", i)].(string), "60000000-0000-0000-0000-%012d", &n)
				row := detail(n)
				incoming := []any{}
				blocker := 0
				if n == 3 {
					blocker = 2
				} else if n == 4 {
					blocker = 99
				}
				if blocker != 0 {
					incoming = append(incoming, map[string]any{"type": "blocks", "issue": entity(blocker)})
				}
				row["relations"], row["inverseRelations"] = conn([]any{}), conn(incoming)
				batch[fmt.Sprintf("i%d", i)] = row
			}
			data = batch
		case strings.Contains(q, "inverseRelations(") || strings.Contains(q, "relations("):
			key, rows := "relations", []any{}
			if strings.Contains(q, "inverseRelations(") {
				key = "inverseRelations"
				blocker := 0
				if req.Variables["id"] == id(3) {
					blocker = 2
				} else if req.Variables["id"] == id(4) {
					blocker = 99
				}
				if blocker != 0 {
					rows = append(rows, map[string]any{"type": "blocks", "issue": entity(blocker)})
				}
			}
			data = map[string]any{"issue": map[string]any{"id": req.Variables["id"], key: conn(rows)}}
		default:
			var n int
			_, _ = fmt.Sscanf(req.Variables["id"].(string), "60000000-0000-0000-0000-%012d", &n)
			data = map[string]any{"issue": detail(n)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
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
	t.Setenv("BL_PARTIAL_LINEAR_TOKEN", "partial-linear-test-credential")
	cfg.Sources = []project.SourceConfig{{ID: "tasks", Type: "linear", Site: org, Team: team, Account: "test"}}
	cfg.Accounts = map[string][]project.AccountConfig{"linear": {{Name: "test", User: user, Env: "BL_PARTIAL_LINEAR_TOKEN"}}}
	cfg.Paths.CacheDir = t.TempDir()
	cfg.Paths.StateDir = t.TempDir()
	reads, err := engine.LoadReads(ctx, uncoordinated(cfg), false)
	if err != nil || len(reads.Query.Items) != 5 {
		t.Fatalf("initial partial list: %+v %v", reads, err)
	}
	missing = true
	// Changing archive visibility forces reconciliation, where omissions must
	// become stale. Minute change windows deliberately retain prior freshness.
	for _, archived := range []bool{true, false} {
		reads, err = engine.LoadReads(ctx, uncoordinated(cfg), archived)
		if err != nil {
			t.Fatal(err)
		}
		wantCount := 5
		if archived {
			wantCount = 6
		}
		if len(reads.Query.Items) != wantCount {
			t.Fatalf("partial list lost cached rows (archives=%v): %+v", archived, reads.Query.Items)
		}
		if !slices.Equal(reads.Ranking.Candidates, []model.Ref{{Source: "tasks", ID: "TEST-1"}}) {
			t.Fatalf("unsafe partial next (archives=%v): %+v", archived, reads.Ranking)
		}
		for _, e := range reads.Ranking.Items {
			if slices.Contains([]string{"TEST-3", "TEST-4", "TEST-5"}, e.Ref.ID) && e.Readiness != engine.Unknown {
				t.Fatalf("stale or missing blocker treated as known: %+v", e)
			}
		}
	}
	workspace := uncoordinated(cfg)
	workspace.Workspace = &project.Workspace{Projects: []project.Config{uncoordinated(cfg)}, Sources: map[string]project.WorkspaceSource{"test/tasks": {Project: 0, ID: "tasks"}}}
	composed, err := engine.LoadReads(ctx, workspace, false)
	if err != nil || !slices.Equal(composed.Ranking.Candidates, []model.Ref{{Source: "test/tasks", ID: "TEST-1"}}) {
		t.Fatalf("workspace lost partial freshness: %+v %v", composed.Ranking, err)
	}
	rows, err := engine.GoToItems(ctx, cfg)
	if err != nil || len(rows) != 6 {
		t.Fatalf("partial Go To: %+v %v", rows, err)
	}
	result, err := engine.NextClaim(ctx, cfg, engine.NextClaimOptions{})
	if err != nil || len(result.Items) != 1 || result.Items[0].Ref.ID != "TEST-1" || !slices.Equal(c.sequence, []string{"TEST-1"}) {
		t.Fatalf("fresh partial claim: %+v %v attempts=%v", result, err, c.sequence)
	}
	// A retained human work label resolves by verified resource, not by rank;
	// a stale row must never be used as that identity observation.
	c.state.Claim.Item = "run supervisor-label"
	result, err = engine.NextClaim(ctx, cfg, engine.NextClaimOptions{})
	if err != nil || !result.Resumed || result.Items[0].Ref.ID != "TEST-1" {
		t.Fatalf("partial identity resume: %+v %v", result, err)
	}
	c.state.Claim.Resources = []string{"tasks:TEST-5"}
	if _, err = engine.NextClaim(ctx, cfg, engine.NextClaimOptions{}); engine.ErrorReason(err) != "unavailable" {
		t.Fatalf("stale identity resumed: %v", err)
	}
}
