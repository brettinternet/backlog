package engine_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

// This fake exercises authority decisions independently of provider executables.
type coordinator struct {
	mu                                    sync.Mutex
	state                                 model.SessionState
	acquired, released, renewed, replayed int
	acquire, release, run                 model.ClaimResult
	claims                                []model.ClaimInfo
	lists                                 int
}

func (*coordinator) Resource(i model.Item) (string, error) { return i.Ref.Source + ":" + i.Ref.ID, nil }
func (c *coordinator) InspectSession(context.Context, model.CallerContext) model.SessionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}
func (c *coordinator) Acquire(_ context.Context, caller model.CallerContext, i model.Item) model.ClaimResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.acquired++
	if c.acquire.Outcome != "" {
		return c.acquire
	}
	claim := &model.ClaimInfo{ID: "claim", Resources: []string{i.Ref.Source + ":" + i.Ref.ID}, Session: caller.Session}
	c.state = model.SessionState{State: "held", Claim: claim}
	return model.ClaimResult{Outcome: "acquired", Claim: claim}
}
func (c *coordinator) ResumeAcquire(context.Context, model.CallerContext, model.RecoveryRef) model.ClaimResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.replayed++
	return c.acquire
}
func (c *coordinator) Release(_ context.Context, _ model.CallerContext, _ model.Item, expectedClaimID string) model.ClaimResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.released++
	if c.release.Outcome != "" {
		return c.release
	}
	if c.state.Claim == nil || c.state.Claim.ID != expectedClaimID {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: "claim identity changed"}
	}
	c.state = model.SessionState{State: "absent"}
	return model.ClaimResult{Outcome: "released"}
}
func (c *coordinator) Heartbeat(context.Context, model.CallerContext) model.ClaimResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renewed++
	return model.ClaimResult{Outcome: "renewed", ExpiresAt: time.Unix(200, 0)}
}
func (c *coordinator) History(context.Context, string) ([]model.ClaimEpoch, error) { return nil, nil }
func (c *coordinator) List(context.Context) ([]model.ClaimInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lists++
	return slices.Clone(c.claims), nil
}
func (c *coordinator) VerifyRun(context.Context, model.CallerContext, string) model.ClaimResult {
	return c.run
}
func coordinationConfig() project.Config {
	cfg := storeConfig()
	cfg.Coordination.Worklease = &struct{}{}
	cfg.Statuses = append(cfg.Statuses, project.StatusConfig{Key: "doing", Category: "active"})
	return cfg
}

