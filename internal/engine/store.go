package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

// Store owns background work. Current, Subscribe, Mutate, Undo, Redo and Refresh
// perform no source I/O or backlog-sized work. Close waits for workers; call it
// outside a UI callback. Sources must honor context cancellation.
type Store struct {
	ctx          context.Context
	cancel       context.CancelFunc
	cfg          project.Config
	capabilities map[string]model.Capabilities
	current      atomic.Pointer[Snapshot]
	events       chan func(*storeState)
	wg           sync.WaitGroup
	subMu        sync.Mutex
	subscribers  map[chan *Snapshot]struct{}
	results      sync.Map // outstanding result channels, completed exactly once
}
type sourceWorker struct {
	backend source.Source
	jobs    chan *editJob
	refresh chan struct{}
}
type editJob struct {
	mutation model.Mutation
	before   *record
	preview  model.Item
	inverse  model.Mutation
	history  int // -1 undo, +1 redo, 0 ordinary edit
	result   chan error
}
type historyEntry struct {
	mutation      model.Mutation
	targetVersion string
}
type storeState struct {
	store       *Store
	snapshot    *Snapshot
	workers     map[string]*sourceWorker
	pending     map[model.Ref]bool
	undo, redo  []historyEntry
	historyBusy bool
	serial      uint64
	// superseded holds versions this session's own edits replaced. A caller
	// expecting one is only behind its own edit, not in conflict with anyone.
	superseded map[model.Ref]string
}

// OpenProjectStore constructs the configured sources without loading items.
func OpenProjectStore(ctx context.Context, cfg project.Config) (*Store, error) {
	registry := source.Registry{}
	for i, configured := range cfg.Sources {
		// Keep untrusted adapters inert while allowing the local TUI and trust banner.
		if cfg.RequireTrusted(fmt.Sprintf("sources[%d]", i)) != nil {
			continue
		}
		backend, err := readSource(cfg, configured)
		if err != nil {
			return nil, err
		}
		if err := registry.Register(backend); err != nil {
			return nil, err
		}
	}
	return OpenStore(ctx, cfg, registry), nil
}

func OpenStore(ctx context.Context, cfg project.Config, registry source.Registry) *Store {
	ctx, cancel := context.WithCancel(ctx)
	s := &Store{ctx: ctx, cancel: cancel, cfg: cfg, capabilities: make(map[string]model.Capabilities, len(registry)), events: make(chan func(*storeState), 64), subscribers: map[chan *Snapshot]struct{}{}}
	initial := &Snapshot{records: map[model.Ref]*record{}, sources: map[string]SourceState{}}
	st := &storeState{store: s, snapshot: initial, workers: map[string]*sourceWorker{}, pending: map[model.Ref]bool{}, superseded: map[model.Ref]string{}}
	for id, backend := range registry {
		caps := backend.Info().Capabilities
		s.capabilities[id] = make(model.Capabilities, len(caps))
		for op, allowed := range caps {
			s.capabilities[id][op] = allowed
		}
		initial.sources[id] = SourceState{Coverage: source.Unknown}
		st.workers[id] = &sourceWorker{backend: backend, jobs: make(chan *editJob, 64), refresh: make(chan struct{}, 1)}
	}
	s.current.Store(initial)
	s.wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-s.events:
				f(st)
			}
		}
	})
	for id, w := range st.workers {
		s.wg.Go(func() { s.runSource(id, w) })
		if watcher, ok := w.backend.(source.Watcher); ok {
			s.wg.Go(func() {
				err := watcher.Watch(ctx, func() { latest(w.refresh, struct{}{}) })
				if err != nil && ctx.Err() == nil {
					s.send(func(st *storeState) {
						n := st.snapshot.fork()
						status := n.sources[id]
						status.Error = err.Error()
						n.sources[id] = status
						st.publish(n)
					})
				}
			})
		}
	}
	return s
}
func (s *Store) Close() {
	s.cancel()
	s.wg.Wait()
	s.results.Range(func(key, value any) bool { s.complete(key.(chan error), s.ctx.Err()); return true })
}
func (s *Store) complete(result chan error, err error) {
	if _, ok := s.results.LoadAndDelete(result); ok {
		result <- err
	}
}
func (s *Store) Current() *Snapshot { return s.current.Load() }

