package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
)

// ReadSnapshot is the read-only composition used by command adapters.
type ReadSnapshot struct {
	Query     query.Snapshot
	Ranking   Result
	Warnings  []string
	searchers []func(context.Context, string) (map[model.Ref]bool, error)
	ctx       context.Context
}

// LoadReads loads active items. With archived set, it also loads archive/ on
// demand (query.md is:archived); those items are marked archived and never ready.
func LoadReads(ctx context.Context, cfg project.Config, archived bool) (ReadSnapshot, error) {
	s := ReadSnapshot{Query: query.Snapshot{Items: []model.Item{}, Settings: cfg.Settings, Now: time.Now(), States: map[model.Ref]query.State{}}}
	s.ctx = ctx
	if cfg.Me.Default != "" {
		s.Query.Me = []string{cfg.Me.Default}
	}
	archivedRefs := map[model.Ref]bool{}
	load := func(backend source.Source, id string, cursor source.Cursor) (source.Delta, error) {
		delta, err := backend.Load(ctx, cursor)
		if err == nil && delta.Coverage != source.Complete {
			err = fmt.Errorf("source %s has incomplete coverage: %v", id, delta.Findings)
		}
		return delta, err
	}
	for _, c := range cfg.Sources {
		backend, err := readSource(cfg, c)
		if err != nil {
			return s, err
		}
		delta, err := load(backend, c.ID, source.Cursor{})
		if err != nil {
			return s, err
		}
		if archived {
			items := delta.Items
			seen := map[model.Ref]bool{}
			for _, i := range items {
				seen[i.Ref] = true
			}
			// The second load's search index also covers archived items.
			if delta, err = load(backend, c.ID, source.Cursor{IncludeArchive: true}); err != nil {
				return s, err
			}
			// A duplicate ID across active and archive is a check finding; keep the first.
			for _, i := range delta.Items {
				if !seen[i.Ref] {
					seen[i.Ref] = true
					archivedRefs[i.Ref] = true
					items = append(items, i)
				}
			}
			delta.Items = items
		}
		for _, finding := range delta.Findings {
			if finding.Severity == "warning" {
				s.Warnings = append(s.Warnings, finding.Message)
			}
		}
		if delta.TextSearch != nil {
			s.searchers = append(s.searchers, delta.TextSearch)
		} else {
			s.searchers = append(s.searchers, nil)
		}
		if len(s.Query.Items) == 0 {
			s.Query.Items = delta.Items
		} else {
			s.Query.Items = append(s.Query.Items, delta.Items...)
		}
	}
	coordination := cfg.Coordination.Worklease != nil
	claims := map[model.Ref]Claim{}
	if coordination {
		var err error
		claims, _, err = ClaimStates(ctx, s.Query.Items, s.Query.Now)
		if err != nil {
			s.Warnings = append(s.Warnings, err.Error())
		}
	}
	s.Query.Coordination = coordination
	s.Ranking = Evaluate(Input{Items: s.Query.Items, Config: cfg.Ready, Me: s.Query.Me, Archived: archivedRefs, Coordination: coordination, Claims: claims})
	for _, e := range s.Ranking.Items {
		s.Query.Next = append(s.Query.Next, e.Ref)
		claim := string(claims[e.Ref])
		if claims[e.Ref] == ClaimMine {
			claim = "me"
		}
		s.Query.States[e.Ref] = query.State{Readiness: string(e.Readiness), Archived: archivedRefs[e.Ref], Claim: claim}
	}
	return s, nil
}

func readSource(cfg project.Config, c project.SourceConfig) (source.Source, error) {
	backend, err := rawSource(cfg, c)
	if err != nil {
		return nil, err
	}
	if cfg.Paths.StateDir == "" || cfg.Paths.CacheDir == "" {
		return backend, nil
	}
	return &eventSource{Source: backend, cfg: cfg}, nil
}

func rawSource(cfg project.Config, c project.SourceConfig) (*native.Source, error) {
	for i, configured := range cfg.Sources {
		if configured.ID == c.ID {
			if err := cfg.RequireTrusted(fmt.Sprintf("sources[%d]", i)); err != nil {
				return nil, source.Refuse("unavailable", err.Error())
			}
			break
		}
	}
	if c.Type != "native" {
		return nil, fmt.Errorf("source %s (%s) is not available in this build", c.ID, c.Type)
	}
	statuses := map[string]model.StatusCategory{}
	for _, v := range cfg.Statuses {
		statuses[v.Key] = model.StatusCategory(v.Category)
	}
	path := c.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(cfg.Discovery.ControlRoot, path)
	}
	return native.New(native.Options{ID: c.ID, Path: path, Prefix: c.Prefix, CommonDir: cfg.Discovery.CommonDir, ProjectID: cfg.Project.ID, Statuses: statuses, CacheDir: cfg.Paths.CacheDir})
}

// ReadItem reads one source-qualified ref without parsing or ranking the entire backlog.
func ReadItem(ctx context.Context, cfg project.Config, ref model.Ref) (model.Item, error) {
	if ref.Source == "" || ref.ID == "" {
		return model.Item{}, source.Refuse("invalid-id", "source and item ID are required")
	}
	items, err := ReadDetail(ctx, cfg, ref.Source+":"+ref.ID)
	if err != nil {
		return model.Item{}, err
	}
	if len(items) == 0 {
		return model.Item{}, source.Refuse("not-found", ref.ID)
	}
	if len(items) > 1 {
		return model.Item{}, source.Refuse("ambiguous-id", ref.ID)
	}
	return items[0], nil
}

// ReadDetail resolves shorthand without parsing or ranking the entire backlog.
// It returns every match instead of guessing and includes explicitly named archives.
func ReadDetail(ctx context.Context, cfg project.Config, value string) ([]model.Item, error) {
	sourceID, id, qualified := strings.Cut(value, ":")
	if !qualified {
		id = value
	}
	numeric := id != ""
	for _, c := range id {
		numeric = numeric && c >= '0' && c <= '9'
	}
	matches := []model.Item{}
	for _, c := range cfg.Sources {
		if qualified && sourceID != c.ID {
			continue
		}
		backend, err := readSource(cfg, c)
		if err != nil {
			return nil, err
		}
		requested := id
		if numeric {
			requested = c.Prefix + "-" + id
		}
		item, err := backend.Read(ctx, requested)
		if err != nil {
			var refusal *source.Error
			if errors.As(err, &refusal) && (refusal.Reason == "not-found" || refusal.Reason == "invalid-id") {
				continue
			}
			return nil, err
		}
		matches = append(matches, item)
	}
	return matches, nil
}

func (s *ReadSnapshot) Parse(text string) (query.Query, error) {
	q, err := query.Parse(text, query.Options{})
	if err != nil {
		return q, err
	}
	if errs := q.Validate(s.Query); len(errs) > 0 {
		return q, errs[0]
	}
	s.Query.TextMatches = map[string]map[model.Ref]bool{}
	for _, term := range q.Terms {
		if term.Field != "text" {
			continue
		}
		for _, value := range term.Values {
			if _, ok := s.Query.TextMatches[value.Text]; ok {
				continue
			}
			matches := map[model.Ref]bool{}
			complete := len(s.searchers) > 0
			for _, search := range s.searchers {
				if search == nil {
					complete = false
					break
				}
				found, e := search(s.ctx, value.Text)
				if e != nil || found == nil {
					complete = false
					break
				}
				for ref := range found {
					matches[ref] = true
				}
			}
			if complete {
				s.Query.TextMatches[value.Text] = matches
			}
		}
	}
	return q, nil
}
