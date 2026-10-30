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

// claimsUnsupported is non-empty when c cannot claim on this platform, as
// Worklease cannot on Windows.
func claimsUnsupported(c model.Coordinator) string {
	if u, ok := c.(interface{ Unsupported() string }); ok {
		return u.Unsupported()
	}
	return ""
}

func unsupportedClaim(message string) model.ClaimResult {
	return model.ClaimResult{Outcome: model.ClaimUnavailable, Reason: "unavailable", Message: message}
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
	if message := claimsUnsupported(c); message != "" {
		return unsupportedClaim(message), false
	}
	if err := RequireSession(caller); err != nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: ErrorReason(err), Message: ErrorMessage(err)}, false
	}
	resource, err := c.Resource(item)
	if err != nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: err.Error()}, false
	}
	caller = caller.For(resource)
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
	if message := claimsUnsupported(c); message != "" {
		return unsupportedClaim(message)
	}
	if err := RequireSession(caller); err != nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: ErrorReason(err), Message: ErrorMessage(err)}
	}
	resource, err := c.Resource(item)
	if err != nil {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: err.Error()}
	}
	caller = caller.For(resource)
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
	if message := claimsUnsupported(c); message != "" {
		return unsupportedClaim(message)
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

// ClaimKey names a held claim: its session, and its project in a workspace.
type ClaimKey struct{ Project, Session string }

// HeartbeatClaim renews a held claim through its own project's coordinator.
func HeartbeatClaim(ctx context.Context, cfg *project.Config, key ClaimKey) model.ClaimResult {
	if cfg != nil && cfg.Workspace != nil {
		for _, p := range cfg.Workspace.Projects {
			if p.Project.ID == key.Project {
				ctx = ProjectContext(ctx, p)
			}
		}
	}
	c, _ := Coordination(ctx)
	return Heartbeat(WithCoordination(ctx, c, model.CallerContext{Session: key.Session}))
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
		if s.Key == status || slices.Contains(s.Includes, status) {
			return status, model.StatusCategory(s.Category), found
		}
	}
	return status, "", found
}

// statusChoices lists the statuses item's source accepts: the provider's own,
// else the configured statuses, narrowed by sources[].statuses (D35).
func statusChoices(ctx context.Context, cfg project.Config, backend source.Source, item model.Item) ([]model.StatusChoice, error) {
	if ws, ok := backend.(*workspaceSource); ok {
		item.Ref.Source = ws.local
		return statusChoices(ctx, ws.cfg, ws.Source, item)
	}
	if chooser, ok := backend.(source.StatusChooser); ok {
		choices, ok, err := chooser.StatusChoices(ctx, item)
		if err != nil || ok {
			return choices, err
		}
	}
	narrowed := sourceStatuses(cfg, item.Ref.Source)
	choices := []model.StatusChoice{}
	for _, status := range cfg.Statuses {
		if len(narrowed) == 0 || slices.Contains(narrowed, status.Key) {
			choices = append(choices, model.StatusChoice{Key: status.Key, Category: model.StatusCategory(status.Category)})
		}
	}
	return choices, nil
}

// sourceStatuses is sources[].statuses for sourceID, empty when unset.
func sourceStatuses(cfg project.Config, sourceID string) []string {
	for _, c := range cfg.Sources {
		if c.ID == sourceID {
			return c.Statuses
		}
	}
	return nil
}

// entryChoices returns the keys among status entry key and its includes that
// choices accept; a key that isn't an entry stands for itself (D36).
func entryChoices(cfg project.Config, choices []model.StatusChoice, key string) []string {
	members := cfg.StatusMembers(key)
	if members == nil {
		members = []string{key}
	}
	var accepted []string
	for _, choice := range choices {
		if slices.Contains(members, choice.Key) && !slices.Contains(accepted, choice.Key) {
			accepted = append(accepted, choice.Key)
		}
	}
	return accepted
}