// Capabilities returns a source's declared capabilities without touching the
// source from a caller such as the TUI update loop.
func (s *Store) Capabilities(sourceID string) model.Capabilities {
	capabilities := s.capabilities[sourceID]
	copy := make(model.Capabilities, len(capabilities))
	for op, allowed := range capabilities {
		copy[op] = allowed
	}
	return copy
}

func (s *Store) Subscribe() (<-chan *Snapshot, func()) {
	ch := make(chan *Snapshot, 1)
	s.subMu.Lock()
	s.subscribers[ch] = struct{}{}
	ch <- s.Current()
	s.subMu.Unlock()
	return ch, func() { s.subMu.Lock(); delete(s.subscribers, ch); s.subMu.Unlock() }
}
func (st *storeState) publish(n *Snapshot) {
	st.snapshot = n
	s := st.store
	s.subMu.Lock()
	s.current.Store(n)
	for ch := range s.subscribers {
		latest(ch, n)
	}
	s.subMu.Unlock()
}
func (s *Store) send(f func(*storeState)) bool {
	select {
	case s.events <- f:
		return true
	case <-s.ctx.Done():
		return false
	}
}
func (s *Store) request(f func(*storeState), result chan error) {
	s.results.Store(result, struct{}{})
	if s.ctx.Err() != nil {
		s.complete(result, s.ctx.Err())
		return
	}
	select {
	case <-s.ctx.Done():
		s.complete(result, s.ctx.Err())
	case s.events <- f:
	default:
		s.complete(result, source.Refuse("busy", "engine request queue is full"))
	}
}
func (s *Store) Mutate(m model.Mutation) <-chan error {
	result := make(chan error, 1)
	m = cloneMutation(m)
	s.request(func(st *storeState) { st.begin(&editJob{mutation: m, result: result}) }, result)
	return result
}
func (s *Store) Undo() <-chan error { return s.history(-1) }
func (s *Store) Redo() <-chan error { return s.history(1) }
func (s *Store) history(direction int) <-chan error {
	result := make(chan error, 1)
	s.request(func(st *storeState) {
		stack := st.undo
		if direction > 0 {
			stack = st.redo
		}
		if st.historyBusy || len(st.pending) > 0 {
			s.complete(result, source.Refuse("busy", "wait for pending edits before undo or redo"))
			return
		}
		if len(stack) == 0 {
			s.complete(result, source.Refuse("not-found", "no session history"))
			return
		}
		st.begin(&editJob{mutation: stack[len(stack)-1].mutation, history: direction, result: result})
	}, result)
	return result
}
func (s *Store) Refresh() {
	result := make(chan error, 1)
	s.request(func(st *storeState) {
		for _, w := range st.workers {
			latest(w.refresh, struct{}{})
		}
		s.complete(result, nil)
	}, result)
}
func (st *storeState) begin(job *editJob) {
	m := job.mutation
	w := st.workers[m.Ref.Source]
	reject := func(err error) { st.store.complete(job.result, err) }
	if w == nil {
		reject(source.Refuse("unavailable", "unknown source"))
		return
	}
	if st.historyBusy || st.pending[m.Ref] {
		reject(source.Refuse("busy", "item or history mutation pending"))
		return
	}
	if err := validateWrite(st.store.cfg, w.backend.Info().Capabilities, m); err != nil {
		reject(err)
		return
	}
	if m.Op == model.Create {
		st.serial++
		m.Ref.ID = fmt.Sprintf("~pending-%d", st.serial)
		job.mutation.Ref = m.Ref
	} else {
		before := st.snapshot.records[m.Ref]
		if before == nil {
			reject(source.Refuse("not-found", "item is not loaded"))
			return
		}
		if m.ExpectedVersion != "" && m.ExpectedVersion != before.item.Version && m.ExpectedVersion == st.superseded[m.Ref] {
			reject(source.Refuse("busy", "display is behind your previous edit; retry"))
			return
		}
		if m.ExpectedVersion == "" || m.ExpectedVersion != before.item.Version {
			err := source.Refuse("conflict", "expected version does not match displayed item")
			if m.ExpectedVersion != "" {
				local := cloneItem(before.item)
				local.Version = m.ExpectedVersion
				remote := cloneItem(before.item)
				n := st.snapshot.fork()
				n.records[m.Ref] = &record{item: before.item, edit: EditState{Error: err.Error(), Local: &local, Remote: &remote}, archived: before.archived}
				st.publish(n)
			}
			reject(err)
			return
		}
		job.before = before
	}
	if len(w.jobs) == cap(w.jobs) {
		reject(source.Refuse("busy", "source queue is full"))
		return
	}
	preparePreview(st.store.cfg, job)
	st.pending[m.Ref] = true
	st.historyBusy = job.history != 0
	n := st.snapshot.fork()
	n.records[m.Ref] = &record{item: cloneItem(job.preview), edit: EditState{Pending: true}, archived: m.Op == model.Archive || job.before != nil && job.before.archived && m.Op != model.Unarchive}
	st.publish(n)
	// This actor is the only producer; the worker can only free queue capacity.
	w.jobs <- job
}
func (s *Store) runSource(id string, w *sourceWorker) {
	publish := func(delta source.Delta, err error) {
		s.send(func(st *storeState) {
			n := st.snapshot.fork()
			status := SourceState{Coverage: delta.Coverage}
			if err != nil {
				status.Error = err.Error()
				status.Coverage = source.Unknown
			} else {
				seen := map[model.Ref]bool{}
				for _, i := range delta.Items {
					i.Ref.Source = id
					seen[i.Ref] = true
					if st.pending[i.Ref] {
						continue
					}
					old := n.records[i.Ref]
					// Loads list active items only, so a session-archived record seen here
					// was unarchived externally; a version check alone would keep it hidden.
					if old == nil || old.item.Version != i.Version || old.archived {
						edit := EditState{}
						if old != nil {
							edit = old.edit
						}
						n.records[i.Ref] = &record{item: cloneItem(i), edit: edit}
					}
				}
				if delta.Coverage == source.Complete {
					for ref := range n.records {
						if ref.Source == id && !seen[ref] && !st.pending[ref] && !n.records[ref].archived {
							delete(n.records, ref)
						}
					}
				}
				for _, f := range delta.Findings {
					if f.Severity == "error" {
						status.Error = f.Message
						break
					}
				}
			}
			n.sources[id] = status
			st.publish(n)
		})
	}
	load := func() {
		delta, err := w.backend.Load(s.ctx, source.Cursor{})
		publish(delta, err)
	}
	if initial, ok := w.backend.(source.InitialLoader); ok {
		if delta, err := initial.LoadInitial(s.ctx); err == nil && len(delta.Items) > 0 {
			publish(delta, nil)
		}
	}
	load()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-w.refresh:
			load()
		case job := <-w.jobs:
			s.runEdit(w, job)
		}
	}
}