// Regression: a claim decision must gate real source writes, and a failed source
// write must not undo the independently committed acquisition.
func TestCoordinatedStoreTransitions(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"contention", "write-failure", "held", "run", "release-failure", "foreign", "unresolved", "run-mismatch", "missing-session"} {
		t.Run(mode, func(t *testing.T) {
			backend := fakeSource(t, "tasks")
			cfg := coordinationConfig()
			ref := model.Ref{Source: "tasks", ID: "BL-1"}
			c := &coordinator{state: model.SessionState{State: "absent"}}
			caller := model.CallerContext{Session: "loop"}
			target := "doing"
			switch mode {
			case "contention":
				c.acquire = model.ClaimResult{Outcome: "contended", Reason: "contention", Message: "holder other@host"}
			case "write-failure":
				backend.failure = errors.New("disk full")
			case "held":
				c.state = model.SessionState{State: "held", Claim: &model.ClaimInfo{Resources: []string{"tasks:BL-1"}, Session: "loop"}}
			case "run":
				caller.Run = "supervisor"
				c.run = model.ClaimResult{Outcome: "run-managed"}
			case "release-failure":
				target = "done"
				c.state = model.SessionState{State: "held", Claim: &model.ClaimInfo{ID: "claim", Resources: []string{"tasks:BL-1"}, Session: "loop"}}
				c.release = model.ClaimResult{Outcome: "uncertain", Reason: "unavailable", Message: "release uncertain"}
			case "foreign":
				target = "done"
				c.state = model.SessionState{State: "held", Claim: &model.ClaimInfo{ID: "other-claim", Resources: []string{"tasks:BL-2"}, Session: "loop"}}
			case "unresolved":
				target = "done"
				c.state = model.SessionState{State: "unresolved", Message: "corrupt handle"}
			case "run-mismatch":
				target = "done"
				caller.Run = "wrong"
				c.run = model.ClaimResult{Outcome: "unavailable", Reason: "run-mismatch", Message: "wrong run"}
			case "missing-session":
				target = "done"
				caller.Session = ""
			}
			ctx := engine.WithCoordination(context.Background(), c, caller)
			store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
			defer store.Close()
			before := awaitSnapshot(t, store, func(s *engine.Snapshot) bool { return s.Len() == 1 })
			item, _, _ := before.Get(ref)
			err := awaitResult(t, store.Mutate(model.Mutation{Op: model.Set, Field: "status", Value: target, Ref: ref, ExpectedVersion: item.Version}))
			current, _, _ := store.Current().Get(ref)
			c.mu.Lock()
			defer c.mu.Unlock()
			switch mode {
			case "contention", "unresolved", "run-mismatch", "missing-session":
				if err == nil || backend.calls.Load() != 0 || current.Status != "todo" {
					t.Fatalf("unsafe write: err=%v calls=%d status=%s", err, backend.calls.Load(), current.Status)
				}
			case "write-failure":
				if err == nil || c.acquired != 1 || c.released != 0 || c.state.State != "held" {
					t.Fatalf("lost claim after failed write: %v %+v", err, c)
				}
			case "held", "run":
				if err != nil || c.acquired != 0 || c.released != 0 || current.Status != "doing" {
					t.Fatalf("reacquired held/run claim: %v %+v", err, c)
				}
			case "release-failure":
				if err == nil || current.Status != "done" || c.released != 1 {
					t.Fatalf("release failure hid committed status: %v %s", err, current.Status)
				}
			case "foreign":
				if err != nil || c.released != 0 || current.Status != "done" {
					t.Fatalf("foreign release: %v %+v", err, c)
				}
			}
		})
	}
}

type unsupportedCoordinator struct{ *coordinator }

func (unsupportedCoordinator) Unsupported() string { return "claims need Worklease" }

// Regression: on Windows an active transition must refuse rather than write
// unclaimed, while other status writes still work without a session.
func TestClaimsUnsupportedPlatform(t *testing.T) {
	t.Parallel()
	backend := fakeSource(t, "tasks")
	ref := model.Ref{Source: "tasks", ID: "BL-1"}
	c := &coordinator{state: model.SessionState{State: "absent"}}
	ctx := engine.WithCoordination(context.Background(), unsupportedCoordinator{c}, model.CallerContext{})
	store := engine.OpenStore(ctx, coordinationConfig(), source.Registry{"tasks": backend})
	defer store.Close()
	item, _, _ := awaitSnapshot(t, store, func(s *engine.Snapshot) bool { return s.Len() == 1 }).Get(ref)
	err := awaitResult(t, store.Mutate(model.Mutation{Op: model.Set, Field: "status", Value: "doing", Ref: ref, ExpectedVersion: item.Version}))
	if engine.ErrorReason(err) != "unavailable" || !strings.Contains(engine.ErrorMessage(err), "Worklease") || backend.calls.Load() != 0 {
		t.Fatalf("active write: err=%v calls=%d", err, backend.calls.Load())
	}
	if err := awaitResult(t, store.Mutate(model.Mutation{Op: model.Set, Field: "status", Value: "done", Ref: ref, ExpectedVersion: item.Version})); err != nil {
		t.Fatalf("done write: %v", err)
	}
	item, _, _ = store.Current().Get(ref)
	if r := engine.ClaimItem(ctx, item); r.Outcome != model.ClaimUnavailable || !strings.Contains(r.Message, "Worklease") {
		t.Fatalf("claim: %+v", r)
	}
	if item.Status != "done" || c.acquired+c.released != 0 {
		t.Fatalf("status=%s coordinator=%+v", item.Status, c)
	}
}

