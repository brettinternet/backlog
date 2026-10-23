package engine

import (
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

func workspaceSnapshot(cfg project.Config, snapshot *Snapshot, scope string) *Snapshot {
	out := &Snapshot{Generation: snapshot.Generation, records: map[model.Ref]*record{}, sources: snapshot.sources}
	items := make([]model.Item, 0, snapshot.Len())
	for _, r := range snapshot.records {
		if scope == "" || r.item.Project == scope {
			items = append(items, *r.item)
		}
	}
	for _, item := range workspaceItems(cfg, items) {
		r := *snapshot.records[item.Ref]
		r.item = &item
		out.records[item.Ref] = &r
	}
	return out
}
