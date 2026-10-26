package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
)

// WantsSprints reports whether ready.sprint, a saved view, an action's when, or
// one of queries uses sprints. Providers make no sprint requests otherwise.
func WantsSprints(cfg project.Config, queries ...string) bool {
	ready := []project.ReadyConfig{cfg.Ready}
	if cfg.Workspace != nil {
		for _, p := range cfg.Workspace.Projects {
			ready = append(ready, p.Ready)
		}
	}
	for _, r := range ready {
		if r.Sprint != "" {
			return true
		}
	}
	for _, view := range cfg.Views {
		queries = append(queries, view.Query, "group:"+view.Group)
	}
	for _, action := range cfg.Actions {
		queries = append(queries, action.When)
	}
	for _, text := range queries {
		if MentionsSprint(text) {
			return true
		}
	}
	return false
}

// MentionsSprint reports whether text filters or groups by sprint. It is cheap
// enough for the UI loop; unparsable text mentions nothing.
func MentionsSprint(text string) bool {
	q, err := query.Parse(text, query.Options{})
	if err != nil {
		return false
	}
	for _, t := range q.Terms {
		if t.Field == "sprint" {
			return true
		}
	}
	return q.Group == "sprint"
}

func sprintSource(cfg project.Config, id string) bool {
	for _, c := range cfg.Sources {
		if c.ID == id {
			return c.Type == "linear" || c.Type == "jira"
		}
	}
	return false
}

// resolveSprintWrite turns sprint=VALUE into a catalog key from a fresh
// sprint-enabled load, and binds the sprint the caller observes when it did
// not supply one. Providers recheck that sprint immediately before dispatch.
func resolveSprintWrite(ctx context.Context, cfg project.Config, backend source.Source, m model.Mutation) (model.Mutation, error) {
	changes := []*model.Mutation{&m}
	if m.Op == model.Batch {
		m.Changes = slices.Clone(m.Changes)
		changes = changes[:0]
		for n := range m.Changes {
			changes = append(changes, &m.Changes[n])
		}
	}
	var catalog []model.Sprint
	var observed *string
	for _, change := range changes {
		value, ok := change.Value.(string)
		if change.Field != "sprint" || change.Op != model.Set || !ok || !sprintSource(cfg, m.Ref.Source) {
			continue
		}
		if catalog == nil {
			delta, err := loadObservation(ctx, backend, source.Cursor{Sprints: true})
			if err != nil {
				return m, err
			}
			catalog = delta.Sprints
		}
		key, err := ResolveSprint(catalog, value)
		if err != nil {
			return m, err
		}
		change.Value = key
		if change.ExpectedSprint == nil {
			if observed == nil {
				item, err := backend.Read(ctx, m.Ref.ID)
				if err != nil {
					return m, err
				}
				observed = &item.Sprint
			}
			change.ExpectedSprint = new(*observed)
		}
	}
	return m, nil
}

// ResolveSprint maps current, next, none (or empty), a key, or a name to a
// catalog key. Writes never target a closed sprint.
func ResolveSprint(catalog []model.Sprint, value string) (string, error) {
	role := strings.ToLower(strings.TrimSpace(value))
	if role == "" || role == "none" {
		return "", nil
	}
	snapshot := query.Snapshot{Sprints: catalog}
	var matches []model.Sprint
	for _, sprint := range catalog {
		if sprint.Key == value {
			matches = []model.Sprint{sprint}
			break
		}
		_, state, _ := snapshot.Sprint(sprint.Key)
		if role == "current" || role == "next" {
			if state == role {
				matches = append(matches, sprint)
			}
		} else if strings.EqualFold(sprint.Name, strings.TrimSpace(value)) {
			matches = append(matches, sprint)
		}
	}
	switch {
	case len(matches) == 0:
		return "", source.Refuse("rejected", fmt.Sprintf("unknown sprint %q; use current, next, none, or a sprint name or key", value))
	case len(matches) > 1:
		return "", source.Refuse("rejected", fmt.Sprintf("sprint %q is ambiguous; use its key", value))
	case matches[0].State == model.SprintClosed:
		return "", source.Refuse("rejected", fmt.Sprintf("sprint %q is closed", matches[0].Name))
	}
	return matches[0].Key, nil
}
