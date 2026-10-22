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
}
type Finding struct {
	Code, Severity, Path, Message string
	Line                          int
}
type Delta struct {
	Items    []model.Item
	Findings []Finding
	Coverage Coverage
	Cursor   Cursor
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

// Watch is optional and will be implemented by backends that support signals.
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
