package hooks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cli"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

func runnerConfig(t *testing.T, entries ...project.HookConfig) project.Config {
	t.Helper()
	root := t.TempDir()
	return project.Config{
		Settings:  project.Settings{Hooks: entries, Project: project.ProjectConfig{ID: "example/project"}},
		Paths:     project.Paths{StateDir: filepath.Join(t.TempDir(), "state")},
		Discovery: project.Discovery{ControlRoot: root},
	}
}

func automationEvent(id, eventType string) hooks.Event {
	return hooks.Event{
		ID: id, Type: eventType, Project: "example/project", Source: "tasks", Ref: "tasks:BL-12",
		Item: map[string]any{
			"ref":      map[string]any{"source": "tasks", "id": "BL-12"},
			"title":    "Design Hook Runner",
			"status":   "todo",
			"category": "todo",
			"url":      "https://example.invalid/issues/12",
		},
	}
}

func executable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func finishedEvents(t *testing.T, cfg project.Config) []hooks.Event {
	t.Helper()
	events, err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var finished []hooks.Event
	for _, event := range events {
		if event.Type == "hook.finished" {
			finished = append(finished, event)
		}
	}
	return finished
}

// Catches shell interpolation regressions and result/cwd loss across a chained
// worktree-to-agent handoff.
func TestAfterHookChainUsesStdoutResultAndLiteralArgv(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	worktree := filepath.Join(root, "worktree")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(worktree, 0700); err != nil {
		t.Fatal(err)
	}
	wtArgs := filepath.Join(root, "wt-args")
	workleaseArgs := filepath.Join(root, "worklease-args")
	shellSentinel := filepath.Join(root, "shell-ran")
	worktreeJSON, _ := json.Marshal(map[string]string{"path": worktree})
	executable(t, filepath.Join(bin, "wt"), fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nprintf '%%s\\n' '%s'\n", wtArgs, worktreeJSON))
	executable(t, filepath.Join(bin, "worklease"), fmt.Sprintf("#!/bin/sh\nprintf 'cwd=%%s\\n' \"$PWD\" > %q\nprintf '<%%s>\\n' \"$@\" >> %q\n", workleaseArgs, workleaseArgs))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	literal := "literal;$(touch " + shellSentinel + ")"
	cfg := runnerConfig(t,
		project.HookConfig{Name: "work", On: "item.created", Run: []string{"wt", "switch", "--create", "{branch}"}},
		project.HookConfig{Name: "agent", On: "hook.finished", If: "hook:work outcome:ok", CWD: "{result.path}", Run: []string{"worklease", "run", "--resource", "{resource}", "--branch", "{branch}", "--message", literal}},
	)
	event := automationEvent("event-worktree", "item.created")
	event.Resource = "generic:example/project:BL-12"
	if err := hooks.RunEvent(context.Background(), cfg, event, root); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readFile(t, wtArgs)); got != "switch\n--create\nbl-12-design-hook-runner" {
		t.Fatalf("wt argv: %q", got)
	}
	physicalWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, workleaseArgs); !strings.Contains(got, "cwd="+physicalWorktree+"\n") || !strings.Contains(got, "<"+event.Resource+">\n") || !strings.Contains(got, "<bl-12-design-hook-runner>\n") || !strings.Contains(got, "<"+literal+">\n") {
		t.Fatalf("worklease did not receive checked cwd/argv literally: %q", got)
	}
	if _, err := os.Stat(shellSentinel); !os.IsNotExist(err) {
		t.Fatalf("argv metacharacters executed as shell: %v", err)
	}
	finished := finishedEvents(t, cfg)
	if len(finished) != 2 {
		t.Fatalf("got %d hook.finished events, want one per hook", len(finished))
	}
	byName := map[string]hooks.Event{}
	for _, event := range finished {
		if event.Outcome != "ok" || event.ExitCode == nil || *event.ExitCode != 0 || event.Duration <= 0 {
			t.Fatalf("incomplete finished result: %+v", event)
		}
		byName[event.Name] = event
	}
	if byName["work"].Result["path"] != worktree {
		t.Fatalf("stdout result missing from work event: %+v", byName["work"])
	}
}

