package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
)

type eventContext struct {
	Origin string
	Cause  *string
	Depth  int
}
type eventContextKey struct{}
type eventCWDKey struct{}
type beforeHooksKey struct{}

// WithEventCWD preserves the caller's invocation directory through event dispatch.
func WithEventCWD(ctx context.Context, cwd string) context.Context {
	return context.WithValue(ctx, eventCWDKey{}, cwd)
}

func eventCWD(ctx context.Context, cfg project.Config) string {
	if cwd, _ := ctx.Value(eventCWDKey{}).(string); cwd != "" {
		return cwd
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return cfg.Discovery.ControlRoot
}

// WithEventOrigin identifies a caller without changing the mutation contract.
func WithEventOrigin(ctx context.Context, origin string) context.Context {
	e := eventContext{Origin: origin}
	if cause := os.Getenv("BL_EVENT_ID"); cause != "" {
		e.Cause = &cause
		depth, err := strconv.Atoi(os.Getenv("BL_HOOK_DEPTH"))
		if err != nil || depth < 0 {
			e.Depth = -1
		} else {
			e.Depth = depth + 1
		}
		inherited := os.Getenv("BL_EVENT_ORIGIN")
		if inherited == "hook" || inherited == "action" {
			e.Origin = inherited
		} else {
			e.Origin = "hook"
		}
	}
	return context.WithValue(ctx, eventContextKey{}, e)
}

type eventSource struct {
	source.Source
	cfg project.Config
}

func (s *eventSource) Initialize(ctx context.Context) error {
	if initializer, ok := s.Source.(interface{ Initialize(context.Context) error }); ok {
		return initializer.Initialize(ctx)
	}
	return nil
}

func (s *eventSource) LoadInitial(ctx context.Context) (source.Delta, error) {
	if loader, ok := s.Source.(source.InitialLoader); ok {
		return loader.LoadInitial(ctx)
	}
	return source.Delta{Coverage: source.Partial}, nil
}
func (s *eventSource) readState(ctx context.Context, id string) (model.Item, bool, error) {
	if reader, ok := s.Source.(interface {
		ReadState(context.Context, string) (model.Item, bool, error)
	}); ok {
		return reader.ReadState(ctx, id)
	}
	item, err := s.Source.Read(ctx, id)
	return item, false, err
}
func (s *eventSource) Prepare(ctx context.Context, m model.Mutation) (source.Prepared, error) {
	if preparer, ok := s.Source.(source.Preparer); ok {
		return preparer.Prepare(ctx, m)
	}
	return source.Prepared{}, source.Refuse("unavailable", "provider preparation unavailable")
}
func (s *eventSource) Watch(ctx context.Context, changed func()) error {
	if watcher, ok := s.Source.(source.Watcher); ok {
		return watcher.Watch(ctx, changed)
	}
	<-ctx.Done()
	return ctx.Err()
}

var errIncompleteEvents = errors.New("incomplete event snapshot")

func (s *eventSource) Load(ctx context.Context, c source.Cursor) (source.Delta, error) {
	var result source.Delta
	appended, err := eventTransaction(ctx, s.cfg, func(tx *hooks.Transaction, db *sql.DB) error {
		deltas, err := loadEventSources(ctx, s.cfg, s.Source)
		if err != nil {
			return err
		}
		revision := eventRevision(s.cfg, deltas)
		var prior string
		err = db.QueryRowContext(ctx, `SELECT revision FROM event_metadata`).Scan(&prior)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if revision == "" || revision != prior || err == sql.ErrNoRows {
			if err = reconcileEvents(ctx, s.cfg, tx, db, eventRecords(s.cfg, deltas), true, "sync"); err != nil {
				return err
			}
			if _, err = db.ExecContext(ctx, `UPDATE event_metadata SET revision=?`, revision); err != nil {
				return err
			}
		}
		result = deltas[s.Info().ID]
		if !c.IncludeArchive {
			hasArchive := false
			for _, archived := range result.Archived {
				hasArchive = hasArchive || archived
			}
			if hasArchive {
				items := make([]model.Item, 0, len(result.Items))
				var shared []*model.Item
				for n, item := range result.Items {
					if !result.Archived[item.Ref] {
						items = append(items, item)
						if len(result.ImmutableItems) == len(result.Items) {
							shared = append(shared, result.ImmutableItems[n])
						}
					}
				}
				result.Items = items
				result.ImmutableItems = shared
			}
		}
		result.Cursor.IncludeArchive = c.IncludeArchive
		return nil
	})
	if errors.Is(err, errIncompleteEvents) {
		return s.Source.Load(ctx, c)
	}
	if err == nil {
		startHookRunners(s.cfg, appended, eventCWD(ctx, s.cfg))
	}
	return result, err
}
func (s *eventSource) Apply(ctx context.Context, m model.Mutation) (model.Item, error) {
	if event, ok := ctx.Value(eventContextKey{}).(eventContext); ok && event.Depth < 0 {
		return model.Item{}, source.Refuse("rejected", "invalid causal depth")
	}
	var err error
	ctx, err = runBeforeMutation(ctx, s.cfg, m, s.Source)
	if err != nil {
		return model.Item{}, err
	}
	var result model.Item
	appended, err := eventTransaction(ctx, s.cfg, func(log *hooks.Transaction, db *sql.DB) error {
		var initialized int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM event_metadata`).Scan(&initialized); err != nil {
			return err
		}
		if initialized == 0 {
			records, _, err := eventSnapshot(ctx, s.cfg)
			if err != nil && !errors.Is(err, errIncompleteEvents) {
				return err
			}
			if err == nil {
				if err = reconcileEvents(ctx, s.cfg, log, db, records, true, "sync"); err != nil {
					return err
				}
			}
		}
		prior := eventRecord{Item: new(model.Item)}
		if m.Op != model.Create {
			item, archived, err := s.readState(ctx, m.Ref.ID)
			prior = eventRecord{Item: &item, Archived: archived}
			if err != nil {
				return err
			}
			if err = reconcileEvents(ctx, s.cfg, log, db, []eventRecord{prior}, false, "sync"); err != nil {
				return err
			}
		}
		var err error
		result, err = s.Source.Apply(ctx, m)
		if err != nil {
			return err
		}
		archived := prior.Archived
		if m.Op == model.Archive {
			archived = true
		}
		if m.Op == model.Unarchive {
			archived = false
		}
		origin := "cli"
		if e, ok := ctx.Value(eventContextKey{}).(eventContext); ok {
			origin = e.Origin
		}
		if prior.Item.Version == result.Version && prior.Archived == archived {
			e := makeEvent(ctx, s.cfg, result, "item.updated", origin, []string{result.Version, "noop", rand.Text()})
			_, err = log.Append(e)
			return err
		}
		return reconcileEvents(ctx, s.cfg, log, db, []eventRecord{{Item: &result, Archived: archived}}, false, origin)
	})
	if err == nil {
		startHookRunners(s.cfg, appended, eventCWD(ctx, s.cfg))
	}
	return result, err
}

func startHookRunners(cfg project.Config, events []hooks.Event, cwd string) {
	var synced, originated []hooks.Event
	for _, event := range events {
		if event.Origin == "sync" {
			synced = append(synced, event)
		} else {
			originated = append(originated, event)
		}
	}
	if len(synced) > 0 {
		syncCWD := cfg.Discovery.ControlRoot
		if syncCWD == "" {
			syncCWD = cwd
		}
		if err := hooks.StartRunner(cfg, synced, syncCWD); err != nil {
			fmt.Fprintf(os.Stderr, "bl: warning: cannot start hook runner: %v\n", err)
		}
	}
	if err := hooks.StartRunner(cfg, originated, cwd); err != nil {
		fmt.Fprintf(os.Stderr, "bl: warning: cannot start hook runner: %v\n", err)
	}
}

func runBeforeMutation(ctx context.Context, cfg project.Config, m model.Mutation, backend source.Source) (context.Context, error) {
	if done, _ := ctx.Value(beforeHooksKey{}).(bool); done {
		return ctx, nil
	}
	eventType := "item.updated"
	if m.Op == model.Create {
		eventType = "item.created"
	} else if m.Op == model.Archive {
		eventType = "item.archived"
	}
	ctx = context.WithValue(ctx, beforeHooksKey{}, true)
	if current, ok := ctx.Value(eventContextKey{}).(eventContext); ok && current.Depth > 3 {
		return ctx, nil
	}
	if !hooks.HasBefore(cfg, eventType) {
		return ctx, nil
	}
	var before *model.Item
	var after model.Item
	if m.Op == model.Create {
		if m.Item == nil {
			return ctx, source.Refuse("rejected", "create item template required")
		}
		after = *m.Item
		after.Ref.Source = m.Ref.Source
		for _, status := range cfg.Statuses {
			if after.Status == status.Key {
				after.Category = model.StatusCategory(status.Category)
			}
		}
	} else {
		prior, err := backend.Read(ctx, m.Ref.ID)
		if err != nil {
			return ctx, err
		}
		before = &prior
		preparer, ok := backend.(source.Preparer)
		if !ok {
			return ctx, source.Refuse("unavailable", "source cannot prepare a before-hook event")
		}
		prepared, err := preparer.Prepare(ctx, m)
		if err != nil {
			return ctx, err
		}
		after = prepared.Item
	}
	origin := "cli"
	if current, ok := ctx.Value(eventContextKey{}).(eventContext); ok && current.Origin != "" {
		origin = current.Origin
	}
	versions := []string{"", after.Version}
	if before != nil {
		versions[0] = before.Version
	}
	event := makeEvent(ctx, cfg, after, eventType, origin, versions)
	prior := model.Item{}
	if before != nil {
		prior = *before
	}
	event.Changes = itemChanges(prior, after)
	if m.Op == model.Archive {
		event.Changes["archived"] = hooks.Change{From: false, To: true}
	} else if m.Op == model.Unarchive {
		event.Changes["archived"] = hooks.Change{From: true, To: false}
	}
	if err := hooks.Before(ctx, cfg, event, eventCWD(ctx, cfg)); err != nil {
		return ctx, err
	}
	return ctx, nil
}

type eventRecord struct {
	Item     *model.Item
	Archived bool
}

func refString(r model.Ref) string { return r.Source + ":" + r.ID }
func eventRevision(cfg project.Config, deltas map[string]source.Delta) string {
	h := sha256.New()
	for _, source := range cfg.Sources {
		revision := deltas[source.ID].Cursor.Value
		if revision == "" {
			return ""
		}
		fmt.Fprintln(h, source.ID, revision)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
func eventSnapshot(ctx context.Context, cfg project.Config) ([]eventRecord, map[string]source.Delta, error) {
	deltas, err := loadEventSources(ctx, cfg, nil)
	if err != nil {
		return nil, nil, err
	}
	return eventRecords(cfg, deltas), deltas, nil
}
func loadEventSources(ctx context.Context, cfg project.Config, reuse source.Source) (map[string]source.Delta, error) {
	deltas := map[string]source.Delta{}
	for n, c := range cfg.Sources {
		// An inert source cannot establish a complete cross-source event snapshot.
		// Keep trusted sources browsable without interpreting its absence as deletion.
		if cfg.RequireTrusted(fmt.Sprintf("sources[%d]", n)) != nil {
			return nil, errIncompleteEvents
		}
		var backend source.Source
		if reuse != nil && reuse.Info().ID == c.ID {
			backend = reuse
		}
		if backend == nil {
			var err error
			backend, err = rawSource(cfg, c)
			if err != nil {
				return nil, err
			}
		}
		delta, err := backend.Load(ctx, source.Cursor{IncludeArchive: true})
		if err != nil {
			return nil, err
		}
		if delta.Coverage != source.Complete {
			return nil, errIncompleteEvents
		}
		deltas[c.ID] = delta
	}
	return deltas, nil
}
func eventRecords(cfg project.Config, deltas map[string]source.Delta) []eventRecord {
	count := 0
	for _, delta := range deltas {
		count += len(delta.Items)
	}
	records := make([]eventRecord, 0, count)
	for _, source := range cfg.Sources {
		delta := deltas[source.ID]
		for n := range delta.Items {
			item := &delta.Items[n]
			records = append(records, eventRecord{Item: item, Archived: delta.Archived[item.Ref]})
		}
	}
	return records
}
func eventTransaction(ctx context.Context, cfg project.Config, fn func(*hooks.Transaction, *sql.DB) error) ([]hooks.Event, error) {
	var appended []hooks.Event
	err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Update(ctx, func(log *hooks.Transaction) error {
		key := sha256.Sum256([]byte(cfg.Project.ID))
		dir := filepath.Join(cfg.Paths.CacheDir, "events")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		db, err := sql.Open("sqlite", filepath.Join(dir, fmt.Sprintf("%x.sqlite", key)))
		if err != nil {
			return err
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		if _, err = db.ExecContext(ctx, `PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS event_records (ref TEXT PRIMARY KEY, version TEXT NOT NULL, archived INTEGER NOT NULL, item BLOB NOT NULL, graph BLOB NOT NULL); CREATE TABLE IF NOT EXISTS event_metadata (initialized INTEGER NOT NULL, revision TEXT NOT NULL DEFAULT ''); CREATE TABLE IF NOT EXISTS event_edges (item TEXT NOT NULL, owner TEXT NOT NULL, target TEXT NOT NULL); CREATE INDEX IF NOT EXISTS event_edge_target ON event_edges(target); CREATE INDEX IF NOT EXISTS event_edge_owner ON event_edges(owner); CREATE INDEX IF NOT EXISTS event_edge_item ON event_edges(item);`); err != nil {
			return err
		}
		if err := fn(log, db); err != nil {
			return err
		}
		appended = log.Appended()
		return nil
	})
	return appended, err
}

type eventIdentity struct {
	Version  string
	Archived bool
}
type eventTransition struct {
	Before, After eventRecord
	Deleted       bool
}

// The baseline is per item: unchanged snapshots read only identities, and a
// mutation touches one full item. Readiness reads a compact graph, never bodies.
func reconcileEvents(ctx context.Context, cfg project.Config, log *hooks.Transaction, db *sql.DB, records []eventRecord, full bool, origin string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var initialized int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM event_metadata`).Scan(&initialized); err != nil {
		return err
	}
	old := map[string]eventIdentity{}
	if full {
		rows, e := tx.QueryContext(ctx, `SELECT ref,version,archived FROM event_records`)
		if e != nil {
			return e
		}
		for rows.Next() {
			var ref string
			var v eventIdentity
			if err = rows.Scan(&ref, &v.Version, &v.Archived); err != nil {
				rows.Close()
				return err
			}
			old[ref] = v
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	} else {
		for _, r := range records {
			var v eventIdentity
			err = tx.QueryRowContext(ctx, `SELECT version,archived FROM event_records WHERE ref=?`, eventKey(r.Item.Ref)).Scan(&v.Version, &v.Archived)
			if err == nil {
				old[eventKey(r.Item.Ref)] = v
			} else if err != sql.ErrNoRows {
				return err
			}
		}
	}
	transitions := make([]eventTransition, 0, len(records))
	empty := eventRecord{Item: new(model.Item)}
	for _, r := range records {
		ref := eventKey(r.Item.Ref)
		identity, exists := old[ref]
		delete(old, ref)
		if exists && identity.Version == r.Item.Version && identity.Archived == r.Archived {
			continue
		}
		change := eventTransition{Before: empty, After: r}
		if exists {
			change.Before = eventRecord{Item: new(model.Item)}
			var data []byte
			if err = tx.QueryRowContext(ctx, `SELECT item FROM event_records WHERE ref=?`, ref).Scan(&data); err != nil {
				return err
			}
			if err = json.Unmarshal(data, &change.Before.Item); err != nil {
				return err
			}
			change.Before.Archived = identity.Archived
		}
		transitions = append(transitions, change)
	}
	if full {
		refs := make([]string, 0, len(old))
		for ref := range old {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			var data []byte
			var r eventRecord
			if err = tx.QueryRowContext(ctx, `SELECT item FROM event_records WHERE ref=?`, ref).Scan(&data); err != nil {
				return err
			}
			if err = json.Unmarshal(data, &r.Item); err != nil {
				return err
			}
			r.Archived = old[ref].Archived
			transitions = append(transitions, eventTransition{Before: r, After: empty, Deleted: true})
		}
	}
	if len(transitions) == 0 {
		if initialized == 0 && full {
			if _, err = tx.ExecContext(ctx, `INSERT INTO event_metadata(initialized) VALUES(1)`); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	var beforeGraph map[model.Ref]graphRecord
	var affected map[string]bool
	needsGraph := false
	for _, change := range transitions {
		needsGraph = needsGraph || change.Deleted || !reflect.DeepEqual(graphOf(change.Before), graphOf(change.After))
	}
	if initialized > 0 && needsGraph {
		affected, err = affectedEventGraph(ctx, tx, transitions)
		if err != nil {
			return err
		}
		beforeGraph, err = loadEventGraph(ctx, tx, affected)
		if err != nil {
			return err
		}
	}
	if initialized > 0 || !full {
		for _, change := range transitions {
			r := change.After
			kind := "item.updated"
			if change.Before.Item.Version == "" {
				kind = "item.created"
			}
			if r.Archived && !change.Before.Archived || change.Deleted {
				kind = "item.archived"
			}
			if change.Deleted {
				r = change.Before
			}
			versions := transitionVersions(change)
			e := makeEvent(ctx, cfg, *r.Item, kind, origin, versions)
			e.Changes = itemChanges(*change.Before.Item, *change.After.Item)
			if change.Before.Archived != change.After.Archived {
				e.Changes["archived"] = hooks.Change{From: change.Before.Archived, To: change.After.Archived}
			}
			if change.Deleted {
				e.Changes = map[string]hooks.Change{"present": {From: true, To: false}}
			}
			if _, err = log.Append(e); err != nil {
				return err
			}
		}
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO event_records(ref,version,archived,item,graph) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	edgeStmt, err := tx.PrepareContext(ctx, `INSERT INTO event_edges(item,owner,target) VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	defer edgeStmt.Close()
	for _, change := range transitions {
		ref := change.After.Item.Ref
		if change.Deleted {
			ref = change.Before.Item.Ref
		}
		if needsGraph {
			if _, err = tx.ExecContext(ctx, `DELETE FROM event_edges WHERE item=?`, eventKey(ref)); err != nil {
				return err
			}
		}
		if needsGraph && !change.Deleted {
			if err = writeEventEdges(ctx, edgeStmt, *change.After.Item); err != nil {
				return err
			}
		}
		if change.Deleted {
			if _, err = tx.ExecContext(ctx, `DELETE FROM event_records WHERE ref=?`, eventKey(change.Before.Item.Ref)); err != nil {
				return err
			}
			continue
		}
		r := change.After
		item, e := json.Marshal(r.Item)
		if e != nil {
			return e
		}
		graph, e := json.Marshal(graphOf(r))
		if e != nil {
			return e
		}
		if _, err = stmt.ExecContext(ctx, eventKey(r.Item.Ref), r.Item.Version, r.Archived, item, graph); err != nil {
			return err
		}
	}
	if initialized > 0 && needsGraph {
		from := graphReadiness(cfg, beforeGraph)
		afterGraph, e := loadEventGraph(ctx, tx, affected)
		if e != nil {
			return e
		}
		to := graphReadiness(cfg, afterGraph)
		var versions []string
		for _, change := range transitions {
			versions = append(versions, transitionVersions(change)...)
		}
		refs := make([]model.Ref, 0, len(to))
		for ref := range to {
			refs = append(refs, ref)
		}
		sort.Slice(refs, func(i, j int) bool { return refString(refs[i]) < refString(refs[j]) })
		for _, ref := range refs {
			prior, exists := from[ref]
			next := to[ref]
			if !affected[eventKey(ref)] || !exists || prior.Readiness == next.Readiness {
				continue
			}
			var data []byte
			if err = tx.QueryRowContext(ctx, `SELECT item FROM event_records WHERE ref=?`, eventKey(ref)).Scan(&data); err != nil {
				return err
			}
			var item model.Item
			if err = json.Unmarshal(data, &item); err != nil {
				return err
			}
			e := makeEvent(ctx, cfg, item, "item.readiness", origin, versions)
			e.Changes = map[string]hooks.Change{"readiness": {From: prior.Readiness, To: next.Readiness, Reason: next.Reasons}}
			if _, err = log.Append(e); err != nil {
				return err
			}
		}
	}
	if initialized == 0 && full {
		if _, err = tx.ExecContext(ctx, `INSERT INTO event_metadata(initialized) VALUES(1)`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func transitionVersions(c eventTransition) []string {
	ref := c.After.Item.Ref
	if c.Deleted {
		ref = c.Before.Item.Ref
	}
	return []string{refString(ref), c.Before.Item.Version, c.After.Item.Version, strconv.FormatBool(c.Before.Archived), strconv.FormatBool(c.After.Archived), strconv.FormatBool(c.Deleted)}
}

type graphRecord struct {
	Ref                 model.Ref
	Category            model.StatusCategory
	Parent              *model.Ref
	Children, BlockedBy []model.Ref
	Labels, Assignees   []string
	Archived            bool
}

func graphOf(r eventRecord) graphRecord {
	i := r.Item
	return graphRecord{i.Ref, i.Category, i.Parent, i.Children, i.BlockedBy, i.Labels, i.Assignees, r.Archived}
}
func eventKey(ref model.Ref) string { return refString(canonical(ref)) }
func qualifiedEventRef(ref, owner model.Ref) model.Ref {
	if ref.Source == "" {
		ref.Source = owner.Source
	}
	return ref
}
func writeEventEdges(ctx context.Context, stmt *sql.Stmt, item model.Item) error {
	key := eventKey(item.Ref)
	for _, ref := range append(append([]model.Ref{}, item.BlockedBy...), item.Children...) {
		if _, err := stmt.ExecContext(ctx, key, key, eventKey(qualifiedEventRef(ref, item.Ref))); err != nil {
			return err
		}
	}
	if item.Parent != nil {
		if _, err := stmt.ExecContext(ctx, key, eventKey(qualifiedEventRef(*item.Parent, item.Ref)), key); err != nil {
			return err
		}
	}
	return nil
}
func eventKeyQuery(ctx context.Context, tx *sql.Tx, query string, keys map[string]bool, visit func(*sql.Rows) error) error {
	list := make([]string, 0, len(keys))
	for key := range keys {
		list = append(list, key)
	}
	sort.Strings(list)
	for start := 0; start < len(list); start += 400 {
		batch := list[start:min(start+400, len(list))]
		args := make([]any, len(batch))
		for n, key := range batch {
			args[n] = key
		}
		rows, err := tx.QueryContext(ctx, query+" ("+strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")+")", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err = visit(rows); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func affectedEventGraph(ctx context.Context, tx *sql.Tx, changes []eventTransition) (map[string]bool, error) {
	affected := map[string]bool{}
	for _, change := range changes {
		for _, r := range []eventRecord{change.Before, change.After} {
			if r.Item.Ref.ID != "" {
				affected[eventKey(r.Item.Ref)] = true
			}
			if r.Item.Parent != nil {
				affected[eventKey(qualifiedEventRef(*r.Item.Parent, r.Item.Ref))] = true
			}
		}
	}
	frontier := affected
	for len(frontier) > 0 {
		next := map[string]bool{}
		err := eventKeyQuery(ctx, tx, `SELECT owner FROM event_edges WHERE target IN`, frontier, func(rows *sql.Rows) error {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			if !affected[key] {
				affected[key] = true
				next[key] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		frontier = next
	}
	return affected, nil
}
func loadEventGraph(ctx context.Context, tx *sql.Tx, affected map[string]bool) (map[model.Ref]graphRecord, error) {
	keys := map[string]bool{}
	for key := range affected {
		keys[key] = true
	}
	if err := eventKeyQuery(ctx, tx, `SELECT target FROM event_edges WHERE owner IN`, affected, func(rows *sql.Rows) error {
		var key string
		if err := rows.Scan(&key); err != nil {
			return err
		}
		keys[key] = true
		return nil
	}); err != nil {
		return nil, err
	}
	graph := map[model.Ref]graphRecord{}
	err := eventKeyQuery(ctx, tx, `SELECT graph FROM event_records WHERE ref IN`, keys, func(rows *sql.Rows) error {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return err
		}
		var r graphRecord
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		graph[canonical(r.Ref)] = r
		return nil
	})
	return graph, err
}
func graphReadiness(cfg project.Config, graph map[model.Ref]graphRecord) map[model.Ref]Explanation {
	in := Input{Config: cfg.Ready, Me: []string{cfg.Me.Default}, Archived: map[model.Ref]bool{}}
	for ref, r := range graph {
		in.Items = append(in.Items, model.Item{Ref: ref, Category: r.Category, Parent: r.Parent, Children: r.Children, BlockedBy: r.BlockedBy, Labels: r.Labels, Assignees: r.Assignees})
		in.Archived[ref] = r.Archived
	}
	result, _, _ := assess(in)
	out := map[model.Ref]Explanation{}
	for _, e := range result.Items {
		out[e.Ref] = e
	}
	return out
}
func makeEvent(ctx context.Context, cfg project.Config, item model.Item, kind, origin string, versions []string) hooks.Event {
	e := hooks.Event{Project: cfg.Project.ID, Source: item.Ref.Source, Ref: refString(item.Ref), Item: eventItemData(item), Type: kind, Origin: origin, Actor: cfg.Me.Default, Versions: versions}
	if c, _ := Coordination(ctx); c != nil {
		e.Resource, _ = c.Resource(item)
	}
	if origin != "sync" {
		if c, ok := ctx.Value(eventContextKey{}).(eventContext); ok {
			e.Cause = c.Cause
			e.Depth = c.Depth
		}
	}
	return e
}
func eventItemData(item model.Item) map[string]any {
	out := map[string]any{}
	v := reflect.ValueOf(item)
	typ := v.Type()
	for n := 0; n < v.NumField(); n++ {
		name := typ.Field(n).Name
		if name == "Edges" {
			continue
		}
		name = eventFieldName(name)
		out[name] = v.Field(n).Interface()
	}
	return out
}
func eventFieldName(name string) string {
	if name == "URL" {
		return "url"
	}
	if name == "DoD" {
		return "dod"
	}
	return strings.ToLower(name[:1]) + name[1:]
}
func itemChanges(before, after model.Item) map[string]hooks.Change {
	out := map[string]hooks.Change{}
	a, b := reflect.ValueOf(before), reflect.ValueOf(after)
	typ := a.Type()
	for n := 0; n < a.NumField(); n++ {
		name := typ.Field(n).Name
		if name == "Version" || name == "Ref" || name == "Edges" || name == "Children" || name == "Blocks" {
			continue
		}
		if !reflect.DeepEqual(a.Field(n).Interface(), b.Field(n).Interface()) {
			name = eventFieldName(name)
			switch name {
			case "blockedBy":
				name = "blocked_by"
			case "duplicateOf":
				name = "duplicate_of"
			}
			out[name] = hooks.Change{From: a.Field(n).Interface(), To: b.Field(n).Interface()}
		}
	}
	return out
}

// EventMatcher evaluates q, parsed with query.Options{Hook: true}, against
// logged events. Readiness and archive state come from each event's changes.
func EventMatcher(cfg project.Config, q query.Query) func(hooks.Event) bool {
	snapshot := query.Snapshot{Settings: cfg.Settings, Me: []string{cfg.Me.Default}, States: map[model.Ref]query.State{}}
	return func(e hooks.Event) bool {
		var item model.Item
		data, _ := json.Marshal(e.Item)
		_ = json.Unmarshal(data, &item)
		snapshot.Now = e.Time
		state := query.State{}
		if change, ok := e.Changes["readiness"]; ok {
			state.Readiness = fmt.Sprint(change.To)
		}
		if change, ok := e.Changes["archived"]; ok {
			state.Archived = change.To == true
		}
		snapshot.States[item.Ref] = state
		event := query.Event{Type: e.Type, Outcome: e.Outcome}
		if e.Type == "hook.finished" {
			event.Hook = e.Name
		}
		if e.Type == "action.finished" {
			event.Action = e.Name
		}
		for field := range e.Changes {
			event.Changed = append(event.Changed, field)
		}
		return q.Match(item, snapshot, &event)
	}
}

// EventPage is one bounded read of the event log. Cursor is the ID to pass as
// the next since: the last returned event when More, otherwise the last
// logged event, or since itself when nothing is newer.
type EventPage struct {
	Events []hooks.Event
	Cursor string
	More   bool
}

// ReadEvents returns events after since that match, at most limit (0: all).
// An unknown since is a not-found refusal.
func ReadEvents(ctx context.Context, cfg project.Config, since string, match func(hooks.Event) bool, limit int) (EventPage, error) {
	events, err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Read(ctx, since)
	if errors.Is(err, hooks.ErrUnknownCursor) {
		return EventPage{}, source.Refuse("not-found", err.Error())
	}
	if err != nil {
		return EventPage{}, source.Refuse("unavailable", err.Error())
	}
	page := EventPage{Events: []hooks.Event{}, Cursor: since}
	for _, e := range events {
		if !match(e) {
			page.Cursor = e.ID
			continue
		}
		if limit > 0 && len(page.Events) == limit {
			page.More = true
			break
		}
		page.Events = append(page.Events, e)
		page.Cursor = e.ID
	}
	return page, nil
}
