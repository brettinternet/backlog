package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"context"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/worklease"
)

func enableCoordination(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "backlog.yaml")
	put(t, path, get(t, path)+"coordination: {worklease: {}}\n")
	t.Setenv("BL_SESSION", "loop")
}

// The fake CLI changes the source AFTER acquire, simulating a concurrent editor.
// A failed version-bound status write must retain the separately committed claim.
func TestClaimThenFailedStatusKeepsOwnership(t *testing.T) {
	root := writeFixture(t)
	enableCoordination(t, root)
	script := `#!/bin/sh
set -eu
case "$2" in
handle)
 printf '%s\n' '{"schemaVersion":2,"ok":false,"error":{"reason":"handle-malformed","message":"handle is missing"}}'
 exit 2;;
acquire)
 printf '\nConcurrent editor\n' >> "$BL_FAKE_ITEM"
 printf '%s\n' "$4" > "$BL_FAKE_CLAIM"
 printf '{"schemaVersion":2,"ok":true,"claimId":"claim","resources":["%s"],"sessionId":"loop","expiresAt":"2030-01-01T00:00:00Z"}\n' "$4";;
*) exit 1;;
esac
`
	bin := t.TempDir()
	path := filepath.Join(bin, "worklease")
	put(t, path, script)
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "claim")
	t.Setenv("BL_FAKE_ITEM", filepath.Join(root, "items/BL-012.md"))
	t.Setenv("BL_FAKE_CLAIM", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, out, _ := writeRun(root, "", "start", "12")
	if code != 4 || !strings.Contains(out, `"outcome":"acquired"`) || !strings.Contains(out, `"status":"failed"`) {
		t.Fatalf("lost separate outcomes: %d %s", code, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("acquisition not retained", err)
	}
	if strings.Contains(get(t, filepath.Join(root, "items/BL-012.md")), "status: doing") {
		t.Fatal("failed transition wrote status")
	}
}
func TestClaimContentionAndShowResource(t *testing.T) {
	root := writeFixture(t)
	enableCoordination(t, root)
	script := `#!/bin/sh
set -eu
case "$2" in
handle) printf '%s\n' '{"schemaVersion":2,"ok":false,"error":{"reason":"handle-malformed","message":"handle is missing"}}';exit 2;;
acquire) printf '%s\n' '{"schemaVersion":2,"ok":false,"error":{"reason":"already-claimed","message":"resource claimed","details":{"holder":{"claimId":"other","agentId":"agent@host"}}}}';exit 2;;
*) exit 1;;
esac
`
	bin := t.TempDir()
	path := filepath.Join(bin, "worklease")
	put(t, path, script)
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, out, _ := writeRun(root, "", "show", "12")
	var shown struct{ Data struct{ Resource string } }
	if err := json.Unmarshal([]byte(out), &shown); err != nil || code != 0 || !strings.HasPrefix(shown.Data.Resource, "coordination:generic:") {
		t.Fatalf("show resource: %d %s %v", code, out, err)
	}
	before := get(t, filepath.Join(root, "items/BL-012.md"))
	for _, args := range [][]string{{"claim", "12"}, {"start", "12"}, {"set", "12", "status=doing"}} {
		code, out, _ = writeRun(root, "", args...)
		if code != 7 || !strings.Contains(out, "agent@host") {
			t.Fatalf("%v contention: %d %s", args, code, out)
		}
	}
	if get(t, filepath.Join(root, "items/BL-012.md")) != before {
		t.Fatal("contention wrote item")
	}
	code, out, _ = writeRun(root, "", "set", "12", "13", "status=doing")
	if code != 2 {
		t.Fatalf("multi activation: %d %s", code, out)
	}
}

// The real-provider test uses only an isolated authority/home and disposable
// project; linked metadata is enough to exercise bl's no-git control-root lookup.
func TestProviderClaimRoundTripAcrossWorktree(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 for Worklease 1.10.1")
	}
	providerBinary := os.Getenv("BL_PROVIDER_WORKLEASE")
	root := writeFixture(t)
	enableCoordination(t, root)
	selectProviderBinary(t, providerBinary)
	assertProviderBuild(t)
	var output []byte
	var err error
	// Explicit context must ignore inherited run/handle selectors in every child.
	t.Setenv("WORKLEASE_HANDLE", "/not/the/contextual/handle")
	code, out, stderr := writeRun(root, "", "start", "12")
	if code != 0 {
		cmd := exec.Command("worklease", "--json", "status", "--session", "loop")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "WORKLEASE_HANDLE=")
		diagnostic, _ := cmd.CombinedOutput()
		t.Fatalf("start: %d %s %s; provider status: %s", code, out, stderr, diagnostic)
	}
	var started struct {
		Data struct {
			Outcomes []struct {
				Transition struct{ Claim struct{ Resource string } }
			}
		}
	}
	if err = json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatal(err)
	}
	key := started.Data.Outcomes[0].Transition.Claim.Resource
	code, out, _ = writeRun(root, "", "show", "12")
	if code != 0 || !strings.Contains(out, key) {
		t.Fatalf("show key mismatch: %s", out)
	}
	cfgPaths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Resolve(cfgPaths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("worklease", "--json", "list")
	cmd.Dir = root
	output, err = cmd.Output()
	if err != nil || !strings.Contains(string(output), key) {
		t.Fatalf("list: %s %v", output, err)
	}
	// Synthetic linked Git metadata, as resolved by project.Discover.
	linked := t.TempDir()
	admin := filepath.Join(root, ".git/worktrees/linked")
	if err = os.MkdirAll(admin, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(linked, ".git"), "gitdir: "+admin+"\n")
	put(t, filepath.Join(admin, "commondir"), "../..\n")
	put(t, filepath.Join(admin, "gitdir"), filepath.Join(linked, ".git")+"\n")
	put(t, filepath.Join(admin, "HEAD"), "ref: refs/heads/linked\n")
	discovered, err := project.Discover(linked)
	if err != nil || discovered.ControlRoot != cfg.Discovery.ControlRoot {
		t.Fatalf("linked discovery: %+v %v", discovered, err)
	}
	code, out, stderr = writeRun(linked, "", "heartbeat")
	if code != 0 || !strings.Contains(out, `"expiresAt"`) {
		t.Fatalf("heartbeat: %d %s %s", code, out, stderr)
	}
	code, out, stderr = writeRun(linked, "", "done", "12")
	if code != 0 || !strings.Contains(out, `"outcome":"released"`) {
		t.Fatalf("done: %d %s %s", code, out, stderr)
	}
	cmd = exec.Command("worklease", "--json", "list")
	cmd.Dir = root
	output, err = cmd.Output()
	if err != nil || strings.Contains(string(output), key) {
		t.Fatalf("claim survived done: %s %v", output, err)
	}
}

