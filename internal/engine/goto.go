package engine

import (
	"context"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
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
			row := Row{Ref: item.Ref, Title: item.Title, Version: item.Version, Status: item.Status, Category: item.Category, Priority: item.Priority}
			row.SetCounts(item)
			rows = append(rows, row)
		}
	}
	return rows, nil
}
