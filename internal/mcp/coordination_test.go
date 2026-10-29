package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/itemjson"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type callCoordinator struct {
	claims                      map[string]*model.ClaimInfo
	released, renewed, acquired []string
}

func (*callCoordinator) Resource(i model.Item) (string, error) {
	return i.Ref.Source + ":" + i.Ref.ID, nil
}
func (c *callCoordinator) InspectSession(_ context.Context, caller model.CallerContext) model.SessionState {
	if claim := c.claims[caller.Session]; claim != nil {
		if claim.ID == "run-claim" {
			return model.SessionState{State: model.SessionUnresolved, Message: "supervisor requires explicit run context"}
		}
		return model.SessionState{State: "held", Claim: claim}
	}
	return model.SessionState{State: "absent"}
}
func (c *callCoordinator) Acquire(_ context.Context, caller model.CallerContext, item model.Item) model.ClaimResult {
	c.acquired = append(c.acquired, caller.Session)
	resource, _ := c.Resource(item)
	for _, claim := range c.claims {
		if slices.Contains(claim.Resources, resource) {
			return model.ClaimResult{Outcome: "contended", Reason: "contention", Message: "held elsewhere", Claim: claim}
		}
	}
	claim := &model.ClaimInfo{ID: caller.Session, Session: caller.Session, Resources: []string{resource}, ExpiresAt: time.Unix(1000, 0)}
	c.claims[caller.Session] = claim
	return model.ClaimResult{Outcome: "acquired", Claim: claim}
}
func (*callCoordinator) ResumeAcquire(context.Context, model.CallerContext, model.RecoveryRef) model.ClaimResult {
	return model.ClaimResult{Outcome: "uncertain", Reason: "unavailable"}
}
func (c *callCoordinator) Release(_ context.Context, caller model.CallerContext, _ model.Item, expectedClaimID string) model.ClaimResult {
	if claim := c.claims[caller.Session]; claim == nil || claim.ID != expectedClaimID {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: "claim identity changed"}
	}
	c.released = append(c.released, caller.Session)
	delete(c.claims, caller.Session)
	return model.ClaimResult{Outcome: "released"}
}
func (c *callCoordinator) Heartbeat(_ context.Context, caller model.CallerContext) model.ClaimResult {
	c.renewed = append(c.renewed, caller.Session)
	return model.ClaimResult{Outcome: "renewed", ExpiresAt: time.Unix(2000, 0)}
}
func (c *callCoordinator) History(context.Context, string) ([]model.ClaimEpoch, error) {
	return nil, nil
}
func (c *callCoordinator) List(context.Context) ([]model.ClaimInfo, error) {
	var out []model.ClaimInfo
	for _, claim := range c.claims {
		out = append(out, *claim)
	}
	return out, nil
}
func (c *callCoordinator) VerifyRun(_ context.Context, caller model.CallerContext, resource string) model.ClaimResult {
	claim := c.claims[caller.Session]
	if caller.Run != "verified-run" || caller.Session != "supervisor" || claim == nil || resource != "" && !slices.Contains(claim.Resources, resource) {
		return model.ClaimResult{Outcome: "unavailable", Reason: "run-mismatch", Message: "run mismatch"}
	}
	return model.ClaimResult{Outcome: "run-managed", Claim: claim, ExpiresAt: claim.ExpiresAt}
}

