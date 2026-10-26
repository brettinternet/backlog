// Package source defines backend contracts and a per-project source registry.
package source

import (
	"context"
	"fmt"

	"github.com/brettinternet/backlog/internal/model"
)

type Coverage string

const (
	Complete Coverage = "complete"
	Partial  Coverage = "partial"
	Unknown  Coverage = "unknown"
)

type Cursor struct {
	Value          string
	IncludeArchive bool
	// Reconcile requests a full scan instead of a change window. It is a
	// per-load request, never persisted with the cursor.
	Reconcile bool
	// Sprints asks a sprint-capable provider for item sprint keys and a
	// fresh sprint catalog. Without it, providers make no sprint requests.
	Sprints bool
}
type Finding struct {
	Code, Severity, Path, Message string
	Line                          int
}
type Delta struct {
	// ImmutableItems optionally supplies shared, immutable storage for Items,
	// in the same order. Nil entries use defensive copying. Neither source nor
	// consumers may mutate these items. Public snapshot reads still copy.
	ImmutableItems []*model.Item
	// Archived identifies archived items when IncludeArchive is requested.
	Archived map[model.Ref]bool
	Items    []model.Item
	Findings []Finding
	Coverage Coverage
	// Incremental means Items are changes since Cursor, not a full enumeration.
	// Even complete incremental coverage cannot evict unseen rows.
	Incremental bool
	// RetainUnseen preserves cached freshness for rows omitted by a successful
	// change window. It never refreshes an already stale row or proves absence.
	RetainUnseen bool
	// Stale rows may be displayed, but cannot satisfy dependencies or be ready.
	Stale map[model.Ref]bool
	// Sprints is the provider's complete sprint catalog for this response,
	// refreshed on every sprint-enabled load independently of the cursor.
	Sprints []model.Sprint
	Cursor  Cursor
	// TextSearch optionally narrows matches for this snapshot. Nil results or
	// errors require in-memory fallback.
	TextSearch func(context.Context, string) (map[model.Ref]bool, error)
}
type Info struct {
	ID, Kind, Display string
	Capabilities      model.Capabilities
}
type Source interface {
	Info() Info
	Load(context.Context, Cursor) (Delta, error)
	Read(context.Context, string) (model.Item, error)
	Apply(context.Context, model.Mutation) (model.Item, error)
}

// InitialLoader optionally supplies a bounded, authoritative partial snapshot
// before Load. It runs only at startup on the source worker, never on the UI
// loop. Load must still follow, even if the initial read fails.
type InitialLoader interface {
	LoadInitial(context.Context) (Delta, error)
}

// Prepared holds an optimistic result and a version-bound inverse. Native
// sources derive it from the lossless document, not a reserialized Item.
// An empty Inverse.Op marks a nonundoable operation.
type Prepared struct {
	Item    model.Item
	Inverse model.Mutation
}
type Preparer interface {
	Prepare(context.Context, model.Mutation) (Prepared, error)
}

// Watch is optional and implemented by backends that support signals.
type Watcher interface {
	Watch(context.Context, func()) error
}

// Error carries a stable machine-readable refusal reason.
type Error struct{ Reason, Message string }

func (e *Error) Error() string            { return e.Reason + ": " + e.Message }
func Refuse(reason, message string) error { return &Error{reason, message} }

type Registry map[string]Source

func (r Registry) Register(s Source) error {
	id := s.Info().ID
	if id == "" || r[id] != nil {
		return fmt.Errorf("invalid or duplicate source ID %q", id)
	}
	r[id] = s
	return nil
}
