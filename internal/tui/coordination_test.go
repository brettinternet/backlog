package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
)

type contendedCoordinator struct{}

func (contendedCoordinator) Resource(i model.Item) (string, error) { return i.Ref.ID, nil }
func (contendedCoordinator) InspectSession(context.Context, model.CallerContext) model.SessionState {
	return model.SessionState{State: "absent"}
}
func (contendedCoordinator) Acquire(context.Context, model.CallerContext, model.Item) model.ClaimResult {
	return model.ClaimResult{Outcome: "contended", Reason: "contention", Message: "held by agent@host"}
}
func (contendedCoordinator) Release(context.Context, model.CallerContext, model.Item, string) model.ClaimResult {
	panic("unexpected release")
}
func (contendedCoordinator) Heartbeat(context.Context, model.CallerContext) model.ClaimResult {
	panic("unexpected renewal")
}
func (contendedCoordinator) ResumeAcquire(context.Context, model.CallerContext, model.RecoveryRef) model.ClaimResult {
	panic("unexpected replay")
}
func (contendedCoordinator) List(context.Context) ([]model.ClaimInfo, error) { return nil, nil }
func (contendedCoordinator) History(context.Context, string) ([]model.ClaimEpoch, error) {
	return nil, nil
}
func (contendedCoordinator) VerifyRun(context.Context, model.CallerContext, string) model.ClaimResult {
	panic("unexpected run verification")
}

// Key routing must use the engine gate, not silently fall back to the old
// status-only mutation; explicit C must also report the contention holder.
func TestClaimAndStartKeysHonorContention(t *testing.T) {
	t.Parallel()
	for _, keys := range [][]string{{"C"}, {"S"}, {"s", "active", "enter"}} {
		t.Run(strings.Join(keys, "-"), func(t *testing.T) {
			ctx := engine.WithCoordination(context.Background(), contendedCoordinator{}, model.CallerContext{Session: "loop"})
			item := model.Item{Ref: model.Ref{Source: "tasks", ID: "BL-1"}, Title: "Item", Status: "todo", Category: model.Todo, Version: "v1"}
			m, backend := editFixture(t, []model.Item{item}, model.Capabilities{model.Set: true}, Options{Context: ctx, Read: func(context.Context, model.Ref) (model.Item, error) { return item, nil }})
			for _, key := range keys {
				m = editPress(m, key)
			}
			if len(backend.applied()) != 0 || !strings.Contains(m.notice, "agent@host") {
				t.Fatalf("contention wrote/hid holder: %v %s", backend.applied(), m.notice)
			}
		})
	}
}

// Repeated projections before a long lease expires must not build a timer burst
// that can flood Update when the claim finally expires.
func TestClaimExpiryTimerIsCoalesced(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	m := New(Options{Now: func() time.Time { return now }})
	m.claimExpiry = now.Add(time.Minute)
	if m.expiryCommand() == nil {
		t.Fatal("expiry was not scheduled")
	}
	for range 100 {
		if m.expiryCommand() != nil {
			t.Fatal("duplicate expiry timer")
		}
	}
	next, _ := m.Update(claimsExpiry(m.claimExpiry))
	m = next.(Model)
	if !m.claimTimerAt.IsZero() {
		t.Fatal("expiry timer remained pending")
	}
}

type renewingCoordinator struct {
	contendedCoordinator
	renewed *[]string
}

func (c renewingCoordinator) InspectSession(context.Context, model.CallerContext) model.SessionState {
	return model.SessionState{State: "held", Claim: &model.ClaimInfo{ID: "claim"}}
}
func (c renewingCoordinator) Heartbeat(_ context.Context, caller model.CallerContext) model.ClaimResult {
	*c.renewed = append(*c.renewed, caller.Session)
	return model.ClaimResult{Outcome: "renewed"}
}

// Claims taken in the TUI must outlive their first lease: each under its own
// item's session, once per lease, none while a renewal is in flight, and never
// for a claim a run renews itself.
func TestHeldClaimsRenewOncePerLease(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	var renewed []string
	coordinator := renewingCoordinator{renewed: &renewed}
	claims := map[model.Ref]time.Time{{Source: "tasks", ID: "BL-1"}: now.Add(15 * time.Minute), {Source: "tasks", ID: "BL-2"}: now.Add(10 * time.Minute)}
	run := New(Options{Now: func() time.Time { return now }, Context: engine.WithCoordination(context.Background(), coordinator, model.CallerContext{Session: "tui", Run: "run"})})
	run.mineClaims = claims
	if run.renewCommand() != nil {
		t.Fatal("renewed a run-managed claim")
	}
	m := New(Options{Now: func() time.Time { return now }, Context: engine.WithCoordination(context.Background(), coordinator, model.CallerContext{Session: "tui", PerItem: true})})
	m.mineClaims = claims
	if m.renewCommand() == nil || m.renewCommand() != nil {
		t.Fatal("renewal was not scheduled exactly once")
	}
	next, cmd := m.Update(claimsRenew(now.Add(10 * time.Minute)))
	m = next.(Model)
	if cmd == nil || m.renewCommand() != nil {
		t.Fatal("renewal did not start, or another was scheduled while it ran")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	slices.Sort(renewed)
	if !slices.Equal(renewed, []string{"tui:tasks:BL-1", "tui:tasks:BL-2"}) || m.renewing {
		t.Fatalf("renewed = %v, renewing = %v", renewed, m.renewing)
	}
	m.mineClaims = map[model.Ref]time.Time{{Source: "tasks", ID: "BL-1"}: now.Add(16 * time.Minute)}
	if m.renewCommand() == nil {
		t.Fatal("renewed lease was not rescheduled")
	}
}
