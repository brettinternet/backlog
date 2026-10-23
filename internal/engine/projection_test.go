package engine_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
)

type projectionSource struct{ items []model.Item }

func (s projectionSource) Info() source.Info { return source.Info{ID: "tasks"} }
func (s projectionSource) Load(context.Context, source.Cursor) (source.Delta, error) {
	return source.Delta{Items: s.items, Coverage: source.Complete}, nil
}
func (s projectionSource) Read(context.Context, string) (model.Item, error) {
	return model.Item{}, errors.New("unused")
}
func (s projectionSource) Apply(context.Context, model.Mutation) (model.Item, error) {
	return model.Item{}, errors.New("unused")
}
func projectionItems(n int) []model.Item {
	items := make([]model.Item, n)
	for i := range items {
		items[i] = model.Item{Ref: model.Ref{Source: "tasks", ID: fmt.Sprintf("BL-%d", i+1)}, Title: fmt.Sprintf("Task %d", i+1), Status: "todo", Category: model.Todo, Version: "1"}
	}
	return items
}
func TestLatestProjectionAndCancellation(t *testing.T) {
	t.Parallel()
	items := projectionItems(50_000)
	s := engine.OpenStore(context.Background(), storeConfig(), source.Registry{"tasks": projectionSource{items}})
	defer s.Close()
	snapshot := awaitSnapshot(t, s, func(s *engine.Snapshot) bool { return s.Len() == len(items) })
	p := engine.NewProjector(context.Background(), storeConfig())
	defer p.Close()
	// A burst supersedes work even when the worker has started the prior query.
	for range 50 {
		p.RequestQuery(snapshot, engine.ProjectionQuery{Text: "sort:title group:status", Complete: true, Field: "id", Prefix: "BL-"})
	}
	id := p.RequestQuery(snapshot, engine.ProjectionQuery{Text: "id:BL-49999", Complete: true, Field: "id", Prefix: "BL-49999"})
	select {
	case result := <-p.Results():
		if result.Request != id || result.Error != nil || len(result.Groups) != 1 || len(result.Groups[0].Rows) != 1 || result.Groups[0].Rows[0].Ref.ID != "BL-49999" {
			t.Fatalf("stale or incorrect projection: %+v", result)
		}
		if len(result.Completions) != 1 || result.Completions[0].Value != "BL-49999" {
			t.Fatalf("stale or incorrect completions: %v", result.Completions)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("projection stalled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q, err := query.Parse("sort:title group:status", query.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if groups, err := q.ProjectContext(ctx, query.Snapshot{Items: items}, nil); !errors.Is(err, context.Canceled) || groups != nil {
		t.Fatalf("canceled result: %v %v", len(groups), err)
	}
}