type choosingSource struct {
	*controlledSource
	choices []model.StatusChoice
}

func (s choosingSource) StatusChoices(context.Context, model.Item) ([]model.StatusChoice, bool, error) {
	return s.choices, true, nil
}

// Provider-reported categories must gate claims even when project statuses
// omit (or misclassify) the destination name, as for Jira transitions.
func TestProviderStatusChoiceClaims(t *testing.T) {
	t.Parallel()
	for _, contended := range []bool{false, true} {
		t.Run(fmt.Sprint(contended), func(t *testing.T) {
			backend := fakeSource(t, "tasks")
			cfg := coordinationConfig()
			cfg.Sources = []project.SourceConfig{{ID: "tasks", Type: "jira"}}
			c := &coordinator{state: model.SessionState{State: "absent"}}
			if contended {
				c.acquire = model.ClaimResult{Outcome: "contended", Reason: "contention"}
			}
			ctx := engine.WithCoordination(context.Background(), c, model.CallerContext{Session: "loop"})
			store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": choosingSource{backend, []model.StatusChoice{{Key: "Start", Category: model.Active}}}})
			defer store.Close()
			before := awaitSnapshot(t, store, func(s *engine.Snapshot) bool { return s.Len() == 1 })
			item, _, _ := before.Get(model.Ref{Source: "tasks", ID: "BL-1"})
			err := awaitResult(t, store.Mutate(model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "status", Value: "Start"}))
			if c.acquired != 1 || contended && (err == nil || backend.calls.Load() != 0) || !contended && (err != nil || backend.calls.Load() != 1) {
				t.Fatalf("dynamic claim bypass: contended=%v err=%v acquired=%d writes=%d", contended, err, c.acquired, backend.calls.Load())
			}
		})
	}
}

func TestSessionRecoveryNeverFreshAcquires(t *testing.T) {
	t.Parallel()
	item := model.Item{Ref: model.Ref{Source: "tasks", ID: "BL-1"}}
	for _, state := range []string{"held", "pending", "unresolved"} {
		c := &coordinator{state: model.SessionState{State: model.SessionKind(state), Claim: &model.ClaimInfo{Resources: []string{"tasks:BL-1"}}}, acquire: model.ClaimResult{Outcome: "acquired"}}
		r := engine.ClaimItem(engine.WithCoordination(context.Background(), c, model.CallerContext{Session: "loop"}), item)
		if c.acquired != 0 || state == "pending" && c.replayed != 1 || state == "unresolved" && engine.ClaimSucceeded(r) {
			t.Fatalf("%s: %+v %+v", state, c, r)
		}
	}
	c := &coordinator{state: model.SessionState{State: "held", Claim: &model.ClaimInfo{Resources: []string{"tasks:BL-2"}, Item: "BL-2"}}}
	r := engine.ClaimItem(engine.WithCoordination(context.Background(), c, model.CallerContext{Session: "loop"}), item)
	if r.Reason != "claim-held" || r.Message != "session already holds BL-2" || c.acquired != 0 {
		t.Fatalf("held: %+v", r)
	}
}

