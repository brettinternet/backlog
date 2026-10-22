package native

import (
	"context"
	"sort"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

// LoadInitial returns complete items, but only a bounded part of the source.
// Missing dependency targets remain unknown until the full load arrives. The
// ordinary Load retains responsibility for cache refresh, FTS and findings.
func (s *Source) LoadInitial(ctx context.Context) (source.Delta, error) {
	paths, err := s.paths(false)
	if err != nil {
		return source.Delta{}, err
	}
	paths = paths[:min(256, len(paths))]
	var cached map[string]model.Item
	if s.options.CacheDir != "" {
		cached = cache.New(s.options.CacheDir, s.options.ProjectID, s.options.ID, s.options.Path).Initial(ctx, paths)
	}
	result := source.Delta{Coverage: source.Partial}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return source.Delta{}, err
		}
		item, ok := cached[path]
		if !ok {
			doc, err := s.document(path)
			if err != nil {
				continue // Full Load reports per-file findings.
			}
			item = s.item(doc)
		}
		if _, ok := s.options.Statuses[item.Status]; len(s.options.Statuses) > 0 && !ok {
			continue
		}
		item.Category = s.options.Statuses[item.Status]
		result.Items = append(result.Items, item)
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Ref.ID < result.Items[j].Ref.ID })
	return result, nil
}
