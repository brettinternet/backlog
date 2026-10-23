package engine

import (
	"context"
	"crypto/rand"
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
			Invocation: hooks.ActionInvocation{Name: action.Name, Event: event, CWD: eventCWD(ctx, cfg)}}
		if err := hooks.CheckAction(cfg, choice.Invocation); err != nil {
			choice.Disabled = err.Error()
		}
		choices = append(choices, choice)
	}
	return choices, nil
}

func PrepareAction(ctx context.Context, cfg project.Config, name, id string, confirmed bool) (ActionChoice, error) {
	choices, err := ActionChoices(ctx, cfg, id)
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

func ExecuteAction(ctx context.Context, cfg project.Config, choice ActionChoice) (hooks.ActionResult, error) {
	if choice.Detach {
		return hooks.StartAction(cfg, choice.Invocation)
	}
	return hooks.RunAction(ctx, cfg, choice.Invocation, nil, nil)
}
