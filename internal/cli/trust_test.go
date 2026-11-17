package cli_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
	"go.yaml.in/yaml/v3"
)

// Catches clone-triggered execution/network access across inspection, source
// reads and writes, including doctor --online and trust --show before consent.
func TestClonedConfigIsInertUntilTrust(t *testing.T) {
	home := testkit.Isolate(t)
	original := filepath.Join(home, "original")
	clone := filepath.Join(home, "clone")
	marker := filepath.Join(home, "canary")
	git := func(directory string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", directory}, args...)...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
	}
	if err := os.MkdirAll(filepath.Join(original, ".config"), 0700); err != nil {
		t.Fatal(err)
	}
	git(original, "init", "-b", "main")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	argv := []string{os.Args[0], "-test.run=^TestTrustCanaryProcess$", "--", "bl-trust-canary", marker}
	settings := project.Settings{Milestones: []project.Milestone{{Key: "m1", Name: "First"}}, Version: 1, Project: project.ProjectConfig{ID: "test-clone"}, Sources: []project.SourceConfig{{ID: "adapter", Type: "external", Run: argv}, {ID: "network", Type: "github", Host: server.URL, Repo: "acme/test"}}, Hooks: []project.HookConfig{{Name: "canary", On: "item.created", Run: argv}}, Actions: []project.ActionConfig{{Name: "canary", Run: argv}}}
	data, err := yaml.Marshal(settings)
	// Omit the YAML zero value while retaining its always-present JSON value.
	data = []byte(strings.ReplaceAll(string(data), "    archived: false\n", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(original, "backlog.yaml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	git(original, "add", "backlog.yaml")
	git(original, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	git(home, "clone", "--quiet", original, clone)
	// Keep the actual provider out of the test. Version probes are local only.
	fakeWorklease(t)
	for _, args := range [][]string{{"--help"}, {"version"}, {"config", "show", "--json"}, {"trust", "--show", "--json"}, {"doctor", "--json"}, {"doctor", "--online", "--json"}, {"ls", "--json"}, {"next", "--json"}, {"show", "1", "--json"}, {"new", "Canary", "--json"}, {"events", "--json"}, {"check", "--json"}, {"mcp"}, {"show", "", "--generate-shell-completion"}, {"ls", "status:", "--generate-shell-completion"}} {
		code, out, _ := run(append([]string{"-C", clone}, args...)...)
		if args[0] == "doctor" {
			assertDoctor(t, code, out, true)
			if !strings.Contains(out, `"name":"hooks","status":"warn","message":"hook canary is untrusted and inert; action canary is untrusted and inert"`) {
				t.Fatalf("untrusted hooks not reported inert: %s", out)
			}
		}
		if args[0] == "ls" && args[1] == "--json" && (code != 6 || !strings.Contains(out, "untrusted")) {
			t.Fatalf("source not gated: %d %s", code, out)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("%v executed canary: %v", args, err)
		}
		if requests.Load() != 0 {
			t.Fatalf("%v contacted network", args)
		}
	}
	code, out, stderr := run("-C", clone, "config", "show", "--json")
	var cfg struct{ Data project.Config }
	if err := json.Unmarshal([]byte(out), &cfg); err != nil || code != 0 {
		t.Fatalf("config: %d %s %s %v", code, out, stderr, err)
	}
	for _, key := range []string{"hooks[0].run[0]", "actions[0].run[0]", "sources[0].run[0]"} {
		if cfg.Data.Provenance[key].Layer != "project" {
			t.Fatalf("missing provenance %s", key)
		}
	}
	// Implicit values emitted by JSON still need a layer (BL-29 review regression).
	if cfg.Data.Provenance["milestones[0].archived"].Layer != "built-in" {
		t.Fatalf("implicit archived default has no built-in provenance: %+v", cfg.Data.Provenance)
	}
	if len(cfg.Data.Privileged) != 4 || len(cfg.Data.Untrusted()) != 4 {
		t.Fatalf("trust markers: %+v", cfg.Data.Privileged)
	}
	code, out, stderr = run("-C", clone, "trust", "--json")
	if code != 0 {
		t.Fatalf("trust: %d %s %s", code, out, stderr)
	}
	code, out, _ = run("-C", clone, "config", "show", "--json")
	if err := json.Unmarshal([]byte(out), &cfg); err != nil || code != 0 || len(cfg.Data.Untrusted()) != 0 {
		t.Fatalf("grant not loaded: %d %s %v", code, out, err)
	}
	// Completion reads only cached rows, even from trusted sources (D16).
	for _, args := range [][]string{{"show", "", "--generate-shell-completion"}, {"ls", "label:", "--generate-shell-completion"}} {
		if code, out, stderr := run(append([]string{"-C", clone}, args...)...); code != 0 || stderr != "" {
			t.Fatalf("trusted completion %v: %d %s %s", args, code, out, stderr)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) || requests.Load() != 0 {
			t.Fatalf("trusted completion %v executed or contacted network: %v", args, err)
		}
	}
	// Trust permits execution, but an executable without a valid handshake is unavailable.
	code, out, _ = run("-C", clone, "ls", "--json")
	if code != 6 || strings.Contains(out, "untrusted") {
		t.Fatalf("trusted source: %d %s", code, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("trusted adapter did not execute: %v", err)
	}
}

func TestTrustCanaryProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "bl-trust-canary" && i+1 < len(os.Args) {
			if err := os.WriteFile(os.Args[i+1], []byte("executed"), 0600); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}

func fakeWorklease(t *testing.T) {
	testkit.Unix(t, "a shell-script Worklease fake")
	t.Helper()
	directory := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' '{\"ok\":true,\"schemaVersion\":2,\"version\":\"1.10.1\"}'\n"
	if err := os.WriteFile(filepath.Join(directory, "worklease"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Private configuration must not silently leak through Git: ignored files
// are safe only when untracked, including force-added files matching ignores.
func TestDoctorLocalConfigGitSafety(t *testing.T) {
	testkit.Isolate(t)
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if data, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, data)
		}
	}
	git("init", "-b", "main")
	put(t, project.LocalFile(root), "project: {id: private}\n")
	check := func(want string) {
		t.Helper()
		_, out, _ := run("-C", root, "--json", "doctor")
		var result struct {
			Data struct {
				Checks []struct{ Name, Status string }
			}
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		for _, c := range result.Data.Checks {
			if c.Name == "local-config" {
				if c.Status != want {
					t.Fatalf("local config: want %s: %s", want, out)
				}
				return
			}
		}
		t.Fatalf("missing local-config check: %s", out)
	}
	check("warn")
	put(t, filepath.Join(root, ".gitignore"), "/backlog.local.yaml\n")
	check("pass")
	git("add", "--force", "backlog.local.yaml")
	check("warn")
}

func assertDoctor(t *testing.T, code int, out string, failed bool) {
	t.Helper()
	var result struct {
		OK   bool
		Data struct {
			Checks []struct{ Name, Status, Message, Fix string }
		}
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err, out)
	}
	if result.OK == failed || (failed && code != 5) || (!failed && code != 0) {
		t.Fatalf("doctor status: %d %s", code, out)
	}
	want := []string{"config", "trust", "hooks", "control-root", "local-config", "cache", "worklease", "claims", "sources", "accounts", "forges"}
	if len(result.Data.Checks) != len(want) {
		t.Fatalf("missing checks: %s", out)
	}
	for i, c := range result.Data.Checks {
		if c.Name != want[i] || c.Status == "" || c.Fix == "" || c.Message == "" {
			t.Fatalf("incomplete check: %+v", c)
		}
	}
}

// Catches doctor accidentally auto-repairing corruption or returning a success
// envelope when cache or configuration validation has failed.
func TestDoctorReportsCorruptCacheAndInvalidConfig(t *testing.T) {
	home := testkit.Isolate(t)
	root := filepath.Join(home, "repo")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n bare = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "items"), 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "backlog.yaml")
	if err := os.WriteFile(configPath, []byte("sources: [{id: tasks, type: native, path: items, prefix: BL}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fakeWorklease(t)
	code, out, _ := run("-C", root, "doctor", "--json")
	assertDoctor(t, code, out, false)
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := cache.New(paths.CacheDir, cfg.Project.ID, "tasks", filepath.Join(cfg.Discovery.ControlRoot, "items"))
	if err := os.MkdirAll(filepath.Dir(store.Path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path, []byte("corrupt cache"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = run("-C", root, "doctor", "--json")
	assertDoctor(t, code, out, true)
	if !strings.Contains(out, `"name":"cache","status":"fail"`) {
		t.Fatal(out)
	}
	b, err := os.ReadFile(store.Path)
	if err != nil || string(b) != "corrupt cache" {
		t.Fatalf("doctor changed cache: %s %v", b, err)
	}
	if err := os.WriteFile(configPath, []byte("unknown: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = run("-C", root, "doctor", "--json")
	assertDoctor(t, code, out, true)
	if !strings.Contains(out, `"name":"config","status":"fail"`) {
		t.Fatal(out)
	}
}

// Catches doctor passing while linked pull requests can never appear: a
// GitHub origin with no account or several, an untrusted explicit forge, or
// a token that can't read pull requests or their checks.
func TestDoctorForges(t *testing.T) {
	root := writeFixture(t)
	fakeWorklease(t)
	put(t, filepath.Join(root, ".git/config"), "[core]\n bare = false\n[remote \"origin\"]\n url = https://github.com/owner/repo.git\n")
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(paths.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	var deny atomic.Value
	deny.Store("")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Query string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "viewer") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"viewer": map[string]any{"login": "alice"}}})
			return
		}
		node := map[string]any{"number": 7, "title": "Fix", "url": "https://github.com/owner/repo/pull/7", "state": "OPEN", "headRefName": "fix", "createdAt": "2026-11-01T00:00:00Z", "updatedAt": "2026-11-01T00:00:00Z", "closingIssuesReferences": map[string]any{"nodes": []any{}}, "commits": map[string]any{"nodes": []any{}}}
		var failures []any
		switch deny.Load() {
		case "checks":
			delete(node, "commits")
			failures = append(failures, map[string]any{"type": "FORBIDDEN", "path": []any{"repository", "pullRequests", "nodes", 0, "commits"}})
		case "pulls":
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"type": "FORBIDDEN", "path": []any{"repository", "pullRequests"}}}, "data": map[string]any{"repository": map[string]any{"pullRequests": nil}}})
			return
		}
		page := map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": []any{node}}
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": failures, "data": map[string]any{"repository": map[string]any{"pullRequests": page}}})
	}))
	defer server.Close()
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	original := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })
	t.Setenv("BL_FORGE_TOKEN", "forge-test-credential")

	type report struct {
		Type, Host, Repo, Account, Status string
		Explicit                          bool
		Unreadable                        []string
		Sync                              struct {
			Kept      int
			Refreshed string
		}
	}
	doctor := func(wantCode int) (forges struct{ Status, Message, Fix string }, reports []report) {
		t.Helper()
		code, out, _ := run("-C", root, "--json", "doctor")
		var result struct {
			Data struct {
				Checks []struct{ Name, Status, Message, Fix string }
				Forges []report
			}
			Error *struct{ Data json.RawMessage }
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err, out)
		}
		if result.Error != nil && result.Error.Data != nil {
			_ = json.Unmarshal(result.Error.Data, &result.Data)
		}
		if code != wantCode {
			t.Fatalf("doctor exit %d, want %d: %s", code, wantCode, out)
		}
		for _, c := range result.Data.Checks {
			if c.Name == "forges" {
				forges.Status, forges.Message, forges.Fix = c.Status, c.Message, c.Fix
			}
		}
		return forges, result.Data.Forges
	}
	account := func(name string) string {
		return "    - {name: " + name + ", host: github.com, user: alice, env: BL_FORGE_TOKEN}\n"
	}

	type configForge struct {
		Type, Host, Repo, Account string
		Implicit                  bool
	}
	// config show lists the forges doctor checks, or says why there are none
	// in doctor's words.
	configShow := func() (forges []configForge, reason, fix string) {
		t.Helper()
		code, out, _ := run("-C", root, "--json", "config", "show")
		var result struct {
			Data struct {
				Changes struct {
					Forges      []configForge
					Reason, Fix string
				}
			}
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil || code != 0 {
			t.Fatalf("config show: %d %v %s", code, err, out)
		}
		return result.Data.Changes.Forges, result.Data.Changes.Reason, result.Data.Changes.Fix
	}

	c, _ := doctor(0)
	if c.Status != "warn" || !strings.Contains(c.Message, "no GitHub account for github.com") || !strings.Contains(c.Fix, "accounts: {github: [{name: personal, host: github.com") || !strings.Contains(c.Fix, "bl auth login github personal") {
		t.Fatalf("no account: %+v", c)
	}
	if forges, reason, fix := configShow(); len(forges) != 0 || reason != c.Message || fix != c.Fix {
		t.Fatalf("config show without account: %+v %q %q", forges, reason, fix)
	}

	put(t, paths.ConfigFile, "accounts:\n  github:\n"+account("personal")+account("work"))
	if c, _ = doctor(0); c.Status != "warn" || !strings.Contains(c.Message, "personal, work") || !strings.Contains(c.Fix, "changes: {forges: [{type: github, host: github.com, repo: owner/repo, account: personal}]}") {
		t.Fatalf("two accounts: %+v", c)
	}

	put(t, paths.ConfigFile, "accounts:\n  github:\n"+account("personal"))
	if forges, reason, _ := configShow(); len(forges) != 1 || forges[0] != (configForge{"github", "github.com", "owner/repo", "personal", true}) || reason != "" {
		t.Fatalf("config show implicit forge: %+v %q", forges, reason)
	}
	if code, out, _ := writeRun(root, "", "changes", "12"); code != 0 {
		t.Fatalf("changes: %d %s", code, out)
	}
	c, reports := doctor(0)
	if c.Status != "pass" || len(reports) != 1 {
		t.Fatalf("implicit forge: %+v %+v", c, reports)
	}
	if r := reports[0]; r.Type != "github" || r.Host != "github.com" || r.Repo != "owner/repo" || r.Account != "personal" || r.Explicit || r.Status != "ok" || r.Sync.Kept != 1 || r.Sync.Refreshed == "" {
		t.Fatalf("implicit forge report: %+v", r)
	}
	deny.Store("checks")
	if c, reports = doctor(0); c.Status != "warn" || !strings.Contains(c.Message, "can't read checks") || reports[0].Status != "warn" {
		t.Fatalf("checks forbidden: %+v %+v", c, reports)
	}
	deny.Store("pulls")
	if c, reports = doctor(5); c.Status != "fail" || !strings.Contains(c.Message, "can't read pull requests") || reports[0].Status != "fail" {
		t.Fatalf("pull requests forbidden: %+v %+v", c, reports)
	}

	path := filepath.Join(root, "backlog.yaml")
	put(t, path, get(t, path)+"changes: {forges: [{type: github, repo: owner/repo, account: personal}]}\n")
	if c, reports = doctor(5); c.Status != "fail" || !strings.Contains(c.Message, "changes.forges[0] is untrusted") || !strings.Contains(c.Fix, "bl trust") || !reports[0].Explicit {
		t.Fatalf("untrusted explicit forge: %+v %+v", c, reports)
	}
}
