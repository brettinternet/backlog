package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
)

// Source wraps a provider after its project trust check. The same Secrets used
// for credential resolution must be supplied here: no provider text reaches
// the engine, cache, event log or hooks without crossing this boundary.
type Source struct {
	backend  source.Source
	cache    cache.Store
	secrets  *Secrets
	interval time.Duration
}

func New(backend source.Source, store cache.Store, secrets *Secrets) *Source {
	return &Source{backend: backend, cache: store, secrets: secrets, interval: time.Minute}
}
func (s *Source) Info() source.Info {
	info := s.backend.Info()
	info.ID, info.Kind, info.Display = s.secrets.Redact(info.ID), s.secrets.Redact(info.Kind), s.secrets.Redact(info.Display)
	return info
}
func (s *Source) LoadInitial(ctx context.Context) (source.Delta, error) {
	delta, err := s.cache.RemoteInitial(ctx)
	if err != nil {
		return source.Delta{}, s.safeError(err)
	}
	delta.Coverage = source.Unknown
	delta.Stale = make(map[model.Ref]bool, len(delta.Items))
	for _, item := range delta.Items {
		delta.Stale[item.Ref] = true
	}
	return s.cleanDelta(delta)
}
func (s *Source) Load(ctx context.Context, _ source.Cursor) (source.Delta, error) {
	if s.secrets == nil {
		return source.Delta{}, source.Refuse("unavailable", "remote secret store required")
	}
	called := false
	var loaded source.Delta
	delta, err := s.cache.RemoteRefresh(ctx, func(cursor source.Cursor) (source.Delta, error) {
		called = true
		delta, err := s.backend.Load(ctx, cursor)
		if err != nil {
			return source.Delta{}, s.safeError(err)
		}
		loaded, err = s.cleanDelta(delta)
		return loaded, err
	})
	if err != nil && !called {
		// Cursors are disposable. An unavailable cache starts a fresh provider
		// enumeration rather than preventing reads or trusting lost state.
		delta, err = s.backend.Load(ctx, source.Cursor{})
		if err == nil {
			return s.cleanDelta(delta)
		}
	}
	if err != nil && loaded.Coverage != "" {
		// Cache persistence failed after a successful read. Publish the fresh
		// response; incomplete coverage still retains unseen engine rows.
		return loaded, nil
	}
	return s.guardSearch(delta), s.safeError(err)
}
func (s *Source) Read(ctx context.Context, id string) (model.Item, error) {
	item, err := s.backend.Read(ctx, id)
	if err != nil {
		return model.Item{}, s.safeError(err)
	}
	return s.cleanItem(item)
}
func (s *Source) Apply(ctx context.Context, m model.Mutation) (model.Item, error) {
	if m.Op != model.Create {
		current, err := s.Read(ctx, m.Ref.ID)
		if err != nil {
			return model.Item{}, err
		}
		if m.ExpectedVersion == "" || current.Version != m.ExpectedVersion {
			return model.Item{}, source.Refuse("conflict", "provider version changed")
		}
	}
	// The provider must also enforce ExpectedVersion atomically. A preflight is
	// not permission to overwrite a race, nor to retry an uncertain remote write.
	item, err := s.backend.Apply(ctx, m)
	if err != nil {
		return model.Item{}, s.safeError(err)
	}
	return s.cleanItem(item)
}
func (s *Source) Prepare(ctx context.Context, m model.Mutation) (source.Prepared, error) {
	before, err := s.Read(ctx, m.Ref.ID)
	if err != nil {
		return source.Prepared{}, err
	}
	if before.Version != m.ExpectedVersion || m.ExpectedVersion == "" {
		return source.Prepared{}, source.Refuse("conflict", "provider version changed")
	}
	return source.PrepareFields(before, m)
}
func (s *Source) Watch(ctx context.Context, notify func()) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			notify()
		}
	}
}
func (s *Source) safeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var schedule ScheduleDiagnostic
	if errors.As(err, &schedule) {
		return ScheduleDiagnostic{Code: "rate-limited", RetryAt: schedule.RetryAt}
	}
	var refusal *source.Error
	if errors.As(err, &refusal) {
		switch refusal.Reason {
		case "conflict", "not-found", "unavailable", "rejected":
			return source.Refuse(refusal.Reason, "remote source refused operation")
		}
	}
	return source.Refuse("unavailable", "remote source failed")
}
func (s *Source) cleanItem(item model.Item) (model.Item, error) {
	data, err := json.Marshal(item)
	if err != nil {
		return model.Item{}, source.Refuse("rejected", "invalid provider item")
	}
	var clean model.Item
	if err = json.Unmarshal([]byte(s.secrets.Redact(string(data))), &clean); err != nil {
		return model.Item{}, source.Refuse("rejected", "invalid provider item")
	}
	return clean, nil
}
func (s *Source) cleanDelta(delta source.Delta) (source.Delta, error) {
	items := make([]model.Item, 0, len(delta.Items))
	for _, item := range delta.Items {
		clean, err := s.cleanItem(item)
		if err != nil {
			return source.Delta{}, err
		}
		items = append(items, clean)
	}
	delta.Items, delta.ImmutableItems = items, nil
	cleanRefs := func(refs map[model.Ref]bool) map[model.Ref]bool {
		if refs == nil {
			return nil
		}
		clean := make(map[model.Ref]bool, len(refs))
		for ref, value := range refs {
			ref.Source, ref.ID = s.secrets.Redact(ref.Source), s.secrets.Redact(ref.ID)
			clean[ref] = value
		}
		return clean
	}
	delta.Stale, delta.Archived = cleanRefs(delta.Stale), cleanRefs(delta.Archived)
	for index, finding := range delta.Findings {
		finding.Code, finding.Severity = s.secrets.Redact(finding.Code), s.secrets.Redact(finding.Severity)
		finding.Path, finding.Message = s.secrets.Redact(finding.Path), s.secrets.Redact(finding.Message)
		delta.Findings[index] = finding
	}
	// A credential-bearing cursor is unusable, not a redacted cursor to replay.
	if s.secrets.Redact(delta.Cursor.Value) != delta.Cursor.Value {
		return source.Delta{}, source.Refuse("rejected", "invalid provider cursor")
	}
	return s.guardSearch(delta), nil
}

// Text predicates use the existing source pushdown seam. A provider may return
// false positives (the shared query evaluator removes them), but a false
// negative or failed search disables pushdown for this immutable snapshot.
func (s *Source) guardSearch(delta source.Delta) source.Delta {
	search := delta.TextSearch
	if search == nil {
		return delta
	}
	items := delta.Items
	delta.TextSearch = func(ctx context.Context, text string) (map[model.Ref]bool, error) {
		matches, err := search(ctx, text)
		if err != nil {
			return nil, s.safeError(err)
		}
		if matches == nil {
			return nil, nil
		}
		for _, item := range items {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if strings.Contains(strings.ToLower(query.SearchText(item)), strings.ToLower(text)) && !matches[item.Ref] {
				return nil, nil
			}
		}
		clean := make(map[model.Ref]bool, len(matches))
		for ref, value := range matches {
			ref.Source, ref.ID = s.secrets.Redact(ref.Source), s.secrets.Redact(ref.ID)
			clean[ref] = value
		}
		return clean, nil
	}
	return delta
}