// The documented notify-ready query must fire only on the derived ready state.
func TestNotifyReadyAutomationExample(t *testing.T) {
	root := t.TempDir()
	tellLog := filepath.Join(root, "tell-args")
	tell := filepath.Join(root, "tell")
	executable(t, tell, fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", tellLog))
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := runnerConfig(t, project.HookConfig{Name: "notify-ready", On: "item.readiness", If: "is:ready", Run: []string{"tell", "notify", "Ready: {id}"}})
	ready := automationEvent("event-ready", "item.readiness")
	ready.Changes = map[string]hooks.Change{"readiness": {From: "blocked", To: "ready"}}
	blocked := automationEvent("event-blocked", "item.readiness")
	blocked.Changes = map[string]hooks.Change{"readiness": {From: "ready", To: "blocked"}}
	if err := hooks.RunEvent(context.Background(), cfg, ready, root); err != nil {
		t.Fatal(err)
	}
	if err := hooks.RunEvent(context.Background(), cfg, blocked, root); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readFile(t, tellLog)); got != "notify\nReady: BL-12" {
		t.Fatalf("notify-ready argv: %q", got)
	}
	if got := len(finishedEvents(t, cfg)); got != 1 {
		t.Fatalf("finished events=%d, want only the ready notification", got)
	}
}

// The §8 agent hook consumes a successful work action's result and exact item
// resource, without needing an action runner in this delivery.
func TestAgentAutomationExample(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "agent-worktree")
	if err := os.Mkdir(worktree, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "worklease-args")
	worklease := filepath.Join(root, "worklease")
	executable(t, worklease, fmt.Sprintf("#!/bin/sh\nprintf 'cwd=%%s\\n' \"$PWD\" > %q\nprintf '<%%s>\\n' \"$@\" >> %q\n", logPath, logPath))
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := runnerConfig(t, project.HookConfig{Name: "agent", On: "action.finished", If: "action:work outcome:ok", CWD: "{result.path}", Run: []string{"worklease", "run", "--detach", "--name", "{id}", "--ref", "{ref}", "--resource", "{resource}"}, Timeout: "30s"})
	event := automationEvent("event-action", "action.finished")
	event.Name, event.Outcome = "work", "ok"
	event.Resource = "generic:example/project:BL-12"
	event.Result = map[string]any{"path": worktree}
	if err := hooks.RunEvent(context.Background(), cfg, event, root); err != nil {
		t.Fatal(err)
	}
	physicalWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(t, logPath)
	for _, want := range []string{"cwd=" + physicalWorktree, "<BL-12>", "<tasks:BL-12>", "<" + event.Resource + ">"} {
		if !strings.Contains(got, want) {
			t.Fatalf("agent hook missing %q in %q", want, got)
		}
	}
}

// Untrusted project declarations remain inert for both mutation vetoes and after dispatch.
func TestUntrustedHooksAreInert(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	program := filepath.Join(root, "program")
	executable(t, program, fmt.Sprintf("#!/bin/sh\nprintf ran > %q\n", marker))
	cfg := runnerConfig(t,
		project.HookConfig{Name: "before", On: "item.updated", Phase: "before", Run: []string{program}},
		project.HookConfig{Name: "after", On: "item.created", Run: []string{program}},
	)
	cfg.Privileged = []project.Privileged{
		{Key: "hooks[0]", Trusted: false},
		{Key: "hooks[1]", Trusted: false},
	}
	if err := hooks.Before(context.Background(), cfg, automationEvent("event-before", "item.updated"), root); err != nil {
		t.Fatalf("untrusted veto ran: %v", err)
	}
	if err := hooks.RunEvent(context.Background(), cfg, automationEvent("event-after", "item.created"), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("untrusted hook executed: %v", err)
	}
	if got := len(finishedEvents(t, cfg)); got != 0 {
		t.Fatalf("untrusted hook emitted finished events: %d", got)
	}

	trusted := runnerConfig(t,
		project.HookConfig{Name: "before", On: "item.updated", Phase: "before", Run: []string{program}},
		project.HookConfig{Name: "after", On: "item.created", Run: []string{program}},
	)
	deepBefore := automationEvent("event-depth-four-before", "item.updated")
	deepBefore.Depth = 4
	if err := hooks.Before(context.Background(), trusted, deepBefore, root); err != nil {
		t.Fatalf("deep before event ran: %v", err)
	}
	deepAfter := automationEvent("event-depth-four-after", "item.created")
	deepAfter.Depth = 4
	if err := hooks.RunEvent(context.Background(), trusted, deepAfter, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) || len(finishedEvents(t, trusted)) != 0 {
		t.Fatalf("hooks ran beyond causal depth 3: %v", err)
	}
}

