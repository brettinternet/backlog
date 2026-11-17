package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
	ctx            context.Context
	cancel         context.CancelFunc
	cfg            project.Config
	capabilities   map[string]model.Capabilities
	backends       source.Registry // immutable after OpenStore
	capabilitiesMu sync.RWMutex
	current        atomic.Pointer[Snapshot]
	events         chan func(*storeState)
	wg             sync.WaitGroup
	subMu          sync.Mutex
	subscribers    map[chan *Snapshot]struct{}
	results        sync.Map // outstanding result channels, completed exactly once
	historyNote    atomic.Pointer[string]
	sprints        atomic.Bool
}
type sourceWorker struct {
	backend source.Source
	jobs    chan *editJob
	refresh chan struct{}
	// reconcile marks the next queued refresh as a manual full resync.
	reconcile atomic.Bool
	sprints   bool // the source can supply a sprint catalog
}

// editJob applies its steps in order, as one edit: one result and one history
// entry. A step that fails stops the rest, and the entry keeps what was written.
type editJob struct {
	steps   []*editStep
	history int // -1 undo, +1 redo, 0 ordinary edit
	result  chan error
}
type editStep struct {
	mutation model.Mutation
	before   *record
	preview  model.Item
	inverse  model.Mutation
	// Set by the worker: the written item, or why the step failed. A step after
	// a failure isn't attempted.
	item      model.Item
	err       error
	attempted bool
	remote    *model.Item
}
type historyEntry struct{ steps []historyStep }
type historyStep struct {
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
		if cfg.Workspace != nil && !workspaceSourceTrusted(cfg, configured.ID) || cfg.Workspace == nil && cfg.RequireTrusted(fmt.Sprintf("sources[%d]", i)) != nil {
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
	s := &Store{ctx: ctx, cancel: cancel, cfg: cfg, backends: maps.Clone(registry), capabilities: make(map[string]model.Capabilities, len(registry)), events: make(chan func(*storeState), 64), subscribers: map[chan *Snapshot]struct{}{}}
	s.sprints.Store(WantsSprints(cfg))
	initial := &Snapshot{records: map[model.Ref]*record{}, sources: map[string]SourceState{}}
	st := &storeState{store: s, snapshot: initial, workers: map[string]*sourceWorker{}, pending: map[model.Ref]bool{}, superseded: map[model.Ref]string{}}
	configured := make(map[string]project.SourceConfig, len(cfg.Sources))
	for _, sc := range cfg.Sources {
		configured[sc.ID] = sc
	}
	for id, backend := range registry {
		caps := backend.Info().Capabilities
		s.capabilities[id] = make(model.Capabilities, len(caps))
		for op, allowed := range caps {
			s.capabilities[id][op] = allowed
		}
		w := &sourceWorker{backend: backend, jobs: make(chan *editJob, 64), refresh: make(chan struct{}, 1), sprints: configured[id].Sprints()}
		initial.sources[id] = SourceState{Coverage: source.Unknown, SprintsPending: w.sprints && s.sprints.Load()}
		st.workers[id] = w
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
	s.capabilitiesMu.RLock()
	defer s.capabilitiesMu.RUnlock()
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

// WorkflowStatus resolves workflow.start or workflow.done for item, which may
// need provider I/O, so callers keep it off the UI loop.
func (s *Store) WorkflowStatus(item model.Item, category model.StatusCategory) (string, error) {
	backend, ok := s.backends[item.Ref.Source]
	if !ok {
		return "", source.Refuse("unavailable", "unknown source "+item.Ref.Source)
	}
	return workflowStatus(s.ctx, s.cfg, backend, item, category)
}

// StatusChoices lists the statuses item's source accepts, which may need
// provider I/O, so callers keep it off the UI loop.
func (s *Store) StatusChoices(item model.Item) ([]model.StatusChoice, error) {
	backend, ok := s.backends[item.Ref.Source]
	if !ok {
		return nil, source.Refuse("unavailable", "unknown source "+item.Ref.Source)
	}
	return statusChoices(s.ctx, s.cfg, backend, item)
}

// Mutate applies ms in order as one edit, which one undo reverses. They must
// target distinct items of one source; a create goes alone.
func (s *Store) Mutate(ms ...model.Mutation) <-chan error {
	result := make(chan error, 1)
	job := &editJob{result: result}
	for _, m := range ms {
		job.steps = append(job.steps, &editStep{mutation: cloneMutation(m)})
	}
	s.request(func(st *storeState) { st.begin(job) }, result)
	return result
}
func (s *Store) Undo() <-chan error { return s.history(-1) }
func (s *Store) Redo() <-chan error { return s.history(1) }

// HistoryNote describes what the last completed undo or redo did when that
// differs from reversing the edit, such as deleting a created item.
func (s *Store) HistoryNote() string {
	if note := s.historyNote.Load(); note != nil {
		return *note
	}
	return ""
}
func (s *Store) history(direction int) <-chan error {
	result := make(chan error, 1)
	s.request(func(st *storeState) {
		st.store.historyNote.Store(nil)
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
		// Reversing an edit runs its steps backward.
		job := &editJob{history: direction, result: result}
		steps := stack[len(stack)-1].steps
		for n := len(steps) - 1; n >= 0; n-- {
			job.steps = append(job.steps, &editStep{mutation: steps[n].mutation})
		}
		st.begin(job)
	}, result)
	return result
}

// SprintSource reports whether a source can carry sprints (Linear or Jira).
func (s *Store) SprintSource(sourceID string) bool {
	cfg, local := s.cfg.SourceProject(sourceID)
	return sprintSource(cfg, local)
}

// UseSprints enables provider sprint data, refreshing sources the first time.
// Capable sources report SprintsPending until a sprint-enabled load publishes
// their catalog. Like Refresh it performs no I/O on the caller.
func (s *Store) UseSprints() {
	if !s.sprints.Swap(true) {
		s.refresh(true)
	}
}

// Refresh is a manual resync: remote sources run a full reconciliation instead
// of their change window. Periodic watcher refreshes keep their normal cost.
func (s *Store) Refresh() { s.refresh(false) }
func (s *Store) refresh(sprints bool) {
	result := make(chan error, 1)
	s.request(func(st *storeState) {
		n := st.snapshot
		for id, w := range st.workers {
			if sprints && w.sprints {
				status := n.sources[id]
				status.SprintsPending = true
				n = n.withSource(id, status)
			}
			w.reconcile.Store(true)
			latest(w.refresh, struct{}{})
		}
		if n != st.snapshot {
			st.publish(n)
		}
		s.complete(result, nil)
	}, result)
}

// Observe publishes a fresh source read of a loaded item, so its row agrees
// with detail before the next scan. A not-found read marks the row stale; other
// failures change nothing. It may wait for the engine actor, so call it from a
// background command, never the UI loop.
func (s *Store) Observe(ref model.Ref, item model.Item, err error) {
	gone := ErrorReason(err) == "not-found"
	if err != nil && !gone || !gone && item.Ref != ref {
		return
	}
	item = cloneItem(item)
	s.send(func(st *storeState) { st.observe(ref, &item, gone) })
}
func (st *storeState) observe(ref model.Ref, item *model.Item, gone bool) {
	old := st.snapshot.records[ref]
	// A pending or failed edit owns the row until it settles or is dismissed.
	if old == nil || st.pending[ref] || old.edit.Pending || old.edit.Error != "" {
		return
	}
	r := *old
	if gone {
		if old.stale {
			return
		}
		r.stale = true
	} else {
		// A read that raced our own edit returns the version that edit replaced.
		if old.item.Version == item.Version && !old.stale || st.superseded[ref] == item.Version {
			return
		}
		r.item, r.stale = item, false
	}
	n := st.snapshot.fork()
	n.records[ref] = &r
	st.publish(n)
}
func (st *storeState) begin(job *editJob) {
	reject := func(err error) { st.store.complete(job.result, err) }
	if len(job.steps) == 0 {
		reject(source.Refuse("rejected", "empty mutation"))
		return
	}
	sourceID := job.steps[0].mutation.Ref.Source
	w := st.workers[sourceID]
	if w == nil {
		reject(source.Refuse("unavailable", "unknown source"))
		return
	}
	cfg, local := st.store.cfg.SourceProject(sourceID)
	seen := map[model.Ref]bool{}
	for _, step := range job.steps {
		m := step.mutation
		if m.Ref.Source != sourceID || m.Op == model.Create && len(job.steps) > 1 || m.Op != model.Create && seen[m.Ref] {
			reject(source.Refuse("rejected", "one edit changes distinct items of one source, or creates one item"))
			return
		}
		seen[m.Ref] = true
		if st.historyBusy || st.pending[m.Ref] {
			reject(source.Refuse("busy", "item or history mutation pending"))
			return
		}
		if err := validateWrite(cfg, local, w.backend.Info().Capabilities, m); err != nil {
			reject(err)
			return
		}
		if m.Op == model.Create {
			st.serial++
			step.mutation.Ref.ID = fmt.Sprintf("~pending-%d", st.serial)
			continue
		}
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
				local := cloneItem(*before.item)
				local.Version = m.ExpectedVersion
				remote := cloneItem(*before.item)
				n := st.snapshot.fork()
				n.records[m.Ref] = &record{item: before.item, edit: EditState{Error: err.Error(), Local: &local, Remote: &remote}, archived: before.archived}
				st.publish(n)
			}
			reject(err)
			return
		}
		step.before = before
	}
	if len(w.jobs) == cap(w.jobs) {
		reject(source.Refuse("busy", "source queue is full"))
		return
	}
	n := st.snapshot.fork()
	for _, step := range job.steps {
		m := step.mutation
		preparePreview(cfg, w.backend.Info().Capabilities, step)
		if st.store.cfg.Workspace != nil {
			step.preview.Project = cfg.Project.ID
		}
		st.pending[m.Ref] = true
		n.records[m.Ref] = &record{item: new(cloneItem(step.preview)), edit: EditState{Pending: true}, archived: m.Op == model.Archive || step.before != nil && step.before.archived && m.Op != model.Unarchive}
	}
	st.historyBusy = job.history != 0
	st.publish(n)
	// This actor is the only producer; the worker can only free queue capacity.
	w.jobs <- job
}
func (s *Store) runSource(id string, w *sourceWorker) {
	// catalog marks a load that requested sprints, so its success publishes the catalog.
	publish := func(delta source.Delta, err error, replaceInitial, initial, catalog bool) {
		s.send(func(st *storeState) {
			// The actor can retain its last callback while idle. Records own
			// individual items, not the full transfer slice or search closure.
			defer func() { delta = source.Delta{} }()
			n := st.snapshot.fork()
			status := SourceState{Coverage: delta.Coverage, Cursor: delta.Cursor, Initial: initial, Sprints: delta.Sprints, SprintsPending: n.sources[id].SprintsPending && (err != nil || !catalog)}
			for k := range status.Sprints {
				status.Sprints[k].Source = id
			}
			if err != nil {
				status.Error = err.Error()
				status.Coverage = source.Unknown
				status.Cursor = n.sources[id].Cursor
				status.Sprints = n.sources[id].Sprints
				for ref, old := range n.records {
					if ref.Source == id {
						r := *old
						r.stale = true
						n.records[ref] = &r
					}
				}
			} else {
				seen := map[model.Ref]bool{}
				for index := range delta.Items {
					i := &delta.Items[index]
					shared := len(delta.ImmutableItems) == len(delta.Items) && delta.ImmutableItems[index] != nil
					if shared {
						i = delta.ImmutableItems[index]
					}
					ref := i.Ref
					ref.Source = id
					seen[ref] = true
					if st.pending[ref] {
						continue
					}
					old := n.records[ref]
					// Loads list active items only, so a session-archived record seen here
					// was unarchived externally; a version check alone would keep it hidden.
					// Versions exclude the read-only sprint; compare it directly.
					if old == nil || old.item.Version != i.Version || old.item.Sprint != i.Sprint || old.item.Names[i.Sprint] != i.Names[i.Sprint] || old.archived != delta.Archived[i.Ref] || old.stale != delta.Stale[i.Ref] {
						edit := EditState{}
						if old != nil {
							edit = old.edit
						}
						if !shared || i.Ref.Source != id {
							i = new(cloneItem(*i))
							i.Ref.Source = id
						}
						n.records[i.Ref] = &record{item: i, edit: edit, archived: delta.Archived[i.Ref], stale: delta.Stale[i.Ref]}
					}
				}
				// Initial records are provisional, not a prior successful full
				// refresh. Even a partial first load must discard omitted preview
				// items (e.g. deleted files or files that now fail to parse).
				if delta.Coverage == source.Complete && !delta.Incremental && len(delta.Stale) == 0 || replaceInitial {
					for ref := range n.records {
						if ref.Source == id && !seen[ref] && !st.pending[ref] && !n.records[ref].archived {
							delete(n.records, ref)
						}
					}
				}
				if delta.Coverage != source.Complete {
					for ref, old := range n.records {
						if ref.Source == id && !seen[ref] {
							r := *old
							r.stale = true
							n.records[ref] = &r
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
	// Preview records stay provisional until a load succeeds; a failed first
	// load must not leave them to be retained by later partial loads.
	provisional := false
	cursor := source.Cursor{}
	load := func() {
		// publish replaces the state, which clears Loading.
		s.send(func(st *storeState) {
			status := st.snapshot.sources[id]
			status.Loading = true
			st.publish(st.snapshot.withSource(id, status))
		})
		request := cursor
		request.Reconcile = w.reconcile.Swap(false)
		request.Sprints = s.sprints.Load()
		delta, err := w.backend.Load(s.ctx, request)
		caps := w.backend.Info().Capabilities
		s.capabilitiesMu.Lock()
		s.capabilities[id] = make(model.Capabilities, len(caps))
		for op, allowed := range caps {
			s.capabilities[id][op] = allowed
		}
		s.capabilitiesMu.Unlock()
		if err == nil {
			if delta.Cursor.Value != "" {
				cursor = delta.Cursor
			}
			delta.Cursor = cursor
		}
		publish(delta, err, provisional, false, request.Sprints)
		provisional = provisional && err != nil
	}
	if initial, ok := w.backend.(source.InitialLoader); ok {
		if delta, err := initial.LoadInitial(s.ctx); err == nil {
			cursor = delta.Cursor
			if len(delta.Items) > 0 {
				publish(delta, nil, false, true, false)
				provisional = len(delta.Stale) == 0
			}
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
func preparePreview(cfg project.Config, caps model.Capabilities, job *editStep) {
	m := job.mutation
	if m.Op == model.Create {
		job.preview = cloneItem(*m.Item)
		job.preview.Ref = m.Ref
		switch {
		case caps.Allows(model.Archive):
			job.inverse = model.Mutation{Ref: m.Ref, Op: model.Archive}
		case caps.Allows(model.Delete):
			job.inverse = model.Mutation{Ref: m.Ref, Op: model.Delete}
		}
	} else {
		job.preview = *job.before.item
		if prepared, err := source.PrepareFields(job.preview, m); err == nil {
			job.preview = prepared.Item
		} else if data, err := format.Serialize(*job.before.item); err == nil {
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
	for _, step := range job.steps {
		step.attempted = true
		step.item, step.err = s.runStep(w, step)
		if step.err != nil {
			var refusal *source.Error
			if errors.As(step.err, &refusal) && refusal.Reason == "conflict" {
				if i, e := w.backend.Read(s.ctx, step.mutation.Ref.ID); e == nil {
					step.remote = new(cloneItem(i))
				}
			}
			break
		}
	}
	s.finishEdit(job)
}
func (s *Store) runStep(w *sourceWorker, step *editStep) (model.Item, error) {
	m := step.mutation
	if m.Op == model.Create {
		m.Ref.ID = ""
	}
	m, err := resolveStatusWrite(s.ctx, s.cfg, w.backend, m)
	if err == nil {
		err = requireTours(s.ctx, s.cfg, w.backend, m)
	}
	if err != nil {
		return model.Item{}, err
	}
	if m.Op != model.Create {
		preparer, ok := w.backend.(source.Preparer)
		if !ok {
			return model.Item{}, source.Refuse("unavailable", "source does not support optimistic preparation")
		}
		prepared, err := preparer.Prepare(s.ctx, m)
		if err != nil {
			return model.Item{}, err
		}
		step.preview = cloneItem(prepared.Item)
		step.inverse = cloneMutation(prepared.Inverse)
	}
	var transition TransitionResult
	ctx := WithTransitionResult(s.ctx, &transition)
	var item model.Item
	if s.cfg.Workspace != nil {
		item, err = w.backend.Apply(ctx, m)
	} else {
		item, err = coordinatedWrite(ctx, s.cfg, w.backend, m)
	}
	if err != nil && transition.Claim != nil {
		err = source.Refuse(ErrorReason(err), fmt.Sprintf("status %s; claim %s: %s", transition.Status, transition.Claim.Outcome, ErrorMessage(err)))
	}
	return item, err
}
func (s *Store) finishEdit(job *editJob) {
	var first error
	for _, step := range job.steps {
		if first == nil {
			first = step.err
		}
	}
	if !s.send(func(st *storeState) {
		n := st.snapshot.fork()
		if job.history != 0 {
			st.historyBusy = false
		}
		var written []historyStep // inverses of the steps that applied, in order
		applied := map[model.Ref]model.Item{}
		deleted := false
		for _, step := range job.steps {
			ref, item, err := step.mutation.Ref, step.item, step.err
			delete(st.pending, ref)
			switch {
			case !step.attempted:
				if step.before != nil {
					n.records[ref] = step.before
				} else {
					delete(n.records, ref)
				}
			case err != nil:
				state := EditState{Error: err.Error()}
				if step.remote != nil {
					local := cloneItem(step.preview)
					state.Local = &local
					state.Remote = step.remote
				}
				if item.Version != "" {
					n.records[ref] = &record{item: new(cloneItem(item)), edit: state, archived: step.before != nil && step.before.archived}
				} else if step.before != nil {
					prior := step.before.item
					if step.remote != nil {
						prior = step.remote
					}
					// A preflight that finds the item gone revokes its readiness now.
					n.records[ref] = &record{item: prior, edit: state, archived: step.before.archived, stale: step.before.stale || ErrorReason(err) == "not-found"}
				} else {
					n.records[ref] = &record{item: new(cloneItem(step.preview)), edit: state}
				}
			case step.mutation.Op == model.Delete:
				// No redo: recreating would assign the item a new ID.
				deleted = true
				applied[ref] = item
				delete(n.records, ref)
				note := "deleted " + ref.Source + ":" + ref.ID
				if item.Version != "" {
					n.records[ref] = &record{item: new(cloneItem(item))}
					note = "couldn't delete " + ref.Source + ":" + ref.ID + ", so it's now " + item.Status
					if item.Category == model.Canceled {
						note += " (canceled)"
					}
				}
				st.store.historyNote.Store(&note)
			default:
				item = cloneItem(item)
				applied[ref] = item
				delete(n.records, ref)
				n.records[item.Ref] = &record{item: &item, archived: step.mutation.Op == model.Archive || step.before != nil && step.before.archived && step.mutation.Op != model.Unarchive}
				inverse := step.inverse
				inverse.Ref = item.Ref
				inverse.ExpectedVersion = item.Version
				entry := historyStep{mutation: inverse}
				if step.before != nil {
					entry.targetVersion = step.before.item.Version
					st.superseded[item.Ref] = step.before.item.Version
				}
				if inverse.Op != "" {
					written = append(written, entry)
				}
			}
		}
		if len(applied) > 0 {
			// The stack this edit came from loses the steps that applied; the
			// opposite one gains their inverses as one entry.
			from, to := &st.undo, &st.redo
			if job.history == 1 {
				from, to = &st.redo, &st.undo
			}
			targets := map[model.Ref]string{}
			if job.history != 0 {
				top := (*from)[len(*from)-1]
				remaining := top.steps[:0:0]
				for _, step := range top.steps {
					if _, ok := applied[step.mutation.Ref]; ok {
						if step.mutation.Op != model.Delete {
							targets[step.mutation.Ref] = step.targetVersion
						}
					} else {
						remaining = append(remaining, step)
					}
				}
				*from = (*from)[:len(*from)-1]
				if len(remaining) > 0 {
					*from = append(*from, historyEntry{steps: remaining})
				}
			} else {
				to = &st.undo
				st.redo = nil
			}
			if len(written) > 0 {
				*to = append(*to, historyEntry{steps: written})
			}
			if deleted {
				st.redo = nil
			}
			// Only rebind history across our own contiguous edits. Crossing an external
			// version gap would let a later undo silently discard somebody else's work.
			for ref, targetVersion := range targets {
				version := applied[ref].Version
				for _, stack := range [][]historyEntry{st.undo, st.redo} {
					found := false
					for j := len(stack) - 1; j >= 0 && !found; j-- {
						for k := range stack[j].steps {
							if step := &stack[j].steps[k]; step.mutation.Ref == ref {
								if step.mutation.ExpectedVersion == targetVersion {
									step.mutation.ExpectedVersion = version
								}
								found = true
								break
							}
						}
					}
				}
			}
		}
		st.publish(n)
		s.complete(job.result, first)
	}) {
		s.complete(job.result, s.ctx.Err())
	}
}
