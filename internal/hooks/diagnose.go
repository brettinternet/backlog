package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
)

// EventTypes are the event types bl appends; see docs/events.md.
var EventTypes = []string{"item.created", "item.updated", "item.archived", "item.readiness",
	"claim.acquired", "claim.released", "hook.finished", "action.finished"}

// Diagnostic is one static problem with a trusted hook or action, or an
// untrusted declaration that stays inert.
type Diagnostic struct {
	Message string
	Fix     string
	Inert   bool
}

// Diagnose checks trusted hooks and actions without running, expanding against
// an item, or probing anything beyond PATH lookup. It reports only what config
// load accepts, so the two never disagree. Untrusted declarations are not
// inspected at all.
func Diagnose(cfg project.Config) []Diagnostic {
	var out []Diagnostic
	for i, hook := range cfg.Hooks {
		label := fmt.Sprintf("hook %s", hook.Name)
		if cfg.RequireTrusted(fmt.Sprintf("hooks[%d]", i)) != nil {
			out = append(out, inert(label))
			continue
		}
		if !slices.Contains(EventTypes, hook.On) {
			out = append(out, Diagnostic{Message: fmt.Sprintf("%s: unknown event type %s", label, hook.On),
				Fix: "Set on to one of " + strings.Join(EventTypes, ", ")})
		}
		if _, err := hookTimeout(hook); err != nil {
			out = append(out, Diagnostic{Message: fmt.Sprintf("%s: invalid timeout %q", label, hook.Timeout),
				Fix: "Set timeout to a positive duration such as 30s"})
		}
		hasResult := hook.On == "hook.finished" || hook.On == "action.finished"
		out = append(out, diagnoseRun(cfg, label, hook.Run, hook.CWD, hasResult)...)
	}
	for i, action := range cfg.Actions {
		label := fmt.Sprintf("action %s", action.Name)
		if cfg.RequireTrusted(fmt.Sprintf("actions[%d]", i)) != nil {
			out = append(out, inert(label))
			continue
		}
		if action.When != "" {
			if _, err := query.Parse(action.When, query.Options{}); err != nil {
				out = append(out, Diagnostic{Message: fmt.Sprintf("%s: invalid when: %v", label, err), Fix: "Correct the when query"})
			}
		}
		out = append(out, diagnoseRun(cfg, label, action.Run, action.CWD, false)...)
	}
	return out
}

func inert(label string) Diagnostic {
	return Diagnostic{Message: label + " is untrusted and inert", Fix: "Review bl trust --show, then run bl trust", Inert: true}
}

func diagnoseRun(cfg project.Config, label string, run []string, cwd string, hasResult bool) []Diagnostic {
	var out []Diagnostic
	for _, value := range append(append([]string(nil), run...), cwd) {
		_, err := scanPlaceholders(value, func(key string) (string, error) {
			switch {
			case strings.HasPrefix(key, "result."):
				if !hasResult {
					return "", fmt.Errorf("{%s} is only set for hooks on hook.finished or action.finished", key)
				}
			case !placeholders[key]:
				return "", fmt.Errorf("unknown placeholder {%s}", key)
			case key == "resource" && !cfg.Coordination.Enabled():
				return "", fmt.Errorf("{resource} needs coordination, which is not configured")
			}
			return "x", nil
		})
		if err != nil {
			out = append(out, Diagnostic{Message: label + ": " + err.Error(),
				Fix: "Use only {id}, {ref}, {source}, {url}, {root}, {branch}, {resource}, or {result.KEY} where available"})
		}
	}
	if len(run) == 0 || run[0] == "" {
		return append(out, Diagnostic{Message: label + ": run is empty", Fix: "Set run to an executable and its arguments"})
	}
	if program := run[0]; !strings.Contains(program, "{") {
		if err := resolvable(program); err != nil {
			out = append(out, Diagnostic{Message: fmt.Sprintf("%s: %s: %v", label, program, err),
				Fix: "Install " + filepath.Base(program) + " or correct run[0]"})
		}
	}
	return out
}

// resolvable checks bare names on PATH and absolute paths. A relative path
// depends on the invoking directory, so it is left to run time.
func resolvable(program string) error {
	if !strings.ContainsRune(program, filepath.Separator) {
		_, err := exec.LookPath(program)
		if err != nil {
			return fmt.Errorf("not found on PATH")
		}
		return nil
	}
	if !filepath.IsAbs(program) {
		return nil
	}
	info, err := os.Stat(program)
	if err != nil {
		return fmt.Errorf("not found")
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("not executable")
	}
	return nil
}
