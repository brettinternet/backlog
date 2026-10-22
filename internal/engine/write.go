package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

const WriteFields = "title status type priority assignees labels milestone parent blocked_by related duplicate_of rank due refs files"

// ResolveRef expands an item reference using the same shorthand rules as CLI writes.
func ResolveRef(ctx context.Context, cfg project.Config, sourceID string, ref model.Ref) (model.Ref, error) {
	if ref.Source == sourceID {
		ref.Source = ""
	}
	lookup := ref.Source
	if lookup == "" {
		lookup = sourceID
	}
	matches, err := ReadDetail(ctx, cfg, lookup+":"+ref.ID)
	if err != nil {
		return ref, err
	}
	if len(matches) == 1 {
		ref.ID = matches[0].Ref.ID
		return ref, nil
	}
	if _, err := strconv.ParseUint(ref.ID, 10, 64); err == nil {
		for _, c := range cfg.Sources {
			if c.ID == lookup {
				ref.ID = c.Prefix + "-" + ref.ID
			}
		}
	}
	return ref, nil
}

func ListField(field string) bool {
	return slices.Contains(strings.Fields("assignees labels blocked_by related refs files"), field)
}

// ResolveChanges expands reference shorthand in mutations before applying them.
func ResolveChanges(ctx context.Context, cfg project.Config, sourceID string, changes []model.Mutation) ([]model.Mutation, error) {
	resolve := func(value string) (string, error) {
		if value == "" {
			return value, nil
		}
		ref := model.Ref{ID: value}
		if source, id, ok := strings.Cut(value, ":"); ok {
			ref = model.Ref{Source: source, ID: id}
		}
		resolved, err := ResolveRef(ctx, cfg, sourceID, ref)
		if resolved.Source != "" {
			return resolved.Source + ":" + resolved.ID, err
		}
		return resolved.ID, err
	}
	out := make([]model.Mutation, len(changes))
	for n, m := range changes {
		switch m.Field {
		case "parent", "duplicate_of", "blocked_by", "related":
			switch value := m.Value.(type) {
			case string:
				resolved, err := resolve(value)
				if err != nil {
					return nil, err
				}
				m.Value = resolved
			case []string:
				values := make([]string, len(value))
				for k, raw := range value {
					resolved, err := resolve(raw)
					if err != nil {
						return nil, err
					}
					values[k] = resolved
				}
				m.Value = values
			}
		}
		out[n] = m
	}
	return out, nil
}

// ErrorReason classifies engine/source failures for machine-facing adapters.
func ErrorReason(err error) string {
	var refusal *source.Error
	if errors.As(err, &refusal) {
		switch refusal.Reason {
		case "conflict", "not-found", "unavailable", "rejected":
			return refusal.Reason
		}
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return "unavailable"
	}
	return "rejected"
}

// ErrorMessage preserves source refusal text without its machine reason prefix.
func ErrorMessage(err error) string {
	var refusal *source.Error
	if errors.As(err, &refusal) {
		return refusal.Message
	}
	return err.Error()
}

// ApplyWrite is synchronous and UI-independent. Every operation is validated
// before the source applies the entire mutation under its lock. Callers bind
// ExpectedVersion to the version they read, even without an explicit CLI guard.
func ApplyWrite(ctx context.Context, cfg project.Config, m model.Mutation) (model.Item, error) {
	var backend source.Source
	for _, c := range cfg.Sources {
		if c.ID == m.Ref.Source {
			s, err := readSource(cfg, c)
			if err != nil {
				return model.Item{}, source.Refuse("unavailable", err.Error())
			}
			backend = s
			break
		}
	}
	if backend == nil {
		return model.Item{}, source.Refuse("unavailable", "unknown source "+m.Ref.Source)
	}
	if err := validateWrite(cfg, backend.Info().Capabilities, m); err != nil {
		return model.Item{}, err
	}
	return backend.Apply(ctx, m)
}

func validateWrite(cfg project.Config, caps model.Capabilities, m model.Mutation) error {
	reject := func(message string) error { return source.Refuse("rejected", message) }
	if !caps.Allows(m.Op) {
		return source.Refuse("unavailable", "unsupported operation "+string(m.Op))
	}
	switch m.Op {
	case model.Batch:
		if len(m.Changes) == 0 {
			return reject("empty mutation")
		}
		for _, change := range m.Changes {
			if change.Op == model.Batch || change.Op == model.Create || change.Op == model.Restore || change.Op == model.Archive || change.Op == model.Unarchive || (change.Ref != (model.Ref{}) && change.Ref != m.Ref) {
				return reject("invalid batch operation")
			}
			if err := validateWrite(cfg, caps, change); err != nil {
				return err
			}
		}
	case model.Create:
		if m.ExpectedVersion != "" {
			return source.Refuse("conflict", "a new item has no existing version")
		}
		if m.Item == nil {
			return reject("item template required")
		}
		return ValidateItem(cfg, *m.Item)
	case model.Set, model.Add, model.Remove, model.Unset:
		if !slices.Contains(strings.Fields(WriteFields), m.Field) {
			return reject("unknown field " + m.Field + "; allowed fields: " + WriteFields)
		}
		if (m.Op == model.Add || m.Op == model.Remove) && !ListField(m.Field) {
			return reject(m.Field + " accepts = only; allowed list fields: assignees labels blocked_by related refs files")
		}
		if m.Field == "status" || m.Field == "priority" {
			value, _ := m.Value.(string)
			allowed := []string{"urgent", "high", "medium", "low", ""}
			if m.Field == "status" {
				allowed = nil
				for _, s := range cfg.Statuses {
					allowed = append(allowed, s.Key)
				}
			}
			if m.Op == model.Unset && m.Field == "priority" {
				return nil
			}
			if !slices.Contains(allowed, value) {
				return reject(fmt.Sprintf("unknown %s %q; allowed values: %s", m.Field, value, strings.Join(allowed, ", ")))
			}
		}
	case model.Restore:
		doc, err := format.Parse(m.Original)
		if err != nil {
			return reject(err.Error())
		}
		return ValidateItem(cfg, doc.Item())
	}
	return nil
}

// ValidateItem also validates whole-file editor saves and creation templates.
func ValidateItem(cfg project.Config, item model.Item) error {
	allowed := []string{}
	for _, s := range cfg.Statuses {
		allowed = append(allowed, s.Key)
	}
	if !slices.Contains(allowed, item.Status) {
		return source.Refuse("rejected", "unknown status "+item.Status+"; allowed values: "+strings.Join(allowed, ", "))
	}
	if item.Ref.ID == "" {
		item.Ref.ID = "BL-1"
	}
	if _, err := format.Serialize(item); err != nil {
		return source.Refuse("rejected", err.Error())
	}
	return nil
}
