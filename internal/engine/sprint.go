package engine

import (
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
)

// WantsSprints reports whether ready.sprint, a saved view, or one of queries
// uses sprints. Providers make no sprint requests otherwise.
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
