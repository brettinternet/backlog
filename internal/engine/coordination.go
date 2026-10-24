package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

type coordinationKey struct{}
type coordinationContext struct {
	coordinator model.Coordinator
	caller      model.CallerContext
}

// WithCoordination binds a coordinator and explicit identity to one request.
func WithCoordination(ctx context.Context, c model.Coordinator, caller model.CallerContext) context.Context {
	return context.WithValue(ctx, coordinationKey{}, coordinationContext{c, caller})
}
func Coordination(ctx context.Context) (model.Coordinator, model.CallerContext) {
	v, _ := ctx.Value(coordinationKey{}).(coordinationContext)
	return v.coordinator, v.caller
}

// ClaimHistory reads an item's retained claims for display. A failure comes
// back as a message so the caller can still show the item.
func ClaimHistory(ctx context.Context, c model.Coordinator, resource string) ([]model.ClaimEpoch, string) {
	if c == nil || resource == "" {
		return nil, ""
	}
	epochs, err := c.History(ctx, resource)
	if err != nil {
		return nil, err.Error()
	}
	return epochs, ""
}

type TransitionResult struct {
	Claim  *model.ClaimResult `json:"claim,omitempty"`
	Status string             `json:"status"`
}
type transitionKey struct{}

func WithTransitionResult(ctx context.Context, result *TransitionResult) context.Context {
	return context.WithValue(ctx, transitionKey{}, result)
}
func recordTransition(ctx context.Context, result TransitionResult) {
	if out, ok := ctx.Value(transitionKey{}).(*TransitionResult); ok {
		*out = result
	}
}
func claimError(r model.ClaimResult) error {
	reason := r.Reason
	if reason == "" {
		reason = "unavailable"
	}
	return source.Refuse(reason, r.Message)
}
func RequireSession(c model.CallerContext) error {
	if strings.TrimSpace(c.Session) == "" {
		return source.Refuse("session-required", "coordination requires an explicit session")
	}
	if len(c.Session) > 1024 || strings.ContainsAny(c.Session, "\r\n\x00") {
		return source.Refuse("rejected", "invalid session")
	}
	return nil
}
func ClaimItem(ctx context.Context, item model.Item) model.ClaimResult {
	result, _ := claimItem(ctx, item)
	return result
}

// claimItem retains whether an inspection race resumed ownership rather than
// authorizing a fresh acquire. Public writes need only the claim outcome.
func claimItem(ctx context.Context, item model.Item) (result model.ClaimResult, resumed bool) {
	defer func() {
		if result.Outcome == "acquired" {
			recordClaimEvent(ctx, item, "claim.acquired", &result)
		}
	}()
	c, caller := Coordination(ctx)
	if c == nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: "coordination is not configured"}, false
	}
	if err := RequireSession(caller); err != nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: ErrorReason(err), Message: ErrorMessage(err)}, false
	}
	resource, err := c.Resource(item)
	if err != nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: err.Error()}, false
	}
	if caller.Run != "" {
		return c.VerifyRun(ctx, caller, resource), true
	}
	state := c.InspectSession(ctx, caller)
	switch state.State {
	case "absent":
		return c.Acquire(ctx, caller, item), false
	case "held":
		if state.Claim != nil && slices.Equal(state.Claim.Resources, []string{resource}) {
			return model.ClaimResult{Outcome: "held", Resource: resource, Claim: state.Claim, ExpiresAt: state.Claim.ExpiresAt}, true
		}
		held := "unknown"
		if state.Claim != nil {
			held = state.Claim.Item
			if held == "" {
				held = strings.Join(state.Claim.Resources, ", ")
			}
		}
		return model.ClaimResult{Outcome: "unavailable", Reason: "claim-held", Message: "session already holds " + held, Claim: state.Claim}, false
	case "pending":
		if state.Claim != nil && slices.Equal(state.Claim.Resources, []string{resource}) {
			return c.ResumeAcquire(ctx, caller, state.Recovery), true
		}
		return model.ClaimResult{Outcome: "uncertain", Reason: "claim-held", Message: "session has a pending claim on another item", Claim: state.Claim}, false
	default:
		return model.ClaimResult{Outcome: "uncertain", Reason: "unavailable", Message: state.Message}, false
	}
}
func ClaimSucceeded(r model.ClaimResult) bool {
	return slices.Contains([]model.ClaimOutcome{model.ClaimAcquired, model.ClaimHeld, model.ClaimRunManaged}, r.Outcome)
}
func ReleaseItem(ctx context.Context, item model.Item) (result model.ClaimResult) {
	defer func() {
		if result.Outcome == "released" {
			recordClaimEvent(ctx, item, "claim.released", &result)
		}
	}()
	c, caller := Coordination(ctx)
	if c == nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: "coordination is not configured"}
	}
	if err := RequireSession(caller); err != nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: ErrorReason(err), Message: ErrorMessage(err)}
	}
	resource, err := c.Resource(item)
	if err != nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: err.Error()}
	}
	if caller.Run != "" {
		return c.VerifyRun(ctx, caller, resource)
	}
	state := c.InspectSession(ctx, caller)
	if state.State == "absent" {
		return model.ClaimResult{Outcome: "not-held", Resource: resource}
	}
	if state.State != "held" || state.Claim == nil || state.Claim.ID == "" {
		return model.ClaimResult{Outcome: "uncertain", Reason: "unavailable", Message: "session ownership unresolved; no release attempted"}
	}
	if !slices.Equal(state.Claim.Resources, []string{resource}) {
		return model.ClaimResult{Outcome: "not-held", Resource: resource}
	}
	result = c.Release(ctx, caller, item, state.Claim.ID)
	result.Claim = state.Claim
	result.Resource = resource
	return result
}
func Heartbeat(ctx context.Context) model.ClaimResult {
	c, caller := Coordination(ctx)
	if c == nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: "coordination is not configured"}
	}
	if err := RequireSession(caller); err != nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: ErrorReason(err), Message: ErrorMessage(err)}
	}
	if caller.Run != "" {
		return c.VerifyRun(ctx, caller, "")
	}
	state := c.InspectSession(ctx, caller)
	if state.State != "held" {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: "session has no verified held claim"}
	}
	return c.Heartbeat(ctx, caller)
}