// resolveStatusWrite replaces a status entry written to an item with the one
// key the entry covers that the item's source accepts, else refuses naming
// the source (D36). Statuses that aren't entries with includes pass through.
func resolveStatusWrite(ctx context.Context, cfg project.Config, backend source.Source, m model.Mutation) (model.Mutation, error) {
	if _, ok := backend.(*workspaceSource); ok {
		return m, nil // The project write resolves with its own statuses.
	}
	var choices []model.StatusChoice
	resolve := func(key string, item func() (model.Item, error)) (string, error) {
		members := cfg.StatusMembers(key)
		if len(members) < 2 {
			return key, nil
		}
		if choices == nil {
			i, err := item()
			if err != nil {
				return "", err
			}
			if choices, err = statusChoices(ctx, cfg, backend, i); err != nil {
				return "", err
			}
		}
		switch accepted := entryChoices(cfg, choices, key); len(accepted) {
		case 1:
			return accepted[0], nil
		case 0:
			return "", source.Refuse("rejected", fmt.Sprintf("source %s accepts none of status %s (%s)", m.Ref.Source, key, strings.Join(members, ", ")))
		default:
			return "", source.Refuse("rejected", fmt.Sprintf("source %s accepts several of status %s (%s); set one of them", m.Ref.Source, key, strings.Join(accepted, ", ")))
		}
	}
	if m.Op == model.Create && m.Item != nil {
		created := *m.Item
		created.Ref.Source = m.Ref.Source
		status, err := resolve(created.Status, func() (model.Item, error) { return created, nil })
		created.Status = status
		m.Item = &created
		return m, err
	}
	read := func() (model.Item, error) { return backend.Read(ctx, m.Ref.ID) }
	changes := []*model.Mutation{&m}
	if m.Op == model.Batch {
		m.Changes = slices.Clone(m.Changes)
		changes = changes[:0]
		for n := range m.Changes {
			changes = append(changes, &m.Changes[n])
		}
	}
	for _, change := range changes {
		if value, ok := change.Value.(string); ok && change.Op == model.Set && change.Field == "status" {
			resolved, err := resolve(value, read)
			if err != nil {
				return m, err
			}
			change.Value = resolved
		}
	}
	return m, nil
}

// WorkflowStatus resolves workflow.start (category active) or workflow.done
// (category done) for item's source, for every client (D35): the workflow key
// when the source accepts it in that category, else the source's only status
// in the category, else the source's default. Anything else is refused,
// naming the source.
func WorkflowStatus(ctx context.Context, cfg project.Config, item model.Item, category model.StatusCategory) (string, error) {
	if cfg.Workspace != nil {
		p, local := cfg.SourceProject(item.Ref.Source)
		if p.Workspace != nil {
			return "", source.Refuse("unavailable", "unknown workspace source "+item.Ref.Source)
		}
		item.Ref.Source = local
		return WorkflowStatus(ctx, p, item, category)
	}
	backend, err := writeBackend(ctx, cfg, item.Ref.Source)
	if err != nil {
		return "", err
	}
	return workflowStatus(ctx, cfg, backend, item, category)
}

func workflowStatus(ctx context.Context, cfg project.Config, backend source.Source, item model.Item, category model.StatusCategory) (string, error) {
	if ws, ok := backend.(*workspaceSource); ok {
		item.Ref.Source = ws.local
		return workflowStatus(ctx, ws.cfg, ws.Source, item, category)
	}
	choices, err := statusChoices(ctx, cfg, backend, item)
	if err != nil {
		return "", err
	}
	key, name := cfg.Workflow.Start, "start"
	if category == model.Done {
		key, name = cfg.Workflow.Done, "done"
	}
	inCategory := []model.StatusChoice{}
	for _, choice := range choices {
		if choice.Category == category {
			inCategory = append(inCategory, choice)
		}
	}
	switch matched := entryChoices(cfg, inCategory, key); len(matched) {
	case 1:
		return matched[0], nil
	case 0:
	default:
		return "", source.Refuse("rejected", fmt.Sprintf("source %s accepts several statuses of workflow.%s %s (%s); keep one", item.Ref.Source, name, key, strings.Join(matched, ", ")))
	}
	candidates, preferred := []string{}, ""
	for _, choice := range inCategory {
		if choice.Default {
			preferred = choice.Key
		}
		candidates = append(candidates, choice.Key)
	}
	switch {
	case len(candidates) == 1:
		return candidates[0], nil
	case preferred != "":
		return preferred, nil
	case len(candidates) == 0:
		return "", source.Refuse("rejected", fmt.Sprintf("source %s accepts no %s status for workflow.%s", item.Ref.Source, category, name))
	}
	return "", source.Refuse("rejected", fmt.Sprintf("source %s accepts several %s statuses (%s) but not workflow.%s; set workflow.%s to one of them", item.Ref.Source, category, strings.Join(candidates, ", "), name, name))
}