// The preview runs on the engine actor, before queued source I/O. Even a
// second edit behind a slow write is visible immediately as pending.
func preparePreview(cfg project.Config, job *editJob) {
	m := job.mutation
	if m.Op == model.Create {
		job.preview = cloneItem(*m.Item)
		job.preview.Ref = m.Ref
		job.inverse = model.Mutation{Ref: m.Ref, Op: model.Archive}
	} else {
		job.preview = job.before.item
		if data, err := format.Serialize(job.before.item); err == nil {
			if doc, err := format.Parse(data); err == nil {
				preview := m
				preview.ExpectedVersion = ""
				if next, _, err := doc.Apply(preview, time.Now()); err == nil {
					job.preview = next.Item()
					job.preview.Ref = m.Ref
				}
			}
		}
	}
	for _, status := range cfg.Statuses {
		if status.Key == job.preview.Status {
			job.preview.Category = model.StatusCategory(status.Category)
		}
	}
}
func (s *Store) runEdit(w *sourceWorker, job *editJob) {
	m := job.mutation
	if m.Op == model.Create {
		m.Ref.ID = ""
	}
	if m.Op != model.Create {
		preparer, ok := w.backend.(source.Preparer)
		if !ok {
			s.finishEdit(w, job, model.Item{}, source.Refuse("unavailable", "source does not support optimistic preparation"))
			return
		}
		prepared, err := preparer.Prepare(s.ctx, m)
		if err != nil {
			s.finishEdit(w, job, model.Item{}, err)
			return
		}
		job.preview = cloneItem(prepared.Item)
		job.inverse = cloneMutation(prepared.Inverse)
	}
	var transition TransitionResult
	ctx := WithTransitionResult(s.ctx, &transition)
	item, err := coordinatedWrite(ctx, s.cfg, m, func() (model.Item, error) { return w.backend.Read(ctx, m.Ref.ID) }, func() (model.Item, error) { return w.backend.Apply(ctx, m) })
	if err != nil && transition.Claim != nil {
		err = source.Refuse(ErrorReason(err), fmt.Sprintf("status %s; claim %s: %s", transition.Status, transition.Claim.Outcome, ErrorMessage(err)))
	}
	s.finishEdit(w, job, item, err)
}
func (s *Store) finishEdit(w *sourceWorker, job *editJob, item model.Item, err error) {
	var remote *model.Item
	var refusal *source.Error
	if errors.As(err, &refusal) && refusal.Reason == "conflict" {
		if i, e := w.backend.Read(s.ctx, job.mutation.Ref.ID); e == nil {
			i = cloneItem(i)
			remote = &i
		}
	}
	if !s.send(func(st *storeState) {
		n := st.snapshot.fork()
		ref := job.mutation.Ref
		delete(st.pending, ref)
		if job.history != 0 {
			st.historyBusy = false
		}
		if err != nil {
			state := EditState{Error: err.Error()}
			if remote != nil {
				local := cloneItem(job.preview)
				state.Local = &local
				state.Remote = remote
			}
			if item.Version != "" {
				n.records[ref] = &record{item: cloneItem(item), edit: state, archived: job.before != nil && job.before.archived}
			} else if job.before != nil {
				prior := job.before.item
				if remote != nil {
					prior = *remote
				}
				n.records[ref] = &record{item: prior, edit: state, archived: job.before.archived}
			} else {
				n.records[ref] = &record{item: job.preview, edit: state}
			}
		} else {
			item = cloneItem(item)
			delete(n.records, ref)
			n.records[item.Ref] = &record{item: item, archived: job.mutation.Op == model.Archive || job.before != nil && job.before.archived && job.mutation.Op != model.Unarchive}
			inverse := job.inverse
			inverse.Ref = item.Ref
			inverse.ExpectedVersion = item.Version
			entry := historyEntry{mutation: inverse}
			if job.before != nil {
				entry.targetVersion = job.before.item.Version
				st.superseded[item.Ref] = job.before.item.Version
			}
			var targetVersion string
			switch job.history {
			case -1:
				targetVersion = st.undo[len(st.undo)-1].targetVersion
				st.undo = st.undo[:len(st.undo)-1]
				st.redo = append(st.redo, entry)
			case 1:
				targetVersion = st.redo[len(st.redo)-1].targetVersion
				st.redo = st.redo[:len(st.redo)-1]
				st.undo = append(st.undo, entry)
			default:
				st.undo = append(st.undo, entry)
				st.redo = nil
			}
			// Only rebind history across our own contiguous edits. Crossing an external
			// version gap would let a later undo silently discard somebody else's work.
			if job.history != 0 {
				for _, stack := range [][]historyEntry{st.undo, st.redo} {
					for j := len(stack) - 1; j >= 0; j-- {
						if stack[j].mutation.Ref == item.Ref {
							if stack[j].mutation.ExpectedVersion == targetVersion {
								stack[j].mutation.ExpectedVersion = item.Version
							}
							break
						}
					}
				}
			}
		}
		st.publish(n)
		s.complete(job.result, err)
	}) {
		s.complete(job.result, s.ctx.Err())
	}
}
