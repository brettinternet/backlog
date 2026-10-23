package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

// A scope toggle must not change stable selection or resolve blockers against
// projects outside the loaded scope. The colliding bare IDs expose bad keys.
func TestWorkspaceScopeSelection(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	projects := make([]project.Config, 3)
	aliases := []string{"a/tasks", "b/upstream", "c/tasks"}
	registry := source.Registry{}
	ws := &project.Workspace{Name: "all", Project: "a", Sources: map[string]project.WorkspaceSource{}}
	for n, id := range []string{"a", "b", "c"} {
		projects[n] = project.Config{Settings: project.Settings{Project: project.ProjectConfig{ID: id}, Ready: project.ReadyConfig{Statuses: []string{"todo"}, SatisfiedBy: []string{"done"}}}}
		local := "tasks"
		if n == 1 {
			local = "upstream"
		}
		ws.Sources[aliases[n]] = project.WorkspaceSource{Project: n, ID: local}
		item := model.Item{Project: id, Ref: model.Ref{Source: aliases[n], ID: "BL-1"}, Title: id + " item", Status: "todo", Category: model.Todo, Version: "1"}
		if n == 0 {
			item.BlockedBy = []model.Ref{{Source: "upstream", ID: "BL-1"}}
		}
		if n == 1 {
			item.Status = "finished"
			item.Category = model.Done
		}
		registry[aliases[n]] = querySource{items: []model.Item{item}}
	}
	ws.Projects = projects
	cfg := projects[0]
	cfg.Workspace = ws
	store := engine.OpenStore(ctx, cfg, registry)
	defer store.Close()
	projector := engine.NewProjector(ctx, cfg)
	defer projector.Close()
	snapshots, unsub := store.Subscribe()
	defer unsub()
	m := New(Options{Context: ctx, Workspace: &cfg, Projector: projector, NoColor: true, Project: "a"})
	for m.snapshot == nil || m.snapshot.Len() != 3 {
		select {
		case m.snapshot = <-snapshots:
		case <-ctx.Done():
			t.Fatal("source deadline")
		}
	}
	m.query = "is:open group:none"
	m.width = 160
	m.project()
	m = settleQuery(t, m)
	row, ok := m.selectedRow()
	if !ok || row.Ref.Source != aliases[0] || row.Readiness != engine.Unknown {
		t.Fatalf("project scope: %+v", row)
	}
	m = send(m, "W")
	m = settleQuery(t, m)
	row, ok = m.selectedRow()
	if !ok || row.Ref.Source != aliases[0] || row.Readiness != engine.Ready || m.data.items != 2 {
		t.Fatalf("workspace scope: %+v count=%d", row, m.data.items)
	}
	rendered := m.View().Content
	if !strings.Contains(rendered, "Project") || !strings.Contains(rendered, "Source") || !strings.Contains(rendered, "tasks") {
		t.Fatalf("workspace columns: %s", rendered)
	}
	m = send(m, "W")
	m = settleQuery(t, m)
	row, ok = m.selectedRow()
	if !ok || row.Ref.Source != aliases[0] || row.Readiness != engine.Unknown || m.data.items != 1 {
		t.Fatalf("restored project scope: %+v count=%d", row, m.data.items)
	}
}
