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

// Linear bumps only the target of a created or retyped relation (BL-90), so the
// source's own Blocks and Related stay stale until reconciliation. Queries and
// relation rows must follow the fresh target instead.
func TestProjectionDerivesSourceRelations(t *testing.T) {
	t.Parallel()
	items := projectionItems(7)
	ref := func(n int) model.Ref { return items[n-1].Ref }
	external := model.Ref{Source: "tasks", ID: "EXT-1"}
	items[1].BlockedBy = []model.Ref{ref(1)} // created: BL-1 blocks BL-2
	items[2].Blocks = []model.Ref{ref(4)}    // retyped blocks->related, BL-3 stale
	items[3].Related = []model.Ref{ref(3)}
	items[4].Related = []model.Ref{ref(6)} // retyped related->blocks, BL-5 stale
	items[5].BlockedBy = []model.Ref{ref(5)}
	items[6].Blocks = []model.Ref{external} // target not loaded keeps the raw ref
	s := engine.OpenStore(context.Background(), storeConfig(), source.Registry{"tasks": projectionSource{items}})
	defer s.Close()
	snapshot := awaitSnapshot(t, s, func(s *engine.Snapshot) bool { return s.Len() == len(items) })
	p := engine.NewProjector(context.Background(), storeConfig())
	defer p.Close()
	project := func(text string) map[string][]engine.Relation {
		t.Helper()
		id := p.RequestQuery(snapshot, engine.ProjectionQuery{Text: text})
		for {
			select {
			case result := <-p.Results():
				if result.Request != id {
					continue
				}
				if result.Error != nil {
					t.Fatal(result.Error)
				}
				rows := map[string][]engine.Relation{}
				for _, g := range result.Groups {
					for _, row := range g.Rows {
						rows[row.Ref.ID] = row.Relations
					}
				}
				return rows
			case <-time.After(10 * time.Second):
				t.Fatal("projection stalled")
			}
		}
	}
	for text, want := range map[string]string{"blocks:BL-2": "BL-1", "blocks:BL-4": "", "blocks:BL-6": "BL-5", "blocks:EXT-1": "BL-7"} {
		got := ""
		for id := range project(text) {
			got += id
		}
		if got != want {
			t.Errorf("%s matched %q, want %q", text, got, want)
		}
	}
	kinds := func(relations []engine.Relation) string {
		out := ""
		for _, relation := range relations {
			out += relation.Kind + " " + relation.Ref.ID + ";"
		}
		return out
	}
	rows := project("")
	for id, want := range map[string]string{
		"BL-1": "blocking BL-2;",
		"BL-3": "related BL-4;",
		"BL-4": "related BL-3;",
		"BL-5": "blocking BL-6;",
		"BL-6": "blocked by BL-5;",
		"BL-7": "blocking EXT-1;",
	} {
		if got := kinds(rows[id]); got != want {
			t.Errorf("%s relations %q, want %q", id, got, want)
		}
	}
}
