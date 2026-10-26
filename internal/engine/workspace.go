package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/worklease"
)

type workspaceSource struct {
	source.Source
	cfg       project.Config
	workspace project.Config
	id, local string
}

func openWorkspaceSource(cfg project.Config, id string) (source.Source, error) {
	p, local := cfg.SourceProject(id)
	if p.Workspace != nil {
		return nil, fmt.Errorf("unknown workspace source %s", id)
	}
	for _, c := range p.Sources {
		if c.ID != local {
			continue
		}
		backend, err := readSource(p, c)
		if err != nil {
			return nil, err
		}
		return &workspaceSource{Source: backend, cfg: p, workspace: cfg, id: id, local: local}, nil
	}
	return nil, fmt.Errorf("unknown workspace source %s", id)
}
func (s *workspaceSource) Info() source.Info { info := s.Source.Info(); info.ID = s.id; return info }
func (s *workspaceSource) item(i model.Item) model.Item {
	i.Ref.Source = s.id
	i.Project = s.cfg.Project.ID
	return i
}
func (s *workspaceSource) mutation(m model.Mutation) model.Mutation {
	m = cloneMutation(m)
	localize := func(value string) string {
		source, id, ok := strings.Cut(value, ":")
		if !ok {
			return value
		}
		p, local := s.workspace.SourceProject(source)
		if p.Workspace == nil && p.Project.ID == s.cfg.Project.ID {
			return local + ":" + id
		}
		return value
	}
	if m.Field == "parent" || m.Field == "duplicate_of" || m.Field == "blocked_by" || m.Field == "related" {
		switch value := m.Value.(type) {
		case string:
			m.Value = localize(value)
		case []string:
			for n := range value {
				value[n] = localize(value[n])
			}
			m.Value = value
		}
	}
	if m.Ref.Source == s.id {
		m.Ref.Source = s.local
	}
	if m.Item != nil {
		m.Item.Ref.Source = s.local
		m.Item.Project = ""
		localRef := func(r model.Ref) model.Ref {
			if r.Source != "" {
				value := localize(r.Source + ":" + r.ID)
				r.Source, r.ID, _ = strings.Cut(value, ":")
			}
			return r
		}
		for _, refs := range [][]model.Ref{m.Item.BlockedBy, m.Item.Blocks, m.Item.Children, m.Item.Related} {
			for n := range refs {
				refs[n] = localRef(refs[n])
			}
		}
		if m.Item.Parent != nil {
			*m.Item.Parent = localRef(*m.Item.Parent)
		}
		if m.Item.DuplicateOf != nil {
			*m.Item.DuplicateOf = localRef(*m.Item.DuplicateOf)
		}
	}
	for n := range m.Changes {
		m.Changes[n] = s.mutation(m.Changes[n])
	}
	return m
}
func (s *workspaceSource) delta(d source.Delta) source.Delta {
	d.Items = append([]model.Item(nil), d.Items...)
	d.ImmutableItems = nil
	for n := range d.Items {
		d.Items[n] = s.item(d.Items[n])
	}
	qualify := func(in map[model.Ref]bool) map[model.Ref]bool {
		out := make(map[model.Ref]bool, len(in))
		for r, value := range in {
			r.Source = s.id
			out[r] = value
		}
		return out
	}
	d.Archived, d.Stale = qualify(d.Archived), qualify(d.Stale)
	if search := d.TextSearch; search != nil {
		d.TextSearch = func(ctx context.Context, text string) (map[model.Ref]bool, error) {
			refs, err := search(ctx, text)
			if refs == nil {
				return nil, err
			}
			return qualify(refs), err
		}
	}
	return d
}
func (s *workspaceSource) Load(ctx context.Context, c source.Cursor) (source.Delta, error) {
	d, err := s.Source.Load(ctx, c)
	return s.delta(d), err
}
func (s *workspaceSource) LoadInitial(ctx context.Context) (source.Delta, error) {
	if loader, ok := s.Source.(source.InitialLoader); ok {
		d, err := loader.LoadInitial(ctx)
		return s.delta(d), err
	}
	return source.Delta{Coverage: source.Partial}, nil
}
func (s *workspaceSource) Watch(ctx context.Context, changed func()) error {
	if watcher, ok := s.Source.(source.Watcher); ok {
		return watcher.Watch(ctx, changed)
	}
	<-ctx.Done()
	return ctx.Err()
}
func (s *workspaceSource) Read(ctx context.Context, id string) (model.Item, error) {
	i, err := s.Source.Read(ctx, id)
	return s.item(i), err
}
func ProjectContext(ctx context.Context, cfg project.Config) context.Context {
	_, caller := Coordination(ctx)
	return WithClaimEvents(WithCoordination(ctx, NewCoordinator(cfg), caller), cfg)
}

// NewCoordinator builds the configured claim system, or nil when none is.
func NewCoordinator(cfg project.Config) model.Coordinator {
	if !cfg.Coordination.Enabled() {
		return nil
	}
	return worklease.New(cfg)
}
func (s *workspaceSource) Apply(ctx context.Context, m model.Mutation) (model.Item, error) {
	i, err := ApplyWrite(ProjectContext(ctx, s.cfg), s.cfg, s.mutation(m))
	return s.item(i), err
}
func (s *workspaceSource) Prepare(ctx context.Context, m model.Mutation) (source.Prepared, error) {
	p, ok := s.Source.(source.Preparer)
	if !ok {
		return source.Prepared{}, source.Refuse("unavailable", "provider preparation unavailable")
	}
	prepared, err := p.Prepare(ctx, s.mutation(m))
	prepared.Item = s.item(prepared.Item)
	prepared.Inverse.Ref.Source = s.id
	return prepared, err
}