// Expiry changes query/readiness facts using the same fetched snapshot, not I/O.
func TestBulkOverlayExpiryAndReadiness(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	item := model.Item{Ref: model.Ref{Source: "tasks", ID: "BL-1"}, Category: model.Todo}
	c := &coordinator{claims: []model.ClaimInfo{{Resources: []string{"tasks:BL-1"}, Agent: "agent@host", Session: "other", ExpiresAt: now.Add(time.Second)}}}
	ctx := engine.WithCoordination(context.Background(), c, model.CallerContext{Session: "loop"})
	states, labels, err := engine.ClaimStates(ctx, []model.Item{item}, now)
	if err != nil || labels[item.Ref] != "agent@host" {
		t.Fatalf("overlay: %v %v", labels, err)
	}
	result := engine.Evaluate(engine.Input{Items: []model.Item{item}, Config: storeConfig().Ready, Coordination: true, Claims: states})
	if result.Items[0].Readiness != engine.Waiting {
		t.Fatal(result)
	}
	states, labels = engine.OverlayStates(c, model.CallerContext{Session: "loop"}, []model.Item{item}, c.claims, now.Add(time.Second))
	if labels[item.Ref] != "expired" || states[item.Ref] != engine.ClaimNone || c.lists != 1 {
		t.Fatalf("expiry required polling: %v %v %d", states, labels, c.lists)
	}
	c.claims[0].Session = "loop"
	c.claims[0].ExpiresAt = now.Add(time.Hour)
	_, labels = engine.OverlayStates(c, model.CallerContext{Session: "loop"}, []model.Item{item}, c.claims, now)
	if labels[item.Ref] != "mine" {
		t.Fatal(labels)
	}
}

// One resource per issue across sources: a repo and a project source share it.
type issueCoordinator struct{ *coordinator }

func (issueCoordinator) Resource(i model.Item) (string, error) { return "issue:" + i.Ref.ID, nil }

// A person holds many claims, each under its own session derived from the
// claim resource, so every source of the item sees the claim as mine, another
// item's claim is not, and a long key still fits Worklease's session limit.
func TestPerItemCallerClaimsUnderResourceSession(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	repo, board, other := model.Item{Ref: model.Ref{Source: "gh", ID: "12"}}, model.Item{Ref: model.Ref{Source: "roadmap", ID: "12"}}, model.Item{Ref: model.Ref{Source: "gh", ID: "13"}}
	caller := model.CallerContext{Session: "tui", PerItem: true}
	c := issueCoordinator{&coordinator{state: model.SessionState{State: "absent"}}}
	r := engine.ClaimItem(engine.WithCoordination(context.Background(), c, caller), repo)
	if r.Outcome != "acquired" || !strings.HasPrefix(r.Claim.Session, "tui:") {
		t.Fatalf("claim: %+v", r)
	}
	claims := []model.ClaimInfo{
		{Resources: []string{"issue:12"}, Session: r.Claim.Session, Agent: "me@host", ExpiresAt: now.Add(time.Hour)},
		{Resources: []string{"issue:13"}, Session: r.Claim.Session, Agent: "me@host", ExpiresAt: now.Add(time.Hour)},
	}
	states, labels := engine.OverlayStates(c, caller, []model.Item{repo, board, other}, claims, now)
	if labels[repo.Ref] != "mine" || labels[board.Ref] != "mine" || states[other.Ref] != engine.ClaimOthers {
		t.Fatalf("overlay: %v %v", states, labels)
	}
	if session := caller.For(strings.Repeat("x", 300)).Session; len(session) > 128 {
		t.Fatalf("session is %d bytes", len(session))
	}
}

// A superseding projection must recover from a canceled startup lookup or
// refresh without retaining an unavailable warning or losing the claim overlay.
type canceledListCoordinator struct {
	coordinator
	cancelCall int
	calls      int
	started    chan struct{}
}

