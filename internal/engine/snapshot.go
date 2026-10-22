package engine

import (
	"maps"
	"slices"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

// EditState retains failures until the next edit, including both versions of a
// conflict. Returned values are owned by the caller, not the snapshot.
type EditState struct {
	Pending       bool
	Error         string
	Local, Remote *model.Item
}
type record struct {
	item     model.Item
	edit     EditState
	archived bool
}
type SourceState struct {
	Coverage source.Coverage
	Error    string
}

// Snapshot is immutable. Successive snapshots share unchanged item records.
// Obtaining a snapshot is O(1); only projection workers enumerate its contents.
type Snapshot struct {
	Generation uint64
	records    map[model.Ref]*record
	sources    map[string]SourceState
}

func (s *Snapshot) Len() int { return len(s.records) }

// Items returns active snapshot items. It is intended for background work such
// as picker candidate construction, not the Bubble Tea update loop.
func (s *Snapshot) Items() []model.Item {
	items := make([]model.Item, 0, len(s.records))
	for _, r := range s.records {
		if !r.archived {
			items = append(items, cloneItem(r.item))
		}
	}
	return items
}

func (s *Snapshot) Get(ref model.Ref) (model.Item, EditState, bool) {
	r, ok := s.records[ref]
	if !ok {
		return model.Item{}, EditState{}, false
	}
	e := r.edit
	if e.Local != nil {
		v := cloneItem(*e.Local)
		e.Local = &v
	}
	if e.Remote != nil {
		v := cloneItem(*e.Remote)
		e.Remote = &v
	}
	return cloneItem(r.item), e, true
}
func (s *Snapshot) Source(id string) SourceState { return s.sources[id] }
func cloneValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = cloneValue(x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for k, x := range v {
			out[k] = cloneValue(x)
		}
		return out
	case []string:
		return slices.Clone(v)
	default:
		return v
	}
}
func cloneItem(i model.Item) model.Item {
	i.Assignees = slices.Clone(i.Assignees)
	i.Labels = slices.Clone(i.Labels)
	i.Children = slices.Clone(i.Children)
	i.BlockedBy = slices.Clone(i.BlockedBy)
	i.Blocks = slices.Clone(i.Blocks)
	i.Related = slices.Clone(i.Related)
	i.Edges = slices.Clone(i.Edges)
	i.Refs = slices.Clone(i.Refs)
	i.Files = slices.Clone(i.Files)
	i.Criteria = slices.Clone(i.Criteria)
	i.DoD = slices.Clone(i.DoD)
	i.Comments = slices.Clone(i.Comments)
	if i.Parent != nil {
		p := *i.Parent
		i.Parent = &p
	}
	if i.DuplicateOf != nil {
		p := *i.DuplicateOf
		i.DuplicateOf = &p
	}
	if i.Fields != nil {
		i.Fields = cloneValue(i.Fields).(map[string]any)
	}
	return i
}
func cloneMutation(m model.Mutation) model.Mutation {
	m.Value = cloneValue(m.Value)
	m.Original = slices.Clone(m.Original)
	if m.Item != nil {
		i := cloneItem(*m.Item)
		m.Item = &i
	}
	if m.Evidence != nil {
		e := *m.Evidence
		m.Evidence = &e
	}
	m.Changes = slices.Clone(m.Changes)
	for n := range m.Changes {
		m.Changes[n] = cloneMutation(m.Changes[n])
	}
	return m
}
func (s *Snapshot) fork() *Snapshot {
	return &Snapshot{Generation: s.Generation + 1, records: maps.Clone(s.records), sources: maps.Clone(s.sources)}
}

// latest replaces an unread value. Producers serialize calls; readers never
// send. A slow or abandoned subscriber cannot stall a worker.
func latest[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- v:
		default:
		}
	}
}