// StatusTarget recognizes all engine paths, including editor restores and undo.
func StatusTarget(cfg project.Config, m model.Mutation) (string, model.StatusCategory, bool) {
	status := ""
	found := false
	switch m.Op {
	case model.Batch:
		for _, change := range m.Changes {
			if s, _, ok := StatusTarget(cfg, change); ok {
				status, found = s, true
			}
		}
	case model.Set, model.Unset:
		if m.Field == "status" {
			status, _ = m.Value.(string)
			found = true
		}
	case model.Restore:
		if doc, err := format.Parse(m.Original); err == nil {
			status, found = doc.Item().Status, true
		}
	}
	for _, s := range cfg.Statuses {
		if s.Key == status {
			return status, model.StatusCategory(s.Category), found
		}
	}
	return status, "", found
}
func coordinatedWrite(ctx context.Context, cfg project.Config, m model.Mutation, read func() (model.Item, error), write func() (model.Item, error)) (model.Item, error) {
	target, category, changes := StatusTarget(cfg, m)
	if !cfg.Coordination.Enabled() || !changes {
		return write()
	}
	c, caller := Coordination(ctx)
	result := TransitionResult{Status: "not-written"}
	defer func() { recordTransition(ctx, result) }()
	if c == nil {
		return model.Item{}, source.Refuse("unavailable", "coordination is configured but unavailable")
	}
	if err := RequireSession(caller); err != nil {
		return model.Item{}, err
	}
	item, err := read()
	if err != nil {
		return model.Item{}, err
	}
	if transitions, ok := item.Fields["jiraTransitions"].(map[string]any); ok {
		choice, _ := transitions[target].(map[string]any)
		raw, _ := choice["category"].(string)
		category = model.StatusCategory(raw)
		if category != model.Todo && category != model.Active && category != model.Done {
			return model.Item{}, source.Refuse("rejected", "Jira transition unavailable")
		}
	}
	if caller.Run == "" && category != model.Active {
		state := c.InspectSession(ctx, caller)
		if state.State != "held" && state.State != "absent" {
			return model.Item{}, source.Refuse("unavailable", "session ownership unresolved; no status write attempted")
		}
	}
	if m.ExpectedVersion != "" && m.ExpectedVersion != item.Version {
		return model.Item{}, source.Refuse("conflict", "expected version does not match current content")
	}
	// Explicit run context must verify before ANY status write, even todo.
	if caller.Run != "" {
		resource, e := c.Resource(item)
		if e != nil {
			return model.Item{}, e
		}
		r := c.VerifyRun(ctx, caller, resource)
		result.Claim = &r
		if r.Outcome != "run-managed" {
			return model.Item{}, claimError(r)
		}
	}
	if caller.Run == "" && category == model.Active {
		r := ClaimItem(ctx, item)
		result.Claim = &r
		if !ClaimSucceeded(r) {
			return model.Item{}, claimError(r)
		}
	}
	updated, err := write()
	if err != nil {
		result.Status = "failed"
		return model.Item{}, err
	}
	result.Status = "written"
	if caller.Run == "" && (category == model.Done || category == model.Canceled) {
		r := ReleaseItem(ctx, item)
		result.Claim = &r
		if !slices.Contains([]model.ClaimOutcome{model.ClaimReleased, model.ClaimNotHeld, model.ClaimRunManaged}, r.Outcome) {
			return updated, claimError(r)
		}
	}
	return updated, nil
}

// ClaimStates builds a single bulk overlay for readiness and query projections.
func ClaimStates(ctx context.Context, items []model.Item, now time.Time) (map[model.Ref]Claim, map[model.Ref]string, error) {
	c, caller := Coordination(ctx)
	states := map[model.Ref]Claim{}
	labels := map[model.Ref]string{}
	if c == nil {
		return states, labels, fmt.Errorf("coordinator unavailable")
	}
	claims, err := c.List(ctx)
	if err != nil {
		return states, labels, err
	}
	states, labels = OverlayStates(c, caller, items, claims, now)
	return states, labels, nil
}
func OverlayStates(c model.Coordinator, caller model.CallerContext, items []model.Item, claims []model.ClaimInfo, now time.Time) (map[model.Ref]Claim, map[model.Ref]string) {
	states := map[model.Ref]Claim{}
	labels := map[model.Ref]string{}
	byResource := map[string]model.ClaimInfo{}
	for _, claim := range claims {
		for _, resource := range claim.Resources {
			byResource[resource] = claim
		}
	}
	for _, item := range items {
		resource, e := c.Resource(item)
		if e != nil {
			states[item.Ref] = ClaimUnknown
			continue
		}
		states[item.Ref] = ClaimNone
		if claim, ok := byResource[resource]; ok {
			labels[item.Ref] = claim.Label(caller.Session, now)
			if claim.ExpiresAt.After(now) {
				states[item.Ref] = ClaimOthers
				if caller.Session != "" && claim.Session == caller.Session {
					states[item.Ref] = ClaimMine
				}
			}
		}
	}
	return states, labels
}