func (c *canceledListCoordinator) List(ctx context.Context) ([]model.ClaimInfo, error) {
	c.calls++
	if c.calls == c.cancelCall {
		close(c.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.coordinator.List(ctx)
}

func TestProjectionRetriesCanceledClaimLookup(t *testing.T) {
	t.Parallel()
	for _, refresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("refresh=%t", refresh), func(t *testing.T) {
			t.Parallel()
			cfg := coordinationConfig()
			c := &canceledListCoordinator{
				coordinator: coordinator{claims: []model.ClaimInfo{{ID: "claim", Resources: []string{"tasks:BL-1"}, Session: "other", Agent: "agent@host", ExpiresAt: time.Now().Add(time.Hour)}}},
				cancelCall:  1,
				started:     make(chan struct{}),
			}
			if refresh {
				c.cancelCall = 2
			}
			ctx := engine.WithCoordination(context.Background(), c, model.CallerContext{Session: "loop"})
			store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": fakeSource(t, "tasks")})
			defer store.Close()
			snap := awaitSnapshot(t, store, func(s *engine.Snapshot) bool { return s.Len() == 1 })
			projector := engine.NewProjector(ctx, cfg)
			defer projector.Close()
			await := func(id uint64) engine.Projection {
				t.Helper()
				select {
				case result := <-projector.Results():
					if result.Request != id || result.Error != nil || result.Warning != nil {
						t.Fatalf("projection: %+v", result)
					}
					return result
				case <-time.After(5 * time.Second):
					t.Fatal("projection timeout")
					return engine.Projection{}
				}
			}
			if refresh {
				await(projector.Request(snap, "is:open"))
				projector.RequestQuery(snap, engine.ProjectionQuery{Text: "is:open", RefreshClaims: true})
			} else {
				projector.Request(snap, "is:open")
			}
			select {
			case <-c.started:
			case <-time.After(5 * time.Second):
				t.Fatal("claim lookup did not start")
			}
			result := await(projector.Request(snap, "is:open claimed:others"))
			if len(result.Groups) != 1 || len(result.Groups[0].Rows) != 1 || result.Groups[0].Rows[0].Claim != "agent@host" {
				t.Fatalf("claim overlay not recovered: %+v", result)
			}
		})
	}
}