// A backgrounded child inherits the hook's output pipes; without a bounded wait
// a before hook that exits at once would still block the write for its lifetime.
func TestBeforeHookIgnoresBackgroundedChildOutput(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "program")
	executable(t, program, "#!/bin/sh\nsleep 30 &\nexit 0\n")
	cfg := runnerConfig(t, project.HookConfig{Name: "before", On: "item.updated", Phase: "before", Run: []string{program}, Timeout: "10s"})
	started := time.Now()
	if err := hooks.Before(context.Background(), cfg, automationEvent("event-background", "item.updated"), root); err != nil {
		t.Fatalf("successful hook vetoed: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("before hook waited %v for a backgrounded child", elapsed)
	}
}

// A result file wins over stdout, and an oversized result cannot be chained.
func TestResultFilePrecedenceAndBound(t *testing.T) {
	root := t.TempDir()
	fileHook := filepath.Join(root, "file-result")
	executable(t, fileHook, "#!/bin/sh\nprintf '%s\\n' '{\"source\":\"file\"}' > \"$BL_RESULT\"\nprintf '%s\\n' '{\"source\":\"stdout\"}'\n")
	cfg := runnerConfig(t, project.HookConfig{Name: "result", On: "item.created", Run: []string{fileHook}})
	if err := hooks.RunEvent(context.Background(), cfg, automationEvent("event-file-result", "item.created"), root); err != nil {
		t.Fatal(err)
	}
	finished := finishedEvents(t, cfg)
	if len(finished) != 1 || finished[0].Outcome != "ok" || finished[0].ExitCode == nil || *finished[0].ExitCode != 0 || finished[0].Duration <= 0 || finished[0].Result["source"] != "file" {
		t.Fatalf("file result precedence: %+v", finished)
	}

	largeHook := filepath.Join(root, "large-result")
	executable(t, largeHook, "#!/bin/sh\nprintf '%08193d' 1 > \"$BL_RESULT\"\n")
	cfg = runnerConfig(t, project.HookConfig{Name: "large", On: "item.created", Run: []string{largeHook}})
	if err := hooks.RunEvent(context.Background(), cfg, automationEvent("event-large-result", "item.created"), root); err != nil {
		t.Fatal(err)
	}
	finished = finishedEvents(t, cfg)
	if len(finished) != 1 || finished[0].Outcome != "failed" || finished[0].Result != nil {
		t.Fatalf("oversized result accepted: %+v", finished)
	}
}

// Invalid placeholder values are refused before process start, not passed to
// executables where they could become flags, paths, or terminal controls.
func TestUnsafeAndMissingPlaceholdersRefuseWithoutRunning(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	program := filepath.Join(root, "program")
	executable(t, program, fmt.Sprintf("#!/bin/sh\nprintf ran > %q\n", marker))
	for n, result := range []map[string]any{{"path": "-danger"}, {"path": "line\nbreak"}, {}} {
		cfg := runnerConfig(t, project.HookConfig{Name: "unsafe", On: "item.created", Run: []string{program, "{result.path}"}})
		event := automationEvent(fmt.Sprintf("event-unsafe-%d", n), "item.created")
		event.Result = result
		if err := hooks.RunEvent(context.Background(), cfg, event, root); err != nil {
			t.Fatal(err)
		}
		finished := finishedEvents(t, cfg)
		if len(finished) != 1 || finished[0].Outcome != "refused" {
			t.Fatalf("placeholder result=%v: %+v", result, finished)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("unsafe value ran program: %v", err)
		}
	}
}

