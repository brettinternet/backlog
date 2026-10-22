package engine

import (
	"context"
	"fmt"

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
		delta, err := backend.Load(ctx, source.Cursor{IncludeArchive: true})
		if err != nil {
			return nil, err
		}
		if delta.Coverage != source.Complete {
			return nil, fmt.Errorf("source %s has incomplete coverage: %v", c.ID, delta.Findings)
		}
		for _, item := range delta.Items {
			rows = append(rows, Row{Ref: item.Ref, Title: item.Title, Version: item.Version, Status: item.Status, Priority: item.Priority})
		}
	}
	return rows, nil
}
