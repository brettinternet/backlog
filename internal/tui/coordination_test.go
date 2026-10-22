package tui

import (
	"context"
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
