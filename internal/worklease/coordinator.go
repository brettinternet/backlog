package worklease

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

type Coordinator struct {
	Config project.Config
	Now    func() time.Time

	profile struct {
		sync.Mutex
		known, builtin bool
		name           string
	}
}

func New(cfg project.Config) *Coordinator                    { return &Coordinator{Config: cfg, Now: time.Now} }
func (c *Coordinator) Resource(i model.Item) (string, error) { return resource(c.Config, i) }

type envelope struct {
	SchemaVersion int  `json:"schemaVersion"`
	OK            bool `json:"ok"`
	Error         struct {
		Reason, Message string
		Details         struct {
			Holder      *wireClaim `json:"holder"`
			CommitState string     `json:"commitState"`
		}
	} `json:"error"`
	Claim  *wireClaim  `json:"claim"`
	Claims []wireClaim `json:"claims"`
	wireClaim
	Receipt struct {
		Result struct {
			ExpiresAt time.Time `json:"expiresAt"`
		}
	} `json:"receipt"`
	HandlePath                                     string `json:"handlePath"`
	SelectorType                                   string `json:"selectorType"`
	SelectorProvenance                             string `json:"selectorProvenance"`
	SelectorSession                                string `json:"selectorSession"`
	SelectedAuthorityID                            string `json:"selectedAuthorityId"`
	AuthorityID                                    string `json:"authorityId"`
	AuthorityMatchesSelection                      bool
	RecordedState, PendingKind, PendingOperationID string
	PendingRequestPresent, RecoveryRequestPresent  bool
	Run                                            wireRun   `json:"run"`
	Runs                                           []wireRun `json:"runs"`
	Profile                                        *struct {
		Name    string `json:"name"`
		Builtin bool   `json:"builtin"`
	} `json:"profile"`
}
type wireRun struct {
	ID, State, Health string
	EndedAt           *time.Time
	Claim             *struct {
		wireClaim
		State string
	}
}
type wireClaim struct {
	ClaimID     string    `json:"claimId"`
	Resources   []string  `json:"resources"`
	SessionID   string    `json:"sessionId"`
	AgentID     string    `json:"agentId"`
	WorkKey     string    `json:"workKey"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Active      bool      `json:"active"`
	AuthorityID string    `json:"authorityId"`
}

func visibleResources(resources []string) bool {
	if len(resources) == 0 {
		return false
	}
	for _, r := range resources {
		if r == "" || strings.Contains(r, "[REDACTED]") {
			return false
		}
	}
	return true
}
func (w wireClaim) info() *model.ClaimInfo {
	return &model.ClaimInfo{ID: w.ClaimID, Resources: w.Resources, Session: w.SessionID, Agent: w.AgentID, Item: w.WorkKey, ExpiresAt: w.ExpiresAt}
}

// boundedOutput refuses unbounded/untrusted provider frames.
type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, fmt.Errorf("worklease output exceeds 4 MiB")
	}
	return b.Buffer.Write(p)
}
func (c *Coordinator) call(ctx context.Context, caller model.CallerContext, args ...string) (envelope, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	argv := append([]string{"--json"}, args...)
	// runs/list/profile have no selector flag; all contextual commands select explicitly.
	if len(args) > 0 && args[0] != "runs" && args[0] != "list" && args[0] != "profile" {
		argv = append(argv, "--session", caller.Session)
	}
	cmd := exec.CommandContext(ctx, "worklease", argv...)
	cmd.Dir = c.Config.Discovery.ControlRoot
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if slices.Contains([]string{"WORKLEASE_SESSION_ID", "WORKLEASE_HANDLE", "WORKLEASE_RUN_ID"}, key) {
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return envelope{}, fmt.Errorf("worklease execution canceled: %w", ctx.Err())
	}
	v, decodeErr := decodeEnvelope(out.Bytes())
	if decodeErr != nil {
		if runErr != nil {
			return v, fmt.Errorf("worklease execution failed: %w", runErr)
		}
		return v, decodeErr
	}
	if runErr != nil && v.OK {
		return v, fmt.Errorf("worklease returned contradictory outcome")
	}
	return v, nil
}
func decodeEnvelope(data []byte) (envelope, error) {
	var v envelope
	if len(data) > 4<<20 {
		return v, fmt.Errorf("worklease output exceeds 4 MiB")
	}
	if err := json.Unmarshal(data, &v); err != nil || v.SchemaVersion != 2 {
		return v, fmt.Errorf("worklease unavailable or invalid schema-v2 response")
	}
	return v, nil
}
func failure(v envelope, err error) model.ClaimResult {
	r := model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: "Worklease unavailable"}
	if err != nil {
		return r
	}
	r.Message = v.Error.Message
	switch v.Error.Reason {
	case "already-claimed":
		r.Outcome = "contended"
		r.Reason = "contention"
		if v.Error.Details.Holder != nil {
			r.Claim = v.Error.Details.Holder.info()
			r.Message += " (holder " + r.Claim.Agent + ")"
		}
	case "handle-in-use":
		r.Reason = "claim-held"
	default:
		if v.Error.Details.CommitState == "unknown" || v.Error.Details.CommitState == "committed" {
			r.Outcome = "uncertain"
		}
	}
	return r
}

func mutationFailure(v envelope, err error) model.ClaimResult {
	if err != nil {
		return model.ClaimResult{Outcome: model.ClaimUncertain, Reason: "unavailable", Message: "Worklease mutation outcome unknown; retain this session and inspect its recovery state before retrying"}
	}
	return failure(v, nil)
}

type recoveryRef struct {
	owner                           *Coordinator
	session, path, claim, operation string
	resources                       []string
}

func (*recoveryRef) CoordinationRecovery() {}
func (c *Coordinator) InspectSession(ctx context.Context, caller model.CallerContext) model.SessionState {
	v, err := c.call(ctx, caller, "handle", "inspect")
	unresolved := func(message string) model.SessionState {
		return model.SessionState{State: "unresolved", Message: message}
	}
	if err != nil {
		return unresolved(err.Error())
	}
	if !v.OK {
		if v.Error.Reason == "handle-malformed" && v.Error.Message == "handle is missing" {
			return model.SessionState{State: "absent"}
		}
		return unresolved(v.Error.Message)
	}
	if v.SelectorType != "contextual" || v.SelectorProvenance != "resolved" || v.SelectorSession != caller.Session || !v.AuthorityMatchesSelection || v.AuthorityID == "" || v.AuthorityID != v.SelectedAuthorityID || v.RecoveryRequestPresent {
		return unresolved("unresolved or foreign handle; inspect with worklease handle inspect before recovery")
	}
	if (v.PendingRequestPresent || v.RecordedState == "pending") && !visibleResources(v.Resources) {
		return unresolved("Worklease redacted pending resource identity; exact recovery requires Worklease")
	}
	if v.PendingRequestPresent || v.RecordedState == "pending" {
		if v.PendingKind != "acquire" || v.PendingOperationID == "" || v.HandlePath == "" || v.ClaimID == "" || len(v.Resources) == 0 {
			return unresolved("pending lifecycle requires exact manual recovery")
		}
		return model.SessionState{State: "pending", Claim: v.wireClaim.info(), Recovery: &recoveryRef{c, caller.Session, v.HandlePath, v.ClaimID, v.PendingOperationID, slices.Clone(v.Resources)}}
	}
	if !visibleResources(v.Resources) {
		return unresolved("Worklease redacted resource identity; cannot prove membership; retain the handle and recover with Worklease")
	}
	if v.RecordedState != "ready" || v.ClaimID == "" {
		return unresolved("unrecognized handle state")
	}
	status, statusErr := c.call(ctx, caller, "status")
	if statusErr != nil || !status.OK || status.Claim == nil || status.Claim.ClaimID != v.ClaimID || status.Claim.AuthorityID != v.AuthorityID || status.Claim.SessionID != caller.Session || !slices.Equal(status.Claim.Resources, v.Resources) {
		return unresolved("cannot verify recorded claim at authority")
	}
	if !status.Claim.Active {
		return unresolved("inactive retained handle; explicit Worklease recovery required")
	}
	verified, verifyErr := c.call(ctx, caller, "verify")
	if verifyErr != nil || !verified.OK || verified.Claim == nil || verified.Claim.ClaimID != v.ClaimID || verified.Claim.AuthorityID != v.AuthorityID || verified.Claim.SessionID != caller.Session || !slices.Equal(verified.Claim.Resources, status.Claim.Resources) {
		return unresolved("claim ownership verification failed")
	}
	// A session by itself cannot downgrade a supervisor-owned claim to a
	// caller-managed one. Correlate public run identity, never naming prefixes.
	runs, runsErr := c.call(ctx, caller, "runs", "--all")
	if runsErr != nil || !runs.OK {
		return unresolved("cannot determine whether a supervisor owns this claim")
	}
	for _, run := range runs.Runs {
		if run.Claim != nil && (run.Claim.State == "held" || run.Claim.State == "kept") && run.Claim.ClaimID == status.Claim.ClaimID && run.Claim.AuthorityID == status.Claim.AuthorityID && run.Claim.SessionID == status.Claim.SessionID && slices.Equal(run.Claim.Resources, status.Claim.Resources) {
			return unresolved("supervisor owns this claim; provide the explicit run context before changing it")
		}
	}
	return model.SessionState{State: "held", Claim: status.Claim.info()}
}
func (c *Coordinator) ResumeAcquire(ctx context.Context, caller model.CallerContext, opaque model.RecoveryRef) model.ClaimResult {
	ref, ok := opaque.(*recoveryRef)
	if !ok || ref.owner != c || ref.session != caller.Session || caller.Run != "" {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: "invalid recovery reference"}
	}
	state := c.InspectSession(ctx, caller)
	current, ok := state.Recovery.(*recoveryRef)
	if !ok || state.State != "pending" || current.path != ref.path || current.claim != ref.claim || current.operation != ref.operation || !slices.Equal(current.resources, ref.resources) {
		return model.ClaimResult{Outcome: "uncertain", Reason: "unavailable", Message: "pending request changed; no fresh acquire attempted"}
	}
	// v1.9.3 remote acquire with ONLY --handle replays retained bytes/options.
	// Local Worklease rejects this form: do not reconstruct its private inputs.
	v, err := c.call(ctx, caller, "acquire", "--handle", ref.path)
	if err != nil || !v.OK {
		return mutationFailure(v, err)
	}
	if v.ClaimID != ref.claim || !slices.Equal(v.Resources, ref.resources) {
		return model.ClaimResult{Outcome: "uncertain", Reason: "unavailable", Message: "replay identity mismatch"}
	}
	return model.ClaimResult{Outcome: "acquired", Claim: v.wireClaim.info(), ExpiresAt: v.ExpiresAt}
}
func (c *Coordinator) Acquire(ctx context.Context, caller model.CallerContext, item model.Item) model.ClaimResult {
	key, err := c.Resource(item)
	if err != nil {
		return failure(envelope{}, err)
	}
	if refused := c.refuseHostLocal(ctx, item); refused != nil {
		return *refused
	}
	v, err := c.call(ctx, caller, "acquire", "--resource", key, "--wait", "0s", "--work-key", workKey(c.Config.Project.ID, item.Ref.ID, key))
	if err != nil || !v.OK {
		return mutationFailure(v, err)
	}
	if !visibleResources(v.Resources) {
		return model.ClaimResult{Outcome: "uncertain", Reason: "unavailable", Message: "claim may be acquired but Worklease redacted its resource identity; claim retained; inspect the same session with Worklease before recovery"}
	}
	if v.ClaimID == "" || !slices.Equal(v.Resources, []string{key}) {
		return model.ClaimResult{Outcome: "uncertain", Reason: "unavailable", Message: "invalid acquire grant"}
	}
	return model.ClaimResult{Outcome: "acquired", Resource: key, Claim: v.wireClaim.info(), ExpiresAt: v.ExpiresAt}
}

// workKey labels a claim for people with the project ID and the item's
// display ID. bl never matches claims on it. It keeps Worklease's public-text
// limits and drops what a remote authority would redact, cutting the project
// ID first; the resource key stands in if nothing printable remains.
func workKey(projectID, itemID, resource string) string {
	printable := func(s string) string {
		return strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
				return -1
			}
			return r
		}, strings.ToValidUTF8(s, "")))
	}
	truncate := func(s string, n int) string {
		for len(s) > max(n, 0) {
			_, size := utf8.DecodeLastRuneInString(s)
			s = s[:len(s)-size]
		}
		return s
	}
	label := truncate(printable(itemID), 1024)
	if label == "" {
		return resource
	}
	if prefix := strings.TrimSpace(truncate(printable(projectID), 1023-len(label))); prefix != "" {
		label = prefix + " " + label
	}
	return label
}

// refuseHostLocal stops a host-local project key from reaching a remote
// authority, where other hosts derive different keys and never contend.
func (c *Coordinator) refuseHostLocal(ctx context.Context, item model.Item) *model.ClaimResult {
	if !projectScoped(c.Config, item) {
		return nil
	}
	if message := c.HostLocalConflict(ctx); message != "" {
		return &model.ClaimResult{Outcome: model.ClaimUnavailable, Reason: "unavailable", Message: message}
	}
	return nil
}

// HostLocalConflict explains why acquiring must fail, or returns "" when it
// may proceed. Only host-local project IDs ask Worklease for the selected
// profile, once per process; an unreadable answer counts as remote.
func (c *Coordinator) HostLocalConflict(ctx context.Context) string {
	if !c.Config.HostLocal {
		return ""
	}
	c.profile.Lock()
	defer c.profile.Unlock()
	if !c.profile.known {
		v, err := c.call(ctx, model.CallerContext{}, "profile", "show")
		if err != nil || !v.OK || v.Profile == nil {
			return "cannot determine whether the selected Worklease authority is remote, and project ID " + c.Config.Project.ID + " is host-local; set project.id or an origin remote"
		}
		c.profile.known, c.profile.name, c.profile.builtin = true, v.Profile.Name, v.Profile.Builtin
	}
	if c.profile.builtin {
		return ""
	}
	return "project ID " + c.Config.Project.ID + " is host-local, so claims through remote Worklease profile " + c.profile.name + " would not contend with other hosts; set project.id or an origin remote"
}

func (c *Coordinator) Release(ctx context.Context, caller model.CallerContext, item model.Item, expectedClaimID string) model.ClaimResult {
	if expectedClaimID == "" {
		return model.ClaimResult{Outcome: "unavailable", Reason: "unavailable", Message: "verified claim identity required; no release attempted"}
	}
	v, err := c.call(ctx, caller, "release", "--reason", "done", "--if-claim-id", expectedClaimID)
	if err != nil || !v.OK {
		return mutationFailure(v, err)
	}
	return model.ClaimResult{Outcome: "released"}
}
func (c *Coordinator) Heartbeat(ctx context.Context, caller model.CallerContext) model.ClaimResult {
	v, err := c.call(ctx, caller, "heartbeat")
	if err != nil || !v.OK {
		return mutationFailure(v, err)
	}
	if v.Receipt.Result.ExpiresAt.IsZero() {
		return model.ClaimResult{Outcome: "uncertain", Reason: "unavailable", Message: "heartbeat expiry missing"}
	}
	return model.ClaimResult{Outcome: "renewed", ExpiresAt: v.Receipt.Result.ExpiresAt}
}
func (c *Coordinator) List(ctx context.Context) ([]model.ClaimInfo, error) {
	v, err := c.call(ctx, model.CallerContext{}, "list")
	if err != nil {
		return nil, err
	}
	if !v.OK {
		return nil, fmt.Errorf("worklease list unavailable")
	}
	claims := make([]model.ClaimInfo, 0, len(v.Claims))
	for _, w := range v.Claims {
		if !visibleResources(w.Resources) {
			return nil, fmt.Errorf("worklease redacted resource identities; claim overlay is unknown")
		}
		claims = append(claims, *w.info())
	}
	return claims, nil
}
func (c *Coordinator) VerifyRun(ctx context.Context, caller model.CallerContext, resource string) model.ClaimResult {
	mismatch := model.ClaimResult{Outcome: "unavailable", Reason: "run-mismatch", Message: "run does not verify a live held claim for this session and resource"}
	if caller.Session == "" || caller.Run == "" {
		return mismatch
	}
	v, err := c.call(ctx, caller, "runs", "show", caller.Run, "--lines", "0")
	if err != nil || !v.OK || v.Run.ID != caller.Run || v.Run.State != "running" || !slices.Contains([]string{"running", "idle"}, v.Run.Health) || v.Run.EndedAt != nil || v.Run.Claim == nil {
		return mismatch
	}
	claim := v.Run.Claim
	if !visibleResources(claim.Resources) {
		return mismatch
	}
	if claim.State != "held" || claim.SessionID != caller.Session || claim.ClaimID == "" || claim.AuthorityID == "" || !claim.ExpiresAt.After(c.Now()) || len(claim.Resources) != 1 || resource != "" && !slices.Contains(claim.Resources, resource) {
		return mismatch
	}
	// Verify against the selected authority, not just the convenience run record.
	status, statusErr := c.call(ctx, caller, "status", "--claim-id", claim.ClaimID)
	if statusErr != nil || !status.OK || status.Claim == nil || !status.Claim.Active || status.Claim.AuthorityID != claim.AuthorityID || status.Claim.SessionID != caller.Session || !slices.Equal(status.Claim.Resources, claim.Resources) {
		return mismatch
	}
	return model.ClaimResult{Outcome: "run-managed", Claim: status.Claim.info(), ExpiresAt: status.Claim.ExpiresAt}
}
