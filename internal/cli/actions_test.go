//go:build unix

package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/tui"
	"github.com/brettinternet/backlog/internal/tui/ptytest"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.yaml.in/yaml/v3"
)

type actionProbe struct {
	Env   map[string]string
	Event hooks.Event
}

// A real subprocess records the execution boundary, not adapter mocks.
func TestActionProbeProcess(t *testing.T) {
	if os.Getenv("BL_EVENT_ORIGIN") != "action" {
		return
	}
	var probe actionProbe
	probe.Env = map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		probe.Env[key] = value
	}
	if err := json.NewDecoder(os.Stdin).Decode(&probe.Event); err != nil {
		os.Exit(2)
	}
	data, _ := json.Marshal(probe)
	if err := os.WriteFile(filepath.Join(os.Getenv("BL_ROOT"), "probe.json"), data, 0600); err != nil {
		os.Exit(2)
	}
	fmt.Print("{\"path\":\"worktree\"}\n")
	os.Exit(0)
}

// Catches context/env drift across adapters, loss of terminal ownership,
// confirmation bypass, disabled expansion execution and missing finish events.
func TestActionsAcrossAdaptersAndPTY(t *testing.T) {
	root := writeFixture(t)
	cfg := eventConfig(t, root)
	cfg.Ready.Statuses = []string{"todo"}
	cfg.Project.ID = "action-integration"
	cfg.Actions = []project.ActionConfig{
		{Name: "probe", Key: "ctrl+x", When: "is:ready", Run: []string{os.Args[0], "-test.run=^TestActionProbeProcess$"}},
		{Name: "confirm", Key: "ctrl+y", Confirm: true, Run: []string{os.Args[0], "-test.run=^TestActionProbeProcess$"}},
		{Name: "resource", Run: []string{"must-not-run", "{resource}"}},
		{Name: "hidden", When: "status:done", Run: []string{"must-not-run"}},
		{Name: "detached", Key: "ctrl+e", Detach: true, Run: []string{"/bin/sh", "-c", `printf started > "$BL_ROOT/started"; read line < "$BL_ROOT/gate"; printf finished > "$BL_ROOT/finished"`}},
	}
	data, _ := yaml.Marshal(cfg.Settings)
	put(t, filepath.Join(root, ".config/backlog.yaml"), string(data))
	actionOK(t, root, "trust")
	cfg = eventConfig(t, root)
	choices, err := engine.ActionChoices(context.Background(), cfg, "12")
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 4 || choices[2].Name != "resource" || !strings.Contains(choices[2].Disabled, "resource") {
		t.Fatalf("choices: %+v", choices)
	}
	for _, name := range []string{"resource", "hidden", "confirm"} {
		code, out, _ := writeRun(root, "", "action", name, "12")
		if code == 0 {
			t.Fatalf("%s ran: %s", name, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "probe.json")); !os.IsNotExist(err) {
		t.Fatal("confirmation refusal ran probe")
	}
	conflict := append([]project.ActionConfig(nil), cfg.Actions...)
	conflict[0].Key = "j"
	if err := tui.ValidateKeys(nil, conflict...); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("key conflict: %v", err)
	}
	actionOK(t, root, "action", "probe", "12")
	var first actionProbe
	if err := json.Unmarshal([]byte(get(t, filepath.Join(root, "probe.json"))), &first); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "bl")
	if output, err := exec.Command("go", "build", "-o", binary, "../../cmd/bl").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "action-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.CommandTransport{Command: exec.Command(binary, "-C", root, "mcp")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "action", Arguments: map[string]any{"name": "confirm", "id": "12"}})
	if err != nil || !result.IsError {
		t.Fatalf("MCP confirmation: %+v %v", result, err)
	}
	result, err = session.CallTool(ctx, &sdk.CallToolParams{Name: "action", Arguments: map[string]any{"name": "probe", "id": "12"}})
	if err != nil || result.IsError {
		t.Fatalf("MCP action: %+v %v", result, err)
	}
	compareProbe(t, root, first)
	command := exec.Command(binary, "-C", root, "tui")
	command.Env = append(os.Environ(), "TERM=xterm-256color")
	pty, err := ptytest.Start(command, 100, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer pty.Close()
	if err := pty.WaitFor("BL-012", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := pty.Write("j"); err != nil {
		t.Fatal(err)
	}
	if _, err := pty.KeyToOutput("\x18", "Action ok.", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	compareProbe(t, root, first)
	// Exec released and re-entered the alternate screen, and keys work again.
	if !strings.Contains(pty.Output(), "\x1b[?1049l") || !strings.Contains(pty.Output(), "\x1b[?1049h") {
		t.Fatalf("no terminal suspend/resume: %q", pty.Output())
	}
	if _, err := pty.KeyToOutput("\x19", "Run confirm on tasks:BL-012?", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := pty.Write("n"); err != nil {
		t.Fatal(err)
	}
	if _, err := pty.KeyToOutput("!", "resource", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := pty.WaitFor("disabled:", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := pty.KeyToOutput("\x1b", "is:ready sort:next", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// A blocked detached action must not hold the terminal or the update loop.
	gate := filepath.Join(root, "gate")
	if output, err := exec.Command("mkfifo", gate).CombinedOutput(); err != nil {
		t.Fatalf("fifo: %v %s", err, output)
	}
	if _, err := pty.KeyToOutput("\x05", "Action started.", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := pty.KeyToOutput("?", "Move down / scroll detail", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	var gateFD int
	for {
		gateFD, err = syscall.Open(gate, syscall.O_WRONLY|syscall.O_NONBLOCK, 0600)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("detached action never opened gate: %v", err)
		}
	}
	_, _ = syscall.Write(gateFD, []byte("release\n"))
	_ = syscall.Close(gateFD)
	if _, err := pty.KeyToOutput("\x1b", "is:ready sort:next", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := pty.Write("q"); err != nil {
		t.Fatal(err)
	}
	waitForAction(t, cfg, "detached")
	events, err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		if event.Type == "action.finished" {
			counts[event.Name]++
			if event.Outcome != "ok" {
				t.Fatalf("finished: %+v", event)
			}
		}
	}
	if counts["probe"] != 3 || counts["detached"] != 1 || len(counts) != 2 {
		t.Fatalf("finish counts: %v", counts)
	}
	exerciseActionExamples(t, root, binary)
}

// Runs the literal YAML examples, catching documentation drift and broken
// action.finished -> agent result/cwd/resource handoff through real runners.
func exerciseActionExamples(t *testing.T, root, binary string) {
	t.Helper()
	bin := filepath.Join(root, "fake-bin")
	worktree := filepath.Join(root, "worktree")
	for _, dir := range []string{bin, worktree} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"wt", "worklease", "herdr", "tell"} {
		script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --json ]; then printf '%%s\\n' '{\"schemaVersion\":2,\"ok\":true,\"claims\":[]}'; exit; fi\nprintf 'cwd=%%s\\n' \"$PWD\" >> %q\nprintf '<%%s>\\n' \"$@\" >> %q\n", filepath.Join(root, name+"-args"), filepath.Join(root, name+"-args"))
		if name == "wt" {
			payload, _ := json.Marshal(map[string]string{"path": worktree})
			script += fmt.Sprintf("printf '%%s\\n' '%s'\n", payload)
		}
		path := filepath.Join(bin, name)
		put(t, path, script)
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	document := get(t, "../../docs/automation.md")
	for _, block := range strings.Split(document, "```yaml\n")[1:] {
		block, _, _ = strings.Cut(block, "```")
		var declarations project.Settings
		if err := yaml.Unmarshal([]byte(block), &declarations); err != nil {
			t.Fatal(err)
		}
		cfg := eventConfig(t, root)
		cfg.Actions, cfg.Hooks = declarations.Actions, declarations.Hooks
		cfg.Coordination.Worklease = &struct{}{}
		data, _ := yaml.Marshal(cfg.Settings)
		put(t, filepath.Join(root, ".config/backlog.yaml"), string(data))
		actionOK(t, root, "trust")
		for _, action := range cfg.Actions {
			output, err := exec.Command(binary, "-C", root, "--json", "action", action.Name, "12", "--yes").CombinedOutput()
			if err != nil {
				t.Fatalf("example %s: %v %s", action.Name, err, output)
			}
			if action.Name == "work" {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				found := false
				for ctx.Err() == nil && !found {
					events, err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Read(ctx, "")
					if err != nil {
						t.Fatal(err)
					}
					for _, e := range events {
						if e.Type == "hook.finished" && e.Name == "agent" {
							if e.Outcome != "ok" {
								t.Fatalf("agent hook: %+v", e)
							}
							found = true
						}
					}
				}
				cancel()
				if !found {
					t.Fatal("action.finished did not chain agent hook")
				}
				args := get(t, filepath.Join(root, "worklease-args"))
				physical, _ := filepath.EvalSymlinks(worktree)
				if !strings.Contains(args, "cwd="+physical) || !strings.Contains(args, "<coordination:generic:") {
					t.Fatalf("agent handoff: %s", args)
				}
			}
		}
	}
	for _, name := range []string{"wt", "worklease", "herdr"} {
		if !strings.Contains(get(t, filepath.Join(root, name+"-args")), "<") {
			t.Fatalf("example never ran %s", name)
		}
	}
}

func actionOK(t *testing.T, root string, args ...string) {
	t.Helper()
	if code, out, stderr := writeRun(root, "", args...); code != 0 {
		t.Fatalf("%v: %d %s %s", args, code, out, stderr)
	}
}

func compareProbe(t *testing.T, root string, want actionProbe) {
	t.Helper()
	var got actionProbe
	if err := json.Unmarshal([]byte(get(t, filepath.Join(root, "probe.json"))), &got); err != nil {
		t.Fatal(err)
	}
	normalize := func(p *actionProbe) {
		delete(p.Env, "BL_EVENT_ID")
		delete(p.Env, "BL_RESULT")
		p.Event.ID = ""
		p.Event.Time = time.Time{}
		p.Event.Versions = nil
	}
	normalize(&want)
	normalize(&got)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("adapter context differs:\nwant=%+v\ngot=%+v", want, got)
	}
}

func waitForAction(t *testing.T, cfg project.Config, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		events, err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Read(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Type == "action.finished" && event.Name == name {
				return
			}
		}
	}
	t.Fatal("action did not finish: " + name)
}
