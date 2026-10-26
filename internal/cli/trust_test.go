package cli_test

import (
	"encoding/json"
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
	for _, args := range [][]string{{"--help"}, {"version"}, {"config", "show", "--json"}, {"trust", "--show", "--json"}, {"doctor", "--json"}, {"doctor", "--online", "--json"}, {"ls", "--json"}, {"next", "--json"}, {"show", "1", "--json"}, {"new", "Canary", "--json"}, {"events", "--json"}, {"check", "--json"}, {"mcp"}} {
		code, out, _ := run(append([]string{"-C", clone}, args...)...)
		if args[0] == "doctor" {
			assertDoctor(t, code, out, true)
			if !strings.Contains(out, `"name":"hooks","status":"warn","message":"hook canary is untrusted and inert; action canary is untrusted and inert"`) {
				t.Fatalf("untrusted hooks not reported inert: %s", out)
			}
		}
		if args[0] == "ls" && (code != 6 || !strings.Contains(out, "untrusted")) {
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
	want := []string{"config", "trust", "hooks", "control-root", "local-config", "cache", "worklease", "claims", "sources"}
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