// One server must never borrow the previous request's ownership, its launching
// worker's environment, a run-name prefix, or an inherited explicit handle.
func TestInterleavedExplicitOwnership(t *testing.T) {
	testkit.Isolate(t)
	t.Setenv("WORKLEASE_RUN_ID", "verified-run")
	t.Setenv("WORKLEASE_SESSION_ID", "supervisor")
	t.Setenv("WORKLEASE_HANDLE", "supervisor-handle")
	t.Setenv("BL_SESSION", "supervisor")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "server-env")
	root := t.TempDir()
	items := filepath.Join(root, "items")
	if err := os.MkdirAll(items, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := project.Config{Settings: project.Settings{Coordination: project.CoordinationConfig{Worklease: &struct{}{}}, Sources: []project.SourceConfig{{ID: "tasks", Type: "native", Path: items, Prefix: "BL"}}, Statuses: []project.StatusConfig{{Key: "todo", Category: "todo"}, {Key: "doing", Category: "active"}, {Key: "done", Category: "done"}}}, Discovery: project.Discovery{ControlRoot: root, CommonDir: filepath.Join(root, ".git")}}
	cfg.Agents = map[string]project.AgentConfig{"claude": {Session: "CLAUDE_CODE_SESSION_ID"}}
	cfg.Paths.StateDir = t.TempDir()
	for _, id := range []string{"BL-1", "BL-2", "BL-3"} {
		data, err := format.Serialize(model.Item{Ref: model.Ref{ID: id}, Title: id, Status: "todo"})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(items, id+".md"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	coordinator := &callCoordinator{claims: map[string]*model.ClaimInfo{"supervisor": {ID: "run-claim", Session: "supervisor", Resources: []string{"tasks:BL-1"}, ExpiresAt: time.Unix(1000, 0)}}}
	server := New(cfg)
	server.coordinator = coordinator
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := sdk.NewInMemoryTransports()
	ss, err := server.server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "ownership", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args map[string]any, wantError bool) *sdk.CallToolResult {
		t.Helper()
		result, e := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if e != nil || result.IsError != wantError {
			t.Fatalf("%s %+v: %+v %v", name, args, result, e)
		}
		return result
	}
	update := func(id, status, session, run string, wantError bool) {
		t.Helper()
		call("update", map[string]any{"id": id, "fields": map[string]any{"status": status}, "session": session, "run": run}, wantError)
	}
	update("BL-1", "doing", "supervisor", "verified-run", false)
	update("BL-1", "done", "supervisor", "", true)
	call("release", map[string]any{"id": "BL-1", "session": "supervisor"}, true)
	call("heartbeat", map[string]any{"session": "supervisor"}, true)
	call("update", map[string]any{"id": "BL-2", "fields": map[string]any{"status": "doing"}, "session": "caller", "agent": "codex", "agentSession": "thread-1"}, false)
	call("heartbeat", map[string]any{"session": "supervisor", "run": "verified-run"}, false)
	heartbeat := call("heartbeat", map[string]any{"session": "caller"}, false)
	data, _ := json.Marshal(heartbeat.StructuredContent)
	var renewed model.ClaimResult
	_ = json.Unmarshal(data, &renewed)
	if renewed.ExpiresAt.IsZero() {
		t.Fatal("heartbeat omitted expiry")
	}
	before, err := os.ReadFile(filepath.Join(items, "BL-3.md"))
	if err != nil {
		t.Fatal(err)
	}
	update("BL-3", "done", "", "", true)
	update("BL-3", "done", "caller", "verified-run", true)
	update("BL-3", "doing", "third", "wrong-run", true)
	after, _ := os.ReadFile(filepath.Join(items, "BL-3.md"))
	if string(before) != string(after) || len(coordinator.released) != 0 {
		t.Fatal("missing/mismatched context had side effects")
	}
	update("BL-1", "done", "supervisor", "verified-run", false)
	update("BL-2", "done", "caller", "", false)
	if !slices.Equal(coordinator.released, []string{"caller"}) || !slices.Equal(coordinator.renewed, []string{"caller"}) || coordinator.claims["supervisor"] == nil || !slices.Equal(coordinator.acquired, []string{"caller"}) {
		t.Fatalf("ownership leaked: %+v", coordinator)
	}
	// The server's environment belongs to whoever launched it, not the caller.
	events, err := hooks.New(cfg.Paths.StateDir, "").Read(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var agents []string
	for _, e := range events {
		if e.Session != nil {
			agents = append(agents, e.Session.ID+":"+e.Session.Agent+"/"+e.Session.AgentSession)
		}
	}
	if !slices.Equal(agents, []string{"caller:codex/thread-1", "caller:/caller"}) {
		t.Fatalf("event agents: %v", agents)
	}
	shown := call("show", map[string]any{"id": "BL-2"}, false)
	data, _ = json.Marshal(shown.StructuredContent)
	var detail itemjson.Detail
	if err := json.Unmarshal(data, &detail); err != nil || len(detail.Sessions) != 1 || detail.Sessions[0].Agent != "codex" || detail.Sessions[0].Ended != "released" {
		t.Fatalf("show sessions: %s %v", data, err)
	}
}
