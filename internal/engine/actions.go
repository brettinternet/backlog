package engine

import (
	"context"
	"crypto/rand"
	"slices"
	"time"

	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
)

type ActionChoice struct {
	Name       string                 `json:"name"`
	Key        string                 `json:"key,omitempty"`
	Confirm    bool                   `json:"confirm"`
	Detach     bool                   `json:"detach"`
	Disabled   string                 `json:"disabled,omitempty"`
	Invocation hooks.ActionInvocation `json:"-"`
}

// ActionChoices evaluates when against the same complete snapshot as CLI queries.
// Callers run this on a worker, never on the TUI update loop.
func ActionChoices(ctx context.Context, cfg project.Config, id string) ([]ActionChoice, error) {
	return actionChoices(ctx, cfg, id, nil)
}

// ChangeActionChoices are the actions that name a {change.*} placeholder,
// prepared to run on change (design.md D43).
func ChangeActionChoices(ctx context.Context, cfg project.Config, id string, change hooks.LinkedChange) ([]ActionChoice, error) {
	choices, err := actionChoices(ctx, cfg, id, &change)
	return slices.DeleteFunc(choices, func(c ActionChoice) bool {
		i := slices.IndexFunc(cfg.Actions, func(a project.ActionConfig) bool { return a.Name == c.Name })
		return !hooks.UsesChange(cfg.Actions[i])
	}), err
}

func actionChoices(ctx context.Context, cfg project.Config, id string, change *hooks.LinkedChange) ([]ActionChoice, error) {
	reads, err := LoadReads(ctx, cfg, false)
	if err != nil {
		return nil, err
	}
	items, err := ReadDetail(ctx, cfg, id)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, source.Refuse("not-found", "item not found: "+id)
	}
	if len(items) != 1 {
		return nil, source.Refuse("rejected", "ambiguous item ID: "+id)
	}
	item := items[0]
	var choices []ActionChoice
	names := map[string]bool{}
	for _, action := range cfg.Actions {
		if action.Name == "" || names[action.Name] {
			return nil, source.Refuse("invalid-config", "action names must be nonempty and unique")
		}
		names[action.Name] = true
		if action.When != "" {
			q, err := query.Parse(action.When, query.Options{})
			if err != nil {
				return nil, source.Refuse("invalid-config", err.Error())
			}
			if !q.Match(item, reads.Query, nil) {
				continue
			}
		}
		event := makeEvent(ctx, cfg, item, "", "action", []string{rand.Text()})
		event.ID = rand.Text()
		event.SchemaVersion = 1
		event.Time = time.Now().UTC()
		event.Name = action.Name
		choice := ActionChoice{Name: action.Name, Key: action.Key, Confirm: action.Confirm, Detach: action.Detach,
			Invocation: hooks.ActionInvocation{Name: action.Name, Event: event, CWD: eventCWD(ctx, cfg), Change: change}}
		if err := hooks.CheckAction(cfg, choice.Invocation); err != nil {
			choice.Disabled = err.Error()
		}
		if change == nil && hooks.UsesChange(action) {
			choice.Disabled = "runs on a linked change"
		}
		choices = append(choices, choice)
	}
	return choices, nil
}

func PrepareAction(ctx context.Context, cfg project.Config, name, id string, confirmed bool) (ActionChoice, error) {
	choices, err := ActionChoices(ctx, cfg, id)
	return prepareChoice(choices, err, name, confirmed)
}

// PrepareChangeAction resolves an action with change selected, as the TUI's
// Changes tab does; an action that names no {change.*} placeholder ignores it.
func PrepareChangeAction(ctx context.Context, cfg project.Config, name, id string, change hooks.LinkedChange) (ActionChoice, error) {
	choices, err := actionChoices(ctx, cfg, id, &change)
	return prepareChoice(choices, err, name, true)
}

func prepareChoice(choices []ActionChoice, err error, name string, confirmed bool) (ActionChoice, error) {
	if err != nil {
		return ActionChoice{}, err
	}
	for _, choice := range choices {
		if choice.Name != name {
			continue
		}
		if choice.Disabled != "" {
			return choice, source.Refuse("rejected", choice.Disabled)
		}
		if choice.Confirm && !confirmed {
			return choice, source.Refuse("confirmation-required", "action requires confirmation")
		}
		return choice, nil
	}
	return ActionChoice{}, source.Refuse("unavailable", "action is unavailable for this item: "+name)
}

// Action runs the shared executor, or hands its exact invocation to a detached
// runner. Noninteractive clients must explicitly acknowledge confirm actions.
func Action(ctx context.Context, cfg project.Config, name, id string, confirmed bool) (hooks.ActionResult, error) {
	choice, err := PrepareAction(ctx, cfg, name, id, confirmed)
	if err != nil {
		return hooks.ActionResult{}, err
	}
	return ExecuteAction(ctx, cfg, choice)
}

// PreviewAction resolves an action exactly as Action would, without
// confirmation, and returns what it would run instead of running it.
func PreviewAction(ctx context.Context, cfg project.Config, name, id string) (hooks.ActionPreview, error) {
	choice, err := PrepareAction(ctx, cfg, name, id, true)
	if err != nil {
		return hooks.ActionPreview{}, err
	}
	return hooks.PreviewAction(cfg, choice.Invocation)
}

func ExecuteAction(ctx context.Context, cfg project.Config, choice ActionChoice) (hooks.ActionResult, error) {
	if choice.Detach {
		return hooks.StartAction(cfg, choice.Invocation)
	}
	return hooks.RunAction(ctx, cfg, choice.Invocation, nil, nil)
}