func coordinatedWrite(ctx context.Context, cfg project.Config, backend source.Source, m model.Mutation) (model.Item, error) {
	read := func() (model.Item, error) { return backend.Read(ctx, m.Ref.ID) }
	write := func() (model.Item, error) { return backend.Apply(ctx, m) }
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
	unsupported := claimsUnsupported(c)
	if err := RequireSession(caller); err != nil && unsupported == "" {
		return model.Item{}, err
	}
	item, err := read()
	if err != nil {
		return model.Item{}, err
	}
	if caller.PerItem {
		resource, err := c.Resource(item)
		if err != nil {
			return model.Item{}, err
		}
		caller = caller.For(resource)
	}
	// A provider's own categories gate claims, even when project statuses omit
	// or misclassify the key, and a key it doesn't accept refuses before any claim.
	if chooser, ok := backend.(source.StatusChooser); ok {
		choices, ok, err := chooser.StatusChoices(ctx, item)
		if err != nil {
			return model.Item{}, err
		}
		if ok {
			index := slices.IndexFunc(choices, func(choice model.StatusChoice) bool { return choice.Key == target })
			if index < 0 {
				return model.Item{}, source.Refuse("rejected", fmt.Sprintf("source %s does not accept status %s", item.Ref.Source, target))
			}
			category = choices[index].Category
		}
	}
	if unsupported == "" && caller.Run == "" && category != model.Active {
		state := c.InspectSession(ctx, caller)
		if state.State != "held" && state.State != "absent" {
			return model.Item{}, source.Refuse("unavailable", "session ownership unresolved; no status write attempted")
		}
	}
	if m.ExpectedVersion != "" && m.ExpectedVersion != item.Version {
		return model.Item{}, source.Refuse("conflict", "expected version does not match current content")
	}
	// Without claims, only an active transition needs one; others write unchecked.
	if unsupported != "" {
		if caller.Run != "" || category == model.Active {
			result.Claim = new(unsupportedClaim(unsupported))
			return model.Item{}, claimError(*result.Claim)
		}
		updated, err := write()
		if err != nil {
			result.Status = "failed"
			return model.Item{}, err
		}
		result.Status = "written"
		return updated, nil
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
	states, matched := ClaimOverlay(c, caller, items, claims, now)
	labels := make(map[model.Ref]string, len(matched))
	for ref, claim := range matched {
		labels[ref] = claimLabel(states[ref], claim, now)
	}
	return states, labels
}

// claimLabel names a claim's holder once ClaimOverlay has decided it is mine.
func claimLabel(state Claim, claim model.ClaimInfo, now time.Time) string {
	if state == ClaimMine {
		return "mine"
	}
	return claim.Label("", now)
}

// ClaimOverlay also returns each item's claim, active or expired.
func ClaimOverlay(c model.Coordinator, caller model.CallerContext, items []model.Item, claims []model.ClaimInfo, now time.Time) (map[model.Ref]Claim, map[model.Ref]model.ClaimInfo) {
	states := map[model.Ref]Claim{}
	matched := map[model.Ref]model.ClaimInfo{}
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
			matched[item.Ref] = claim
			if claim.ExpiresAt.After(now) {
				states[item.Ref] = ClaimOthers
				if session := caller.For(resource).Session; session != "" && claim.Session == session {
					states[item.Ref] = ClaimMine
				}
			}
		}
	}
	return states, matched
}