// workspaceItems resolves explicit source:ID references only when unique in the
// loaded scope. Bare IDs stay project/source local. Ambiguity stays unknown.
func workspaceItems(cfg project.Config, items []model.Item) []model.Item {
	index := make(map[model.Ref]model.Ref, len(items))
	for _, i := range items {
		_, local := cfg.SourceProject(i.Ref.Source)
		key := canonical(model.Ref{Source: local, ID: i.Ref.ID})
		if _, exists := index[key]; exists {
			index[key] = model.Ref{}
		} else {
			index[key] = i.Ref
		}
	}
	out := make([]model.Item, len(items))
	for n, i := range items {
		i = cloneItem(i)
		resolve := func(r model.Ref) model.Ref {
			if r.Source == "" {
				r.Source = i.Ref.Source
				return r
			}
			if _, ok := cfg.Workspace.Sources[r.Source]; ok {
				return r
			}
			if target := index[canonical(r)]; target != (model.Ref{}) {
				return target
			}
			return r
		}
		for _, refs := range [][]model.Ref{i.Children, i.BlockedBy, i.Blocks, i.Related} {
			for k := range refs {
				refs[k] = resolve(refs[k])
			}
		}
		if i.Parent != nil {
			*i.Parent = resolve(*i.Parent)
		}
		if i.DuplicateOf != nil {
			*i.DuplicateOf = resolve(*i.DuplicateOf)
		}
		for k := range i.Edges {
			i.Edges[k].Target = resolve(i.Edges[k].Target)
		}
		out[n] = i
	}
	return out
}

func workspaceRanking(ctx context.Context, cfg project.Config, items []model.Item, archived, stale map[model.Ref]bool, sprints []model.Sprint) (Result, map[model.Ref]Claim, []string) {
	configs := make(map[string]project.Config, len(cfg.Workspace.Projects))
	claims := map[model.Ref]Claim{}
	var warnings []string
	for _, p := range cfg.Workspace.Projects {
		configs[p.Project.ID] = p
		if !p.Coordination.Enabled() {
			continue
		}
		var local []model.Item
		aliases := map[model.Ref]model.Ref{}
		for _, i := range items {
			if i.Project == p.Project.ID {
				alias := i.Ref
				_, i.Ref.Source = cfg.SourceProject(alias.Source)
				aliases[i.Ref] = alias
				local = append(local, i)
			}
		}
		states, _, err := ClaimStates(ProjectContext(ctx, p), local, time.Now())
		if err != nil {
			warnings = append(warnings, p.Project.ID+": "+err.Error())
		}
		for ref, state := range states {
			claims[aliases[ref]] = state
		}
	}
	return Evaluate(Input{Items: items, Archived: archived, Stale: stale, Projects: configs, Claims: claims, Sprints: sprints}), claims, warnings
}

func loadWorkspaceReads(ctx context.Context, cfg project.Config, archived, sprints bool) (ReadSnapshot, error) {
	s := ReadSnapshot{ctx: ctx, Query: query.Snapshot{Settings: cfg.Settings, Now: time.Now(), States: map[model.Ref]query.State{}}}
	archives := map[model.Ref]bool{}
	stale := map[model.Ref]bool{}
	for _, p := range cfg.Workspace.Projects {
		s.Query.Coordination = s.Query.Coordination || p.Coordination.Enabled()
		reads, err := loadReads(ProjectContext(ctx, p), p, archived, true, sprints)
		if err != nil {
			return s, err
		}
		s.Warnings = append(s.Warnings, reads.Warnings...)
		for _, sprint := range reads.Query.Sprints {
			for alias, entry := range cfg.Workspace.Sources {
				if cfg.Workspace.Projects[entry.Project].Project.ID == p.Project.ID && entry.ID == sprint.Source {
					sprint.Source = alias
					s.Query.Sprints = append(s.Query.Sprints, sprint)
					break
				}
			}
		}
		for _, item := range reads.Query.Items {
			for alias, entry := range cfg.Workspace.Sources {
				if cfg.Workspace.Projects[entry.Project].Project.ID == p.Project.ID && entry.ID == item.Ref.Source {
					state := reads.Query.States[item.Ref]
					isStale := reads.stale[item.Ref]
					item.Ref.Source = alias
					item.Project = p.Project.ID
					s.Query.States[item.Ref] = state
					archives[item.Ref] = state.Archived
					stale[item.Ref] = isStale
					break
				}
			}
			s.Query.Items = append(s.Query.Items, item)
		}
	}
	s.Query.Items = workspaceItems(cfg, s.Query.Items)
	var warnings []string
	s.Ranking, _, warnings = workspaceRanking(ctx, cfg, s.Query.Items, archives, stale, s.Query.Sprints)
	s.Warnings = append(s.Warnings, warnings...)
	for _, e := range s.Ranking.Items {
		s.Query.Next = append(s.Query.Next, e.Ref)
		state := s.Query.States[e.Ref]
		state.Readiness = string(e.Readiness)
		s.Query.States[e.Ref] = state
	}
	if cfg.Me.Default != "" {
		s.Query.Me = []string{cfg.Me.Default}
	}
	return s, nil
}