func linkedCoordinationFixture(t *testing.T, root string) string {
	t.Helper()
	linked := t.TempDir()
	admin := filepath.Join(root, ".git/worktrees/linked")
	if err := os.MkdirAll(admin, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(linked, ".git"), "gitdir: "+admin+"\n")
	put(t, filepath.Join(admin, "commondir"), "../..\n")
	put(t, filepath.Join(admin, "gitdir"), filepath.Join(linked, ".git")+"\n")
	put(t, filepath.Join(admin, "HEAD"), "ref: refs/heads/linked\n")
	return linked
}

// Worklease's actual handle root is checkout-scoped. This fake asserts cwd and
// stripped selectors on every call while bl resolves linked-worktree metadata.
func TestClaimHeartbeatDoneUseControlRoot(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "released", true: "replacement-retained"}[replace], func(t *testing.T) {
			testClaimHeartbeatDoneUseControlRoot(t, replace)
		})
	}
}

// A handle can switch items after verification but before release locks it.
// The original claim ID must reach the provider, retaining the replacement
// without hiding the committed status or emitting a false release event.
func testClaimHeartbeatDoneUseControlRoot(t *testing.T, replace bool) {
	root := writeFixture(t)
	enableCoordination(t, root)
	linked := linkedCoordinationFixture(t, root)
	bin := t.TempDir()
	script := get(t, "testdata/worklease.py")
	path := filepath.Join(bin, "worklease")
	put(t, path, script)
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "claim.json")
	t.Setenv("BL_FAKE_ROOT", root)
	t.Setenv("BL_FAKE_STATE", state)
	if replace {
		t.Setenv("BL_FAKE_REPLACE_ON_RELEASE", "1")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WORKLEASE_HANDLE", "foreign")
	t.Setenv("WORKLEASE_SESSION_ID", "foreign")
	for _, operation := range []struct {
		root string
		args []string
		want string
	}{{root, []string{"claim", "12"}, `"outcome":"acquired"`}, {linked, []string{"heartbeat"}, `"expiresAt":"2030-01-01T00:00:00Z"`}, {linked, []string{"done", "12"}, `"outcome":"released"`}} {
		code, out, stderr := writeRun(operation.root, "", operation.args...)
		if replace && operation.args[0] == "done" {
			if code != 6 || !strings.Contains(out, `"status":"written"`) || !strings.Contains(out, `"outcome":"unavailable"`) || !strings.Contains(out, `"claimId":"claim"`) {
				t.Fatalf("replacement release hid outcomes: %d %s %s", code, out, stderr)
			}
		} else if code != 0 || !strings.Contains(out, operation.want) {
			t.Fatalf("%v: %d %s %s", operation.args, code, out, stderr)
		}
	}
	var final struct {
		Active    bool
		ClaimID   string
		Resources []string
	}
	if err := json.Unmarshal([]byte(get(t, state)), &final); err != nil || final.Active != replace || replace && (final.ClaimID != "replacement" || final.Resources[0] != "other-resource") {
		t.Fatalf("claim not released: %+v %v", final, err)
	}
	if !strings.Contains(get(t, filepath.Join(root, "items/BL-012.md")), "status: done") {
		t.Fatal("done did not write shared item")
	}
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	events, err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	acquired, released := 0, 0
	for _, event := range events {
		if event.Type == "claim.acquired" || event.Type == "claim.released" {
			if !replace && event.Resource != final.Resources[0] {
				t.Fatalf("claim event identity drift: %+v", event)
			}
			if event.Type == "claim.acquired" {
				acquired++
			} else {
				released++
			}
		}
	}
	wantReleased := 1
	if replace {
		wantReleased = 0
	}
	if acquired != 1 || released != wantReleased {
		t.Fatalf("claim events: acquired=%d released=%d", acquired, released)
	}
}

