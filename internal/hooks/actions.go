package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

// ActionInvocation is an unlogged invocation context, not a new event type.
// It is preserved when handing a detached action to the private runner. With
// Agent set, Name is resume or fork and the command comes from that agent
// kind's user config, never from the handoff (design.md D40).
type ActionInvocation struct {
	Name  string   `json:"name"`
	Event Event    `json:"event"`
	CWD   string   `json:"cwd"`
	Agent *Session `json:"agent,omitempty"`
}

type ActionResult struct {
	Event    Event  `json:"event"`
	Detached bool   `json:"detached,omitempty"`
	Message  string `json:"message,omitempty"`
}

// actionConfig resolves what an invocation runs: a trusted configured action,
// or an agent kind's resume or fork command, which only built-in and user
// layers can set.
func actionConfig(cfg project.Config, invocation ActionInvocation) (project.ActionConfig, error) {
	if s := invocation.Agent; s != nil {
		agent := cfg.Agents[s.Agent]
		run := agent.Resume
		switch invocation.Name {
		case "fork":
			run = agent.Fork
		case "resume":
		default:
			return project.ActionConfig{}, source.Refuse("rejected", "unknown agent command: "+invocation.Name)
		}
		if s.Agent == "" || len(run) == 0 {
			return project.ActionConfig{}, source.Refuse("no-agent-config", fmt.Sprintf("no %s command for agent %q", invocation.Name, s.Agent))
		}
		return project.ActionConfig{Name: invocation.Name, Run: run, Detach: agent.Detach}, nil
	}
	name := invocation.Name
	for i, action := range cfg.Actions {
		if action.Name == name {
			if err := cfg.RequireTrusted(fmt.Sprintf("actions[%d]", i)); err != nil {
				return action, source.Refuse("unavailable", err.Error())
			}
			return action, nil
		}
	}
	return project.ActionConfig{}, source.Refuse("not-found", "unknown action: "+name)
}

// agentValues are the agent placeholders, available only to agent commands.
func agentValues(invocation ActionInvocation) map[string]string {
	s := invocation.Agent
	if s == nil {
		return nil
	}
	return map[string]string{"agentSession": s.AgentSession, "session": s.ID, "agent": s.Agent, "cwd": s.CWD, "host": s.Host}
}

// CheckAction shares the executor's expansion and value checks without I/O.
func CheckAction(cfg project.Config, invocation ActionInvocation) error {
	action, err := actionConfig(cfg, invocation)
	if err != nil {
		return err
	}
	if invocation.Event.Depth < 0 || invocation.Event.Depth > 3 {
		return source.Refuse("rejected", "action causal depth exceeds 3")
	}
	if len(action.Run) == 0 || action.Run[0] == "" {
		return source.Refuse("rejected", "action run is empty")
	}
	for _, value := range append(append([]string(nil), action.Run...), action.CWD) {
		if _, err := expand(value, cfg, invocation.Event, agentValues(invocation)); err != nil {
			return source.Refuse("rejected", err.Error())
		}
	}
	return nil
}

// RunAction is the sole execution path for CLI, MCP and TUI actions. Terminal
// output can be forwarded by an attached caller; stdin is always context JSON.
func RunAction(ctx context.Context, cfg project.Config, invocation ActionInvocation, out, errOut io.Writer) (ActionResult, error) {
	return runInvocation(ctx, cfg, invocation, runOptions{out: out, errOut: errOut})
}

// RunAgent resumes or forks a recorded agent session attached to the caller's
// terminal, with no timeout, and finishes like an action.
func RunAgent(ctx context.Context, cfg project.Config, invocation ActionInvocation, terminal io.Reader, out, errOut io.Writer) (ActionResult, error) {
	if invocation.Agent == nil {
		return ActionResult{}, source.Refuse("rejected", "not an agent session command")
	}
	return runInvocation(ctx, cfg, invocation, runOptions{out: out, errOut: errOut, terminal: terminal})
}

