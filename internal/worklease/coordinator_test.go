package worklease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

type step struct {
	Argv     []string       `json:"argv"`
	Response map[string]any `json:"response"`
}

func cliFixture(t *testing.T, steps []step) *Coordinator {
	t.Helper()
	testkit.Isolate(t)
	root := t.TempDir()
	bin := t.TempDir()
	script, err := os.ReadFile("testdata/cli.py")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "worklease"), script, 0700); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(t.TempDir(), "plan.json")
	data, _ := json.Marshal(map[string]any{"root": root, "steps": steps})
	if err = os.WriteFile(plan, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BL_FAKE_PLAN", plan)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WORKLEASE_SESSION_ID", "misleading")
	t.Setenv("WORKLEASE_HANDLE", "foreign-handle")
	t.Setenv("WORKLEASE_RUN_ID", "foreign-run")
	t.Cleanup(func() {
		failure, _ := os.ReadFile(strings.TrimSuffix(plan, ".json") + ".failure")
		index, _ := os.ReadFile(strings.TrimSuffix(plan, ".json") + ".index")
		if len(failure) > 0 || string(index) != fmt.Sprint(len(steps)) {
			t.Errorf("CLI fixture consumed %s/%d; failure %s", index, len(steps), failure)
		}
	})
	return New(project.Config{Settings: project.Settings{Project: project.ProjectConfig{ID: "test"}, Sources: []project.SourceConfig{{ID: "tasks", Type: "native"}}}, Discovery: project.Discovery{ControlRoot: root}})
}

// Startup diagnostics must distinguish process failures from malformed JSON.
func TestListExecutionErrors(t *testing.T) {
	testkit.Isolate(t)
	t.Setenv("PATH", t.TempDir())
	c := New(project.Config{})
	_, err := c.List(context.Background())
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing executable error lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.List(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error lost: %v", err)
	}
}

func successFixture(fields map[string]any) map[string]any {
	fields["schemaVersion"] = 2
	fields["ok"] = true
	return fields
}
func errorFixture(reason, message string) map[string]any {
	return map[string]any{"schemaVersion": 2, "ok": false, "error": map[string]any{"reason": reason, "message": message, "details": map[string]any{}}}
}
func inspectFixture(state string) map[string]any {
	return successFixture(map[string]any{
		"handlePath": "/private/retained.json", "selectorType": "contextual", "selectorProvenance": "resolved", "selectorSession": "loop", "authorityId": "authority", "selectedAuthorityId": "authority", "authorityMatchesSelection": true,
		"claimId": "claim", "recordedState": state, "resources": []string{"resource"}, "pendingRequestPresent": state == "pending", "pendingKind": "acquire", "pendingOperationId": "original-operation",
	})
}
func command(args ...string) []string {
	return append(append([]string{"--json"}, args...), "--session", "loop")
}

// Pinned handle inspection is offline only. Only status + credential verify can
// promote a ready handle to held. Corrupt/foreign/stale/ambiguous state is not empty.
func TestPinnedSessionInspection(t *testing.T) {
	for _, mode := range []string{"absent", "corrupt", "foreign", "stale", "uncertain", "pending", "held"} {
		t.Run(mode, func(t *testing.T) {
			inspect := inspectFixture("ready")
			steps := []step{}
			want := "unresolved"
			switch mode {
			case "absent":
				inspect = errorFixture("handle-malformed", "handle is missing")
				want = "absent"
			case "corrupt":
				inspect = errorFixture("handle-malformed", "handle is malformed")
			case "foreign":
				inspect["authorityMatchesSelection"] = false
			case "pending":
				inspect = inspectFixture("pending")
				want = "pending"
			case "held":
				want = "held"
			}
			steps = append(steps, step{command("handle", "inspect"), inspect})
			if mode == "stale" || mode == "uncertain" || mode == "held" {
				response := successFixture(map[string]any{"claim": map[string]any{"claimId": "claim", "authorityId": "authority", "resources": []string{"resource"}, "sessionId": "loop", "active": mode != "stale", "expiresAt": "2030-01-01T00:00:00Z"}})
				if mode == "uncertain" {
					response = errorFixture("authority-unavailable", "authority unavailable")
				}
				steps = append(steps, step{command("status"), response})
				if mode == "held" {
					steps = append(steps, step{command("verify"), response}, step{[]string{"--json", "runs", "--all"}, successFixture(map[string]any{"runs": []any{}})})
				}
			}
			c := cliFixture(t, steps)
			state := c.InspectSession(context.Background(), model.CallerContext{Session: "loop"})
			if string(state.State) != want {
				t.Fatalf("got %+v want %s", state, want)
			}
			if want == "pending" && (state.Recovery == nil || state.Claim.ID != "claim") {
				t.Fatalf("pending identity lost: %+v", state)
			}
		})
	}
}

