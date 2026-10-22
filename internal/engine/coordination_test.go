package engine_test

import (
	"context"
	"errors"
	"slices"
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
	c := &coordinator{claims: []model.ClaimInfo{{Resources: []string{"tasks:BL-1"}, Agent: "agent", Host: "host", Session: "other", ExpiresAt: now.Add(time.Second)}}}
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

// A long-lived projection must invalidate claim-derived queries exactly at the
// scheduled expiry, using its injected clock and the SAME source snapshot.
func TestProjectionClaimExpiryWithoutPolling(t *testing.T) {
	t.Parallel()
	cfg := coordinationConfig()
	backend := fakeSource(t, "tasks")
	c := &coordinator{claims: []model.ClaimInfo{{ID: "claim", Resources: []string{"tasks:BL-1"}, Session: "other", Agent: "agent", Host: "host", ExpiresAt: time.Unix(101, 0)}}}
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