func runInvocation(ctx context.Context, cfg project.Config, invocation ActionInvocation, opts runOptions) (ActionResult, error) {
	action, err := actionConfig(cfg, invocation)
	if err != nil {
		return ActionResult{}, err
	}
	opts.extra = agentValues(invocation)
	result := execution{outcome: "refused", exitCode: -1}
	if err := CheckAction(cfg, invocation); err != nil {
		result.message = err.Error()
	} else {
		result = executeWithOutput(ctx, cfg, project.HookConfig{Name: action.Name, Run: action.Run, CWD: action.CWD}, invocation.Event, invocation.CWD, true, opts)
	}
	event := invocation.Event
	finished := Event{Type: "action.finished", Project: event.Project, Source: event.Source,
		Ref: event.Ref, Resource: event.Resource, Item: event.Item, Actor: event.Actor,
		Origin: "action", Cause: stringPointer(event.ID), Depth: event.Depth + 1,
		Name: action.Name, Outcome: result.outcome, ExitCode: intPointer(result.exitCode),
		Duration: result.duration, Result: result.result, Message: finishedMessage(result),
		Versions: []string{event.ID, action.Name}, Session: event.Session}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultTimeout)
	defer cancel()
	var appended []Event
	err = New(cfg.Paths.StateDir, cfg.Project.ID).Update(finishCtx, func(tx *Transaction) error {
		_, err := tx.Append(finished)
		appended = tx.Appended()
		return err
	})
	if err != nil {
		return ActionResult{}, err
	}
	if len(appended) > 0 {
		finished = appended[0]
		if err := StartRunner(cfg, appended, invocation.CWD); err != nil {
			return ActionResult{Event: finished, Message: result.message}, err
		}
	}
	return ActionResult{Event: finished, Message: result.message}, nil
}

// ActionPreview is what an action would run. Env lists names only.
type ActionPreview struct {
	Name string   `json:"name"`
	Ref  string   `json:"ref"`
	Argv []string `json:"argv"`
	CWD  string   `json:"cwd"`
	Env  []string `json:"env"`
}

// PreviewAction stops the shared execution path before any process, file, or
// event is created.
func PreviewAction(cfg project.Config, invocation ActionInvocation) (ActionPreview, error) {
	if err := CheckAction(cfg, invocation); err != nil {
		return ActionPreview{}, err
	}
	action, _ := actionConfig(cfg, invocation)
	run, failure := prepare(cfg, project.HookConfig{Name: action.Name, Run: action.Run, CWD: action.CWD}, invocation.Event, invocation.CWD, runOptions{extra: agentValues(invocation)})
	if failure.outcome == "refused" {
		return ActionPreview{}, source.Refuse("rejected", failure.message)
	} else if failure.outcome != "" {
		return ActionPreview{}, errors.New(failure.message)
	}
	preview := ActionPreview{Name: action.Name, Ref: invocation.Event.Ref, Argv: run.argv, CWD: run.cwd}
	for _, entry := range hookEnvironment(cfg, invocation.Event, "") {
		name, _, _ := strings.Cut(entry, "=")
		preview.Env = append(preview.Env, name)
	}
	return preview, nil
}

// StartAction hands the already prepared context to a detached bl process.
func StartAction(cfg project.Config, invocation ActionInvocation) (ActionResult, error) {
	if err := CheckAction(cfg, invocation); err != nil {
		return ActionResult{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return ActionResult{}, err
	}
	file, err := os.CreateTemp("", "bl-action-*.json")
	if err != nil {
		return ActionResult{}, err
	}
	path := file.Name()
	if err = json.NewEncoder(file).Encode(invocation); err != nil {
		file.Close()
		os.Remove(path)
		return ActionResult{}, err
	}
	if err = file.Close(); err != nil {
		os.Remove(path)
		return ActionResult{}, err
	}
	cmd := exec.Command(executable, "-C", invocation.CWD, "hooks", "action", path)
	cmd.Dir = invocation.CWD
	if err = startDetached(cmd); err != nil {
		os.Remove(path)
		return ActionResult{}, err
	}
	return ActionResult{Detached: true}, nil
}

// ReadActionInvocation consumes the private runner handoff, bounded independently
// of the task's description size, and always removes its temporary file.
func ReadActionInvocation(path string) (ActionInvocation, error) {
	var invocation ActionInvocation
	clean := filepath.Clean(path)
	if filepath.Dir(clean) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(clean), "bl-action-") || !strings.HasSuffix(clean, ".json") {
		return invocation, fmt.Errorf("invalid action handoff path")
	}
	defer os.Remove(clean)
	file, err := os.Open(path)
	if err != nil {
		return invocation, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	if err := decoder.Decode(&invocation); err != nil {
		return invocation, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invocation, fmt.Errorf("invalid action handoff")
	}
	return invocation, nil
}