func selectProviderBinary(t *testing.T, binary string) {
	t.Helper()
	if binary == "" {
		return
	}
	bin := t.TempDir()
	if err := os.Symlink(binary, filepath.Join(bin, "worklease")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// This is a real supervised worker, not a session-name convention. Start/done
// and heartbeat must leave its lease to the live Worklease supervisor.
func TestProviderRunManagedWorker(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 for Worklease 1.10.1")
	}
	providerBinary := os.Getenv("BL_PROVIDER_WORKLEASE")
	root := writeFixture(t)
	enableCoordination(t, root)
	selectProviderBinary(t, providerBinary)
	assertProviderBuild(t)
	code, out, _ := writeRun(root, "", "show", "12")
	if code != 0 {
		t.Fatal(out)
	}
	var shown struct{ Data struct{ Resource string } }
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("worklease", "--json", "run", "--resource", shown.Data.Resource, "--", os.Args[0], "-test.run=^TestRunManagedWorkerProcess$", "--", root)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("supervised worker: %v %s", err, output)
	}
	command = exec.Command("worklease", "--json", "list")
	command.Dir = root
	output, err = command.CombinedOutput()
	if err != nil || strings.Contains(string(output), shown.Data.Resource) {
		t.Fatalf("supervisor did not release after worker exit: %s %v", output, err)
	}
}
func TestRunManagedWorkerProcess(t *testing.T) {
	root := ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			root = os.Args[i+1]
			break
		}
	}
	if root == "" {
		return
	}
	if os.Getenv("WORKLEASE_RUN_ID") == "" || os.Getenv("WORKLEASE_SESSION_ID") == "" {
		t.Fatal("missing supervisor context")
	}
	// Clear the parent's BL_SESSION so CLI uses the worker's actual caller session.
	t.Setenv("BL_SESSION", "")
	for _, args := range [][]string{{"next", "--claim", "--start"}, {"start", "12"}, {"heartbeat"}, {"done", "12"}} {
		code, out, stderr := writeRun(root, "", args...)
		if code != 0 || !strings.Contains(out, `"outcome":"run-managed"`) {
			t.Fatalf("worker %v: %d %s %s", args, code, out, stderr)
		}
		verify := exec.Command("worklease", "--json", "verify")
		verify.Dir = root
		output, err := verify.CombinedOutput()
		if err != nil {
			t.Fatalf("bl changed supervisor lease after %v: %s %v", args, output, err)
		}
	}
}

func assertProviderBuild(t *testing.T) {
	t.Helper()
	output, err := exec.Command("worklease", "--json", "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	var version struct {
		Version       string
		SchemaVersion int
	}
	if err = json.Unmarshal(output, &version); err != nil || version.SchemaVersion != 2 || !worklease.CompatibleVersion(version.Version) {
		t.Fatalf("requires Worklease >=1.10.1 with schema-v2: %s %v", output, err)
	}
	t.Logf("provider build: %s", output)
}
