package cli_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
profile) printf '%s\n' '{"schemaVersion":2,"ok":true,"profile":{"builtin":true,"name":"local"}}';;
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
profile) printf '%s\n' '{"schemaVersion":2,"ok":true,"profile":{"builtin":true,"name":"local"}}';;
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

// A blocking command longer than the TTL (BL-91's `task ci`) must keep the
// claim renewed until it exits, and its exit status must reach the caller.
func TestHeartbeatRenewsWhileCommandRuns(t *testing.T) {
	root := writeFixture(t)
	enableCoordination(t, root)
	bin := t.TempDir()
	path := filepath.Join(bin, "worklease")
	put(t, path, get(t, "testdata/worklease.py"))
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	renewals := filepath.Join(t.TempDir(), "renewals")
	t.Setenv("BL_FAKE_ROOT", root)
	t.Setenv("BL_FAKE_STATE", filepath.Join(t.TempDir(), "claim.json"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if code, out, stderr := writeRun(root, "", "claim", "12"); code != 0 {
		t.Fatalf("claim: %d %s %s", code, out, stderr)
	}
	// Every lease is already near expiry, so renewal falls to its one-second floor.
	// The command outlasts two floors with slack for slow fake-process spawns under -race.
	t.Setenv("BL_FAKE_EXPIRES", "2000-01-01T00:00:00Z")
	t.Setenv("BL_FAKE_HEARTBEATS", renewals)
	code, out, stderr := writeRun(root, "", "heartbeat", "--", "sh", "-c", "sleep 4.5; echo finished; exit 3")
	if code != 3 || out != "finished\n" {
		t.Fatalf("command status: %d %q %s", code, out, stderr)
	}
	if n := strings.Count(get(t, renewals), "renewed"); n < 3 {
		t.Fatalf("renewed %d times while the command ran", n)
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
	t.Setenv("CLAUDE_CODE_SESSION_ID", "harness-1")
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
			// Each event names the invoking checkout and its branch, read
			// without starting git.
			checkout, branch := root, ""
			if event.Type == "claim.acquired" {
				acquired++
			} else {
				released++
				checkout, branch = linked, "linked"
			}
			checkout, _ = filepath.EvalSymlinks(checkout)
			if s := event.Session; s == nil || s.ID != "loop" || s.Agent != "claude" || s.AgentSession != "harness-1" || s.Host == "" || s.CWD != checkout || s.Branch != branch {
				t.Fatalf("claim event session: %+v", s)
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

// A host-local project key must never reach a remote authority, where every
// host derives a different key and claims silently never contend.
func TestHostLocalClaimRefusedWithRemoteProfile(t *testing.T) {
	root := writeFixture(t)
	enableCoordination(t, root)
	script := `#!/bin/sh
[ "$1" = version ] && { printf '%s\n' '{"schemaVersion":2,"ok":true,"version":"1.10.1"}'; exit 0; }
case "$2" in
profile) printf '%s\n' '{"schemaVersion":2,"ok":true,"profile":{"name":"team","endpoint":"https://authority.example:8443"}}';;
handle) printf '%s\n' '{"schemaVersion":2,"ok":false,"error":{"reason":"handle-malformed","message":"handle is missing"}}';exit 2;;
*) touch "$BL_FAKE_CALLED"; exit 1;;
esac
`
	bin := t.TempDir()
	path := filepath.Join(bin, "worklease")
	put(t, path, script)
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	called := filepath.Join(t.TempDir(), "called")
	t.Setenv("BL_FAKE_CALLED", called)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, args := range [][]string{{"claim", "12"}, {"next", "--claim", "--start"}, {"start", "12", "--session", "s"}} {
		code, out, _ := writeRun(root, "", args...)
		if code != 6 || !strings.Contains(out, `"reason":"unavailable"`) || !strings.Contains(out, "host-local") {
			t.Fatalf("%v: %d %s", args, code, out)
		}
	}
	if _, err := os.Stat(called); err == nil {
		t.Fatal("host-local key reached the remote authority")
	}
	if code, out, _ := writeRun(root, "", "show", "12", "--json"); code != 0 || !strings.Contains(out, `"status":"todo"`) {
		t.Fatalf("refused transition wrote: %d %s", code, out)
	}
	code, out, _ := writeRun(root, "", "doctor", "--json")
	if code != 5 || !strings.Contains(out, `"name":"worklease","status":"fail","message":"project ID`) {
		t.Fatalf("doctor: %d %s", code, out)
	}
	// Heartbeat and release are never refused, so existing claims can be cleaned up.
	for _, args := range [][]string{{"heartbeat", "--session", "s"}, {"release", "12", "--session", "s"}} {
		if _, out, _ := writeRun(root, "", args...); strings.Contains(out, "host-local") {
			t.Fatalf("%v: %s", args, out)
		}
	}
}

// remoteHome is one isolated host: its own HOME and XDG directories, resolved
// because Worklease refuses owner-private paths below a symlink (macOS /var).
type remoteHome []string

func newRemoteHome(t *testing.T, agent string) remoteHome {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err == nil {
		err = os.Chmod(dir, 0700)
	}
	if err != nil {
		t.Fatal(err)
	}
	env := remoteHome{"HOME=" + dir, "WORKLEASE_AGENT_ID=" + agent}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR"} {
		sub := filepath.Join(dir, name)
		if err := os.Mkdir(sub, 0700); err != nil {
			t.Fatal(err)
		}
		env = append(env, name+"="+sub)
	}
	return env
}

// use selects this host for in-process bl calls and the worklease they spawn.
func (h remoteHome) use(t *testing.T) {
	t.Helper()
	for _, pair := range h {
		name, value, _ := strings.Cut(pair, "=")
		t.Setenv(name, value)
	}
}
func (h remoteHome) dir() string { return strings.TrimPrefix(h[0], "HOME=") }
func (h remoteHome) worklease(t *testing.T, dir string, args ...string) map[string]any {
	t.Helper()
	cmd := exec.Command("worklease", append([]string{"--json"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), h...)
	output, _ := cmd.Output()
	var v map[string]any
	if err := json.Unmarshal(output, &v); err != nil || v["ok"] != true {
		t.Fatalf("worklease %v: %s %v", args, output, err)
	}
	return v
}

// remoteAuthority runs `worklease serve` behind a TLS-terminating proxy that
// uses the server's own pinned certificate. An armed proxy forwards the next
// acquire, so it commits, then kills the authority before relaying the reply.
type remoteAuthority struct {
	home   remoteHome
	config string
	listen string
	serve  *exec.Cmd
	armed  atomic.Bool
}

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}
func startRemoteAuthority(t *testing.T) (*remoteAuthority, string) {
	t.Helper()
	a := &remoteAuthority{home: newRemoteHome(t, "server"), listen: freeAddress(t)}
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	invite := filepath.Join(a.home.dir(), "bootstrap.invite")
	init := a.home.worklease(t, t.TempDir(), "server", "init", "--listen", a.listen, "--endpoint", "https://"+proxy.Addr().String(), "--bootstrap-invite-file", invite)
	a.config = init["serverConfig"].(string)
	certificate, err := tls.LoadX509KeyPair(init["certificateFile"].(string), init["keyFile"].(string))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	backend := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	target := &url.URL{Scheme: "https", Host: a.listen}
	forward := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(target); r.Out.Host = r.In.Host }, Transport: backend}
	server := &http.Server{
		TLSConfig:    &tls.Config{Certificates: []tls.Certificate{certificate}},
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/v1/claims/acquire" && a.armed.CompareAndSwap(true, false) {
				out := r.Clone(r.Context())
				out.URL = target.JoinPath(r.URL.Path)
				out.RequestURI = ""
				response, err := backend.RoundTrip(out)
				if err == nil {
					response.Body.Close()
				}
				a.stop()
				panic(http.ErrAbortHandler)
			}
			forward.ServeHTTP(w, r)
		}),
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go server.ServeTLS(proxy, "", "")
	t.Cleanup(func() { server.Close(); a.stop() })
	a.start(t)
	return a, invite
}
func (a *remoteAuthority) start(t *testing.T) {
	t.Helper()
	a.serve = exec.Command("worklease", "serve", "--server-config", a.config)
	a.serve.Env = append(os.Environ(), a.home...)
	if err := a.serve.Start(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		if conn, err := net.Dial("tcp", a.listen); err == nil {
			conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("worklease serve did not listen")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
func (a *remoteAuthority) stop() {
	if a.serve != nil && a.serve.Process != nil {
		_ = a.serve.Process.Kill()
		_ = a.serve.Wait()
		a.serve = nil
	}
}

type remoteNext struct {
	Data struct {
		Items    []struct{ ID string }
		Attempts []struct {
			Ref   struct{ ID string }
			Claim struct{ Outcome, Reason string }
		}
		Status string
	}
	Error struct{ Reason string }
}

func decodeNext(t *testing.T, out string) remoteNext {
	t.Helper()
	var v remoteNext
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("next: %s %v", out, err)
	}
	return v
}

// Two hosts with their own enrolled homes and clones of one origin share a
// remote TLS authority: claims contend, renew, release, and an acquire whose
// reply is lost when the authority stops replays exactly after restart.
func TestProviderRemoteAuthority(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 for Worklease 1.10.1")
	}
	selectProviderBinary(t, os.Getenv("BL_PROVIDER_WORKLEASE"))
	assertProviderBuild(t)
	authority, bootstrap := startRemoteAuthority(t)
	hostA, hostB := newRemoteHome(t, "agent-a"), newRemoteHome(t, "agent-b")
	hostA.worklease(t, t.TempDir(), "enroll", "--invite-file", bootstrap)
	invite := filepath.Join(hostA.dir(), "b.invite")
	hostA.worklease(t, t.TempDir(), "invite", "issue", "--invite-file", invite)
	hostB.worklease(t, t.TempDir(), "enroll", "--invite-file", invite)
	clone := func() string {
		root := writeFixture(t)
		enableCoordination(t, root)
		nextItems(t, root)
		put(t, filepath.Join(root, ".git/config"), "[core]\n bare = false\n[remote \"origin\"]\n url = https://github.com/example/shared.git\n")
		return root
	}
	rootA, rootB := clone(), clone()

	hostA.use(t)
	code, out, stderr := writeRun(rootA, "", "next", "--claim", "--start", "--session", "loop-a")
	first := decodeNext(t, out)
	if code != 0 || len(first.Data.Items) != 1 || first.Data.Items[0].ID != "BL-012" {
		t.Fatalf("host A next: %d %s %s", code, out, stderr)
	}
	hostB.use(t)
	code, out, stderr = writeRun(rootB, "", "next", "--claim", "--start", "--session", "loop-b")
	second := decodeNext(t, out)
	if code != 0 || len(second.Data.Items) != 1 || second.Data.Items[0].ID != "BL-013" || second.Data.Attempts[0].Claim.Outcome != "contended" {
		t.Fatalf("host B next did not skip host A's claim: %d %s %s", code, out, stderr)
	}
	code, out, _ = writeRun(rootB, "", "claim", "12", "--session", "loop-c")
	if code != 7 || !strings.Contains(out, "agent-a") {
		t.Fatalf("host B claim of host A's item: %d %s", code, out)
	}

	hostA.use(t)
	code, out, _ = writeRun(rootA, "", "heartbeat", "--session", "loop-a")
	if code != 0 || !strings.Contains(out, `"outcome":"renewed"`) {
		t.Fatalf("heartbeat: %d %s", code, out)
	}
	code, out, _ = writeRun(rootA, "", "done", "12", "--session", "loop-a")
	if code != 0 || !strings.Contains(out, `"outcome":"released"`) {
		t.Fatalf("done: %d %s", code, out)
	}
	hostB.use(t)
	code, out, _ = writeRun(rootB, "", "done", "13", "--session", "loop-b")
	if code != 0 || !strings.Contains(out, `"outcome":"released"`) {
		t.Fatalf("host B done: %d %s", code, out)
	}
	if claims := hostB.worklease(t, rootB, "list")["claims"]; claims != nil {
		t.Fatalf("remote claims survived done: %v", claims)
	}

	// Host A's clone still has BL-013 open. Lose the committed acquire's reply.
	hostA.use(t)
	authority.armed.Store(true)
	code, out, stderr = writeRun(rootA, "", "next", "--claim", "--start", "--session", "replay")
	lost := decodeNext(t, out)
	if code != 6 || lost.Error.Reason != "unknown-outcome" || lost.Data.Status != "not-written" || len(lost.Data.Attempts) != 1 || lost.Data.Attempts[0].Ref.ID != "BL-013" {
		t.Fatalf("interrupted acquire: %d %s %s", code, out, stderr)
	}
	authority.start(t)
	code, out, stderr = writeRun(rootA, "", "next", "--claim", "--start", "--session", "replay")
	replayed := decodeNext(t, out)
	if code != 0 || len(replayed.Data.Items) != 1 || replayed.Data.Items[0].ID != "BL-013" || len(replayed.Data.Attempts) != 1 {
		t.Fatalf("replay: %d %s %s", code, out, stderr)
	}
	claims, _ := hostA.worklease(t, rootA, "list")["claims"].([]any)
	if len(claims) != 1 || claims[0].(map[string]any)["sessionId"] != "replay" {
		t.Fatalf("replay acquired other than exactly one claim: %v", claims)
	}

	// A clone without origin has a host-local ID: refused, never sent.
	local := writeFixture(t)
	enableCoordination(t, local)
	hostA.use(t)
	code, out, _ = writeRun(local, "", "claim", "12", "--session", "local")
	if code != 6 || !strings.Contains(out, "host-local") {
		t.Fatalf("host-local claim with remote profile: %d %s", code, out)
	}
	if code, out, _ = writeRun(local, "", "doctor", "--json"); code != 5 || !strings.Contains(out, `"name":"worklease","status":"fail","message":"project ID`) {
		t.Fatalf("doctor with host-local ID and remote profile: %d %s", code, out)
	}
}