// Recovery passes only an adapter-owned opaque reference. The bare-handle CLI
// form delegates identity, ttl, agent, workKey, wait and deadlines to Worklease.
func TestExactPendingReplay(t *testing.T) {
	steps := []step{{command("handle", "inspect"), inspectFixture("pending")}, {command("handle", "inspect"), inspectFixture("pending")}, {command("acquire", "--handle", "/private/retained.json"), successFixture(map[string]any{"claimId": "claim", "resources": []string{"resource"}, "expiresAt": "2030-01-01T00:00:00Z"})}}
	c := cliFixture(t, steps)
	caller := model.CallerContext{Session: "loop"}
	state := c.InspectSession(context.Background(), caller)
	result := c.ResumeAcquire(context.Background(), caller, state.Recovery)
	if result.Outcome != "acquired" || result.Claim.ID != "claim" {
		t.Fatalf("exact replay failed: %+v", result)
	}
}
func TestChangedPendingRefusesReplay(t *testing.T) {
	changed := inspectFixture("pending")
	changed["pendingOperationId"] = "changed"
	c := cliFixture(t, []step{{command("handle", "inspect"), inspectFixture("pending")}, {command("handle", "inspect"), changed}})
	caller := model.CallerContext{Session: "loop"}
	state := c.InspectSession(context.Background(), caller)
	result := c.ResumeAcquire(context.Background(), caller, state.Recovery)
	if result.Outcome != "uncertain" {
		t.Fatal(result)
	}
}
func TestPinnedRunVerificationAndHeartbeat(t *testing.T) {
	now := time.Unix(100, 0)
	expiry := now.Add(time.Minute).UTC().Format(time.RFC3339)
	claim := map[string]any{"claimId": "claim", "authorityId": "authority", "sessionId": "loop", "resources": []string{"resource"}, "expiresAt": expiry, "state": "held", "active": true}
	steps := []step{{[]string{"--json", "runs", "show", "run", "--lines", "0"}, successFixture(map[string]any{"run": map[string]any{"id": "run", "state": "running", "health": "idle", "claim": claim}})}, {command("status", "--claim-id", "claim"), successFixture(map[string]any{"claim": claim})}, {command("heartbeat"), successFixture(map[string]any{"receipt": map[string]any{"result": map[string]any{"expiresAt": expiry}}})}}
	c := cliFixture(t, steps)
	c.Now = func() time.Time { return now }
	result := c.VerifyRun(context.Background(), model.CallerContext{Session: "loop", Run: "run"}, "resource")
	if result.Outcome != "run-managed" {
		t.Fatal(result)
	}
	result = c.Heartbeat(context.Background(), model.CallerContext{Session: "loop"})
	if result.Outcome != "renewed" || !result.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatal(result)
	}
}

// The pinned lifecycle redaction bug must never become apparent known-free
// overlay state, inferred ownership, or authorization for a fresh acquire.
func TestPinnedRedactedMembershipFailsClosed(t *testing.T) {
	for _, mode := range []string{"inspect", "list", "acquire"} {
		t.Run(mode, func(t *testing.T) {
			response := inspectFixture("ready")
			response["resources"] = []string{"coordination:generic:[REDACTED]"}
			argv := command("handle", "inspect")
			if mode == "list" {
				argv = []string{"--json", "list"}
				response = successFixture(map[string]any{"claims": []any{map[string]any{"claimId": "claim", "resources": []string{"coordination:generic:[REDACTED]"}}}})
			}
			var item = model.Item{Ref: model.Ref{Source: "tasks", ID: "BL-1"}}
			if mode == "acquire" {
				key, _ := Key("generic", "test", "BL-1")
				argv = command("acquire", "--resource", key, "--wait", "0s", "--work-key", "test BL-1")
				response = successFixture(map[string]any{"claimId": "claim", "resources": []string{"coordination:generic:[REDACTED]"}})
			}
			c := cliFixture(t, []step{{argv, response}})
			switch mode {
			case "inspect":
				if state := c.InspectSession(context.Background(), model.CallerContext{Session: "loop"}); state.State != "unresolved" {
					t.Fatal(state)
				}
			case "list":
				if claims, err := c.List(context.Background()); err == nil || len(claims) != 0 {
					t.Fatalf("redaction became free: %+v %v", claims, err)
				}
			case "acquire":
				if r := c.Acquire(context.Background(), model.CallerContext{Session: "loop"}, item); r.Outcome != "uncertain" {
					t.Fatal(r)
				}
			}
		})
	}
}
func FuzzProviderEnvelope(f *testing.F) {
	for _, seed := range []string{`{"schemaVersion":2,"ok":true,"claims":[]}`, `{"schemaVersion":2,"ok":false,"error":{"reason":"already-claimed","details":{"holder":{"claimId":"other"}}}}`, `{"schemaVersion":2,"ok":true,"recordedState":"pending","pendingKind":"acquire"}`, `null`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := decodeEnvelope(data)
		if err == nil && v.SchemaVersion != 2 {
			t.Fatal("accepted foreign schema")
		}
	})
}