type slowRefreshCoordinator struct {
	coordinator
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (c *slowRefreshCoordinator) List(ctx context.Context) ([]model.ClaimInfo, error) {
	if c.calls.Add(1) > 1 {
		select {
		case c.started <- struct{}{}:
		default:
		}
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.coordinator.List(ctx)
}

// An external status change during a slow claim poll must reach the board
// without a second blocking lookup, while retaining claims until the next poll.
func TestProjectionStatusRefreshDoesNotWaitForCanceledClaimPoll(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := &slowRefreshCoordinator{
		coordinator: coordinator{claims: []model.ClaimInfo{{ID: "claim", Resources: []string{"tasks:BL-1"}, Session: "other", Agent: "agent@host", ExpiresAt: time.Now().Add(time.Hour)}}},
		started:     make(chan struct{}, 1),
		release:     make(chan struct{}),
	}
	ctx = engine.WithCoordination(ctx, c, model.CallerContext{Session: "loop"})
	cfg := coordinationConfig()
	backend := fakeSource(t, "tasks")
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
	defer store.Close()
	snapshot := awaitSnapshot(t, store, func(s *engine.Snapshot) bool { return s.Len() == 1 })
	projector := engine.NewProjector(ctx, cfg)
	defer projector.Close()
	await := func(request uint64, status, claim string) {
		t.Helper()
		select {
		case result := <-projector.Results():
			if result.Request != request || result.Error != nil || result.Warning != nil || len(result.Groups) != 1 || len(result.Groups[0].Rows) != 1 {
				t.Fatalf("projection: %+v", result)
			}
			row := result.Groups[0].Rows[0]
			if result.Groups[0].Value != status || row.Status != status || row.Claim != claim {
				t.Fatalf("status/claim overlay: %+v", row)
			}
		case <-ctx.Done():
			t.Fatal("status projection blocked on claim provider")
		}
	}
	const query = "is:open group:status"
	await(projector.Request(snapshot, query), "todo", "agent@host")
	projector.RequestQuery(snapshot, engine.ProjectionQuery{Text: query, RefreshClaims: true})
	select {
	case <-c.started:
	case <-ctx.Done():
		t.Fatal("claim poll did not start")
	}
	backend.mu.Lock()
	item := backend.current()
	item.Status, item.Category, item.Version = "doing", model.Active, "external-v2"
	backend.items = []model.Item{item}
	backend.mu.Unlock()
	store.Refresh()
	snapshot = awaitSnapshot(t, store, func(s *engine.Snapshot) bool {
		updated, _, ok := s.Get(item.Ref)
		return ok && updated.Version == item.Version
	})
	await(projector.Request(snapshot, query), "doing", "agent@host")
	if c.calls.Load() != 2 {
		t.Fatal("source update retried the slow claim poll")
	}
	c.mu.Lock()
	c.claims = nil
	c.mu.Unlock()
	close(c.release)
	await(projector.RequestQuery(snapshot, engine.ProjectionQuery{Text: query, RefreshClaims: true}), "doing", "")
	if c.calls.Load() != 3 {
		t.Fatal("next explicit poll did not refresh claims")
	}
}

// A long-lived projection must invalidate claim-derived queries exactly at the
// scheduled expiry, using its injected clock and the SAME source snapshot.
func TestProjectionClaimExpiryWithoutPolling(t *testing.T) {
	t.Parallel()
	cfg := coordinationConfig()
	backend := fakeSource(t, "tasks")
	c := &coordinator{claims: []model.ClaimInfo{{ID: "claim", Resources: []string{"tasks:BL-1"}, Session: "other", Agent: "agent@host", ExpiresAt: time.Unix(101, 0)}}}
	ctx := engine.WithCoordination(context.Background(), c, model.CallerContext{Session: "loop"})
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
	defer store.Close()
	snap := awaitSnapshot(t, store, func(s *engine.Snapshot) bool { return s.Len() == 1 })
	var clock atomic.Int64
	clock.Store(100)
	projector := engine.NewProjectorWithClock(ctx, cfg, func() time.Time { return time.Unix(clock.Load(), 0) })
	defer projector.Close()
	project := func(text string) engine.Projection {
		t.Helper()
		id := projector.Request(snap, text)
		select {
		case result := <-projector.Results():
			if result.Request != id || result.Error != nil {
				t.Fatalf("projection: %+v", result)
			}
			return result
		case <-time.After(5 * time.Second):
			t.Fatal("projection timeout")
			return engine.Projection{}
		}
	}
	result := project("is:open claimed:others")
	if len(result.Groups) != 1 || result.Groups[0].Rows[0].Claim != "agent@host" || !result.NextClaimExpiry.Equal(time.Unix(101, 0)) {
		t.Fatalf("before expiry: %+v", result)
	}
	clock.Store(101)
	result = project("is:open")
	if result.Groups[0].Rows[0].Claim != "expired" || result.Groups[0].Rows[0].Readiness != engine.Ready || !result.NextClaimExpiry.IsZero() {
		t.Fatalf("after expiry: %+v", result)
	}
	result = project("is:open is:claimed")
	if len(result.Groups) != 1 || len(result.Groups[0].Rows) != 0 {
		t.Fatalf("expired query still matches: %+v", result)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lists != 1 {
		t.Fatalf("expiry polled provider %d times", c.lists)
	}
}

// Regression: a GitHub project source accepts closed and a Done option, so a
// workflow.done neither matches must take the source's default, closed (D35).
func TestWorkflowStatusSourceDefault(t *testing.T) {
	t.Parallel()
	cfg := coordinationConfig()
	cfg.Workflow = project.WorkflowConfig{Start: "doing", Done: "done"}
	choices := []model.StatusChoice{{Key: "open", Category: model.Todo}, {Key: "closed", Category: model.Done, Default: true}, {Key: "option-active", Category: model.Active}, {Key: "option-done", Category: model.Done}}
	store := engine.OpenStore(context.Background(), cfg, source.Registry{"tasks": choosingSource{fakeSource(t, "tasks"), choices}})
	defer store.Close()
	snapshot := awaitSnapshot(t, store, func(s *engine.Snapshot) bool { return s.Len() == 1 })
	item, _, _ := snapshot.Get(model.Ref{Source: "tasks", ID: "BL-1"})
	for category, want := range map[model.StatusCategory]string{model.Done: "closed", model.Active: "option-active"} {
		if got, err := store.WorkflowStatus(item, category); err != nil || got != want {
			t.Errorf("%s: got %q %v want %q", category, got, err, want)
		}
	}
}

// sources[].statuses must keep another source's status, such as GitHub's
// closed, off a native item on every write path, not just in TUI choices.
func TestSourceStatusesRefuseOtherStatuses(t *testing.T) {
	t.Parallel()
	cfg := storeConfig()
	cfg.Statuses = append(cfg.Statuses, project.StatusConfig{Key: "closed", Category: "done"})
	cfg.Sources = []project.SourceConfig{{ID: "tasks", Type: "native", Statuses: []string{"todo", "done"}}}
	store := engine.OpenStore(context.Background(), cfg, source.Registry{"tasks": fakeSource(t, "tasks")})
	defer store.Close()
	snapshot := awaitSnapshot(t, store, func(s *engine.Snapshot) bool { return s.Len() == 1 })
	item, _, _ := snapshot.Get(model.Ref{Source: "tasks", ID: "BL-1"})
	if choices, err := store.StatusChoices(item); err != nil || len(choices) != 2 {
		t.Fatalf("choices %+v %v", choices, err)
	}
	set := model.Mutation{Ref: item.Ref, Op: model.Set, Field: "status", Value: "closed", ExpectedVersion: item.Version}
	if err := <-store.Mutate(set); engine.ErrorReason(err) != "rejected" || !strings.Contains(err.Error(), "does not accept status closed") {
		t.Fatalf("got %v", err)
	}
	set.Value = "done"
	if err := <-store.Mutate(set); err != nil {
		t.Fatalf("accepted status refused: %v", err)
	}
}

// A status entry stands for the one included key the item's source accepts,
// for writes from every client and for workflow resolution (D36). A wrong
// resolution would write another source's key or refuse a valid start.
func TestStatusEntryResolvesPerSource(t *testing.T) {
	t.Parallel()
	cfg := storeConfig()
	cfg.Sources = []project.SourceConfig{{ID: "tasks", Type: "jira"}}
	cfg.Statuses = append(cfg.Statuses,
		project.StatusConfig{Key: "doing", Category: "active", Includes: []string{"option-active", "other-active"}},
		project.StatusConfig{Key: "review", Category: "active", Includes: []string{"missing"}})
	cfg.Workflow = project.WorkflowConfig{Start: "doing"}
	backend := fakeSource(t, "tasks")
	choices := []model.StatusChoice{{Key: "option-active", Category: model.Active}, {Key: "option-review", Category: model.Active}}
	store := engine.OpenStore(context.Background(), cfg, source.Registry{"tasks": choosingSource{backend, choices}})
	defer store.Close()
	snapshot := awaitSnapshot(t, store, func(s *engine.Snapshot) bool { return s.Len() == 1 })
	item, _, _ := snapshot.Get(model.Ref{Source: "tasks", ID: "BL-1"})
	if got, err := store.WorkflowStatus(item, model.Active); err != nil || got != "option-active" {
		t.Fatalf("workflow.start: got %q %v", got, err)
	}
	err := awaitResult(t, store.Mutate(model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "status", Value: "review"}))
	if engine.ErrorMessage(err) != "source tasks accepts none of status review (review, missing)" || backend.calls.Load() != 0 {
		t.Fatalf("unaccepted entry: %v writes=%d", err, backend.calls.Load())
	}
	if err := awaitResult(t, store.Mutate(model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "status", Value: "doing"})); err != nil || backend.current().Status != "option-active" {
		t.Fatalf("entry write: %v status=%q", err, backend.current().Status)
	}
}
