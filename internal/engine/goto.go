package engine

import (
	"context"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

// GoToItems loads the ID/title catalog through the native stat-validated cache,
// including archive/. Call from a worker, never from a UI callback. Unlike a
// normal projection this does not exclude closed items or compute readiness.
func GoToItems(ctx context.Context, cfg project.Config) ([]Row, error) {
	var rows []Row
	for _, c := range cfg.Sources {
		backend, err := readSource(cfg, c)
		if err != nil {
			return nil, err
		}
		delta, err := loadObservation(ctx, backend, source.Cursor{IncludeArchive: true})
		if err != nil {
			return nil, err
		}
		for _, item := range delta.Items {
			row := Row{Ref: item.Ref, Title: item.Title, Version: item.Version, Status: item.Status, Category: item.Category, Priority: item.Priority, Names: item.Names}
			row.SetCounts(item)
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// CachedItems loads items for shell completion without running a program or
// contacting a provider (design.md D16): native and Backlog.md files through
// their stat-validated caches, and remote sources' cached rows. External and
// Beads sources run programs, so they're skipped, as are untrusted sources and
// any source that fails to load.
func CachedItems(ctx context.Context, cfg project.Config) []model.Item {
	var items []model.Item
	for _, c := range cfg.Sources {
		switch c.Type {
		case "native", "backlog-md", "github", "github-pulls", "linear", "jira":
		default:
			continue
		}
		backend, err := rawSource(cfg, c)
		if err != nil {
			continue
		}
		var delta source.Delta
		if cached, ok := backend.(*remote.Source); ok {
			delta, err = cached.LoadInitial(ctx)
		} else {
			delta, err = backend.Load(ctx, source.Cursor{})
		}
		if err == nil {
			items = append(items, delta.Items...)
		}
	}
	return items
}