func TestPinnedRunMismatchRefusesMutation(t *testing.T) {
	for _, mode := range []string{"wrong-resource", "abandoned", "authority-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			claim := map[string]any{"claimId": "claim", "authorityId": "authority", "sessionId": "loop", "resources": []string{"resource"}, "expiresAt": "2030-01-01T00:00:00Z", "state": "held"}
			run := map[string]any{"id": "run", "state": "running", "health": "running", "claim": claim}
			if mode == "wrong-resource" {
				claim["resources"] = []string{"another-item"}
			}
			if mode == "abandoned" {
				run["health"] = "abandoned"
			}
			steps := []step{{[]string{"--json", "runs", "show", "run", "--lines", "0"}, successFixture(map[string]any{"run": run})}}
			if mode == "authority-unavailable" {
				steps = append(steps, step{command("status", "--claim-id", "claim"), errorFixture("authority-unavailable", "cannot verify")})
			}
			c := cliFixture(t, steps)
			result := c.VerifyRun(context.Background(), model.CallerContext{Session: "loop", Run: "run"}, "resource")
			if result.Reason != "run-mismatch" {
				t.Fatalf("unverified run authorized: %+v", result)
			}
		})
	}
}

func TestAcquireLostOrMalformedResponseIsUncertain(t *testing.T) {
	key, _ := Key("generic", "test", "BL-1")
	response := map[string]any{"schemaVersion": 99, "ok": true}
	c := cliFixture(t, []step{{command("acquire", "--resource", key, "--wait", "0s", "--work-key", "test BL-1"), response}})
	result := c.Acquire(context.Background(), model.CallerContext{Session: "loop"}, model.Item{Ref: model.Ref{Source: "tasks", ID: "BL-1"}})
	if result.Outcome != model.ClaimUncertain {
		t.Fatalf("unknown commit became definitive: %+v", result)
	}
}

func TestSessionAloneCannotMutateSupervisorClaim(t *testing.T) {
	claim := map[string]any{"claimId": "claim", "authorityId": "authority", "resources": []string{"resource"}, "sessionId": "loop", "active": true, "expiresAt": "2030-01-01T00:00:00Z", "state": "held"}
	status := successFixture(map[string]any{"claim": claim})
	// No naming convention or workKey marker: only exact public identities bind it.
	c := cliFixture(t, []step{{command("handle", "inspect"), inspectFixture("ready")}, {command("status"), status}, {command("verify"), status}, {[]string{"--json", "runs", "--all"}, successFixture(map[string]any{"runs": []any{map[string]any{"id": "supervisor", "state": "running", "health": "running", "claim": claim}}})}})
	state := c.InspectSession(context.Background(), model.CallerContext{Session: "loop"})
	if state.State != model.SessionUnresolved || !strings.Contains(state.Message, "explicit run context") {
		t.Fatalf("session downgraded run ownership: %+v", state)
	}
}

// Worklease rejects an acquire whose work key breaks its public-text limits,
// so a long or unprintable project ID must never fail the claim.
func TestWorkKeyStaysPublicText(t *testing.T) {
	long := strings.Repeat("é", 600)
	for _, tc := range []struct{ project, item, want string }{
		{"github.com/brettinternet/backlog", "BL-76", "github.com/brettinternet/backlog BL-76"},
		{"/srv/a\nb\u200f", "#12", "/srv/ab #12"},
		{long, "BL-1", strings.Repeat("é", 509) + " BL-1"},
		{"test", "\x01", "resource"},
	} {
		got := workKey(tc.project, tc.item, "resource")
		if got != tc.want || len(got) > 1024 || !utf8.ValidString(got) {
			t.Errorf("workKey(%q, %q) = %q (%d bytes), want %q", tc.project, tc.item, got, len(got), tc.want)
		}
	}
}
