package cache

import (
	"context"
	"testing"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

// Catches an upgrade blanking the remote warm start, and the new build reusing
// a cursor or rows it did not write instead of re-enumerating.
func TestRemoteInitialAcrossBuilds(t *testing.T) {
	ctx := context.Background()
	store := New(t.TempDir(), "project", "remote", "account")
	item := model.Item{Ref: model.Ref{Source: "remote", ID: "A"}, Title: "A", Version: "v1"}
	previous := build
	t.Cleanup(func() { build = previous })
	build = "old"
	if _, err := store.RemoteRefresh(ctx, func(source.Cursor) (source.Delta, error) {
		return source.Delta{Items: []model.Item{item}, Coverage: source.Complete, Cursor: source.Cursor{Value: "old-cursor"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	build = "new"
	initial, err := store.RemoteInitial(ctx)
	if err != nil || len(initial.Items) != 1 || initial.Cursor.Value != "" || initial.Coverage != source.Unknown {
		t.Fatalf("upgrade warm start: %+v %v", initial, err)
	}
	var cursor source.Cursor
	refreshed, err := store.RemoteRefresh(ctx, func(c source.Cursor) (source.Delta, error) {
		cursor = c
		return source.Delta{Coverage: source.Complete, Cursor: source.Cursor{Value: "new-cursor"}}, nil
	})
	if err != nil || cursor.Value != "" || len(refreshed.Items) != 0 {
		t.Fatalf("refresh reused another build's state: cursor=%+v %+v %v", cursor, refreshed, err)
	}
}