// A canary subprocess sees exactly the documented execution allowlist, not
// inherited provider tokens, session authority, or unrelated environment.
func TestHookCanaryChild(t *testing.T) {
	path := os.Getenv("BL_RESULT")
	if path == "" {
		return
	}
	env := map[string]string{}
	for _, pair := range os.Environ() {
		key, value, ok := strings.Cut(pair, "=")
		if ok {
			env[key] = value
		}
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHookEnvironmentCanary(t *testing.T) {
	testkit.Isolate(t)
	for _, key := range []string{"GITHUB_TOKEN", "LINEAR_API_KEY", "WORKLEASE_SESSION_ID", "BL_SESSION", "UNRELATED_CANARY"} {
		t.Setenv(key, "must-not-leak")
	}
	cfg := runnerConfig(t, project.HookConfig{Name: "canary", On: "item.created", Run: []string{mustExecutable(t), "-test.run=^TestHookCanaryChild$"}})
	cfg.Discovery.ControlRoot = t.TempDir()
	event := automationEvent("event-canary", "item.created")
	event.Resource = "generic:example/project:BL-12"
	if err := hooks.RunEvent(context.Background(), cfg, event, cfg.Discovery.ControlRoot); err != nil {
		t.Fatal(err)
	}
	finished := finishedEvents(t, cfg)
	if len(finished) != 1 || finished[0].Outcome != "ok" {
		t.Fatalf("canary hook: %+v", finished)
	}
	got, ok := finished[0].Result["GITHUB_TOKEN"]
	if ok || got != nil {
		t.Fatalf("credential leaked in result: %#v", got)
	}
	wantKeys := []string{"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "BL_ITEM_ID", "BL_ITEM_REF", "BL_SOURCE", "BL_PROJECT_ID", "BL_ROOT", "BL_BRANCH", "BL_EVENT_ID", "BL_HOOK_DEPTH", "BL_RESULT", "BL_RESOURCE"}
	sort.Strings(wantKeys)
	gotKeys := make([]string, 0, len(finished[0].Result))
	for key := range finished[0].Result {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("hook environment keys=%v want %v", gotKeys, wantKeys)
	}
	if finished[0].Result["WORKLEASE_SESSION_ID"] != nil || finished[0].Result["UNRELATED_CANARY"] != nil {
		t.Fatalf("unapproved environment leaked: %+v", finished[0].Result)
	}
}

func mustExecutable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// Both hook.finished recursion and a nested `bl note` call inherit causal depth.
func TestHookAndNestedBLChainsStopAtDepthThree(t *testing.T) {
	testkit.Isolate(t)
	root := t.TempDir()
	for _, dir := range []string{".git", ".config", "items"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	putFile(t, filepath.Join(root, ".git", "config"), "[core]\n bare = false\n")
	putFile(t, filepath.Join(root, "backlog.yaml"), "version: 1\nproject: {id: example/project}\nsources: [{id: tasks, type: native, path: items, prefix: BL}]\nstatuses: [{key: todo, name: To Do, category: todo}]\n")
	putFile(t, filepath.Join(root, "items", "BL-12.md"), "---\nid: BL-12\ntitle: Hook depth item\nstatus: todo\npriority: low\ncreated: 2026-01-01T00:00:00Z\nupdated: 2026-01-01T00:00:00Z\n---\nDescription.\n")
	binary := mustExecutable(t)
	cfg := runnerConfig(t,
		project.HookConfig{Name: "nested-cli", On: "item.created", Run: []string{binary, "-test.run=^TestNestedHookCLICall$"}},
		project.HookConfig{Name: "observe-note", On: "item.updated", If: "changed:notes", Run: []string{"/usr/bin/true"}},
		project.HookConfig{Name: "loop", On: "hook.finished", If: "outcome:ok", Run: []string{"/usr/bin/true"}},
	)
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Paths = paths
	cfg.Project.ID = "example/project"
	cfg.Settings.Project.ID = "example/project"
	cfg.Discovery.ControlRoot = root
	if err := hooks.RunEvent(context.Background(), cfg, automationEvent("causal-root", "item.created"), root); err != nil {
		t.Fatal(err)
	}
	logged, err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var nested hooks.Event
	for _, event := range logged {
		if event.Type == "item.updated" && event.Cause != nil && *event.Cause == "causal-root" {
			nested = event
		}
	}
	if nested.ID == "" || nested.Depth != 1 || nested.Origin != "hook" {
		t.Fatalf("nested bl event lost causal context: %+v", nested)
	}
	if err := hooks.RunEvent(context.Background(), cfg, nested, root); err != nil {
		t.Fatal(err)
	}
	finished := finishedEvents(t, cfg)
	if len(finished) != 7 {
		t.Fatalf("chain produced %d finished events, want 7: %+v", len(finished), finished)
	}
	for _, event := range finished {
		if event.Depth > 4 {
			t.Fatalf("hook chain exceeded causal depth 3: %+v", event)
		}
	}
}

// The child hook deliberately invokes the real CLI boundary. It only runs as a
// subprocess because the hook environment supplies BL_EVENT_ID and depth.
func TestNestedHookCLICall(t *testing.T) {
	if os.Getenv("BL_EVENT_ID") != "causal-root" {
		return
	}
	root, id := os.Getenv("BL_ROOT"), os.Getenv("BL_ITEM_ID")
	code := cli.Run(context.Background(), []string{"bl", "-C", root, "note", id, "nested hook write"}, cli.BuildInfo{}, strings.NewReader(""), io.Discard, io.Discard)
	if code != 0 {
		t.Fatalf("nested CLI write exit=%d", code)
	}
}

func putFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
