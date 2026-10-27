package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

const WriteFields = "title status type priority assignees labels milestone sprint parent blocked_by related duplicate_of rank due refs files"

// ResolveRef expands an item reference using the same shorthand rules as CLI
// writes, and reports whether it names exactly one existing item. An
// unqualified ref missing locally takes the qualified ID of the only other
// native source that has it; otherwise it stays local.
func ResolveRef(ctx context.Context, cfg project.Config, sourceID string, ref model.Ref) (model.Ref, bool, error) {
	if ref.Source == sourceID {
		ref.Source = ""
	}
	lookup := ref.Source
	if lookup == "" {
		lookup = sourceID
	}
	matches, err := ReadDetail(ctx, cfg, lookup+":"+ref.ID)
	if err != nil {
		return ref, false, err
	}
	if len(matches) == 1 {
		ref.ID = matches[0].Ref.ID
		return ref, true, nil
	}
	if ref.Source == "" {
		var found []model.Ref
		for _, c := range cfg.Sources {
			if c.ID == sourceID || c.Type != "native" {
				continue
			}
			other, err := ReadDetail(ctx, cfg, c.ID+":"+ref.ID)
			if err != nil {
				return ref, false, err
			}
			for _, item := range other {
				found = append(found, model.Ref{Source: c.ID, ID: item.Ref.ID})
			}
		}
		if len(found) == 1 {
			return found[0], true, nil
		}
	}
	if _, err := strconv.ParseUint(ref.ID, 10, 64); err == nil {
		for _, c := range cfg.Sources {
			if c.ID == lookup && c.Prefix != "" {
				ref.ID = c.Prefix + "-" + ref.ID
			}
		}
	}
	return ref, false, nil
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
		resolved, _, err := ResolveRef(ctx, cfg, sourceID, ref)
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
	var diagnostic *oauth.Diagnostic
	if errors.As(err, &diagnostic) && diagnostic != nil {
		return diagnostic.Code
	}
	var refusal *source.Error
	if errors.As(err, &refusal) {
		switch refusal.Reason {
		case "conflict", "not-found", "unavailable", "rejected", "contention", "claim-held", "run-mismatch", "session-required", "unknown-outcome", "usage":
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
	var diagnostic *oauth.Diagnostic
	if errors.As(err, &diagnostic) && diagnostic != nil {
		return diagnostic.Message
	}
	var refusal *source.Error
	if errors.As(err, &refusal) {
		return refusal.Message
	}
	return err.Error()
}

// ErrorCreated names the item a failed create left behind, as source:ID, or
// returns "" when the create applied nothing or succeeded.
func ErrorCreated(err error) string {
	var refusal *source.Error
	if errors.As(err, &refusal) && refusal.Created != nil {
		return refusal.Created.Source + ":" + refusal.Created.ID
	}
	return ""
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
	if initializer, ok := backend.(interface{ Initialize(context.Context) error }); ok {
		if err := initializer.Initialize(ctx); err != nil {
			return model.Item{}, err
		}
	}
	m, err := resolveSprintWrite(ctx, cfg, backend, m)
	if err != nil {
		return model.Item{}, err
	}
	if err := validateWrite(cfg, m.Ref.Source, backend.Info().Capabilities, m); err != nil {
		return model.Item{}, err
	}
	ctx, err = runBeforeMutation(ctx, cfg, m, backend)
	if err != nil {
		return model.Item{}, err
	}
	return coordinatedWrite(ctx, cfg, m, func() (model.Item, error) { return backend.Read(ctx, m.Ref.ID) }, func() (model.Item, error) { return backend.Apply(ctx, m) })
}

// validateWrite checks m against the configuration of its source, whose ID in
// cfg is sourceID (a workspace qualifies m.Ref.Source).
func validateWrite(cfg project.Config, sourceID string, caps model.Capabilities, m model.Mutation) error {
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
			change.Ref = m.Ref
			if err := validateWrite(cfg, sourceID, caps, change); err != nil {
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
		if m.Item.Sprint != "" && !sprintSource(cfg, sourceID) {
			return reject("source " + m.Ref.Source + " has no sprints; only Linear and Jira sources do")
		}
		for _, configured := range cfg.Sources {
			if configured.ID == sourceID && configured.Type == "linear" {
				if err := linearCreate(sourceID, *m.Item); err != nil {
					return err
				}
			}
		}
		return ValidateItem(cfg, *m.Item)
	case model.Set, model.Add, model.Remove, model.Unset:
		if !slices.Contains(strings.Fields(WriteFields), m.Field) {
			return reject("unknown field " + m.Field + "; allowed fields: " + WriteFields)
		}
		if (m.Op == model.Add || m.Op == model.Remove) && !ListField(m.Field) {
			return reject(m.Field + " accepts = only; allowed list fields: assignees labels blocked_by related refs files")
		}
		if m.Field == "sprint" {
			if !sprintSource(cfg, sourceID) {
				return reject("source " + m.Ref.Source + " has no sprints; only Linear and Jira sources do")
			}
			if _, ok := m.Value.(string); !ok || m.Op != model.Set || m.ExpectedSprint == nil {
				return reject("sprint requires sprint=VALUE and the observed sprint")
			}
		}
		if m.Field == "status" || m.Field == "priority" {
			value, _ := m.Value.(string)
			allowed := []string{"urgent", "high", "medium", "low", ""}
			if m.Field == "status" {
				for _, configured := range cfg.Sources {
					if configured.ID == sourceID && configured.Type == "jira" {
						return nil
					} // Per-issue transitions validate at the source.
				}
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
		for _, configured := range cfg.Sources {
			if configured.ID == sourceID && configured.Type == "backlog-md" {
				// A provider-issued inverse contains its own file format. The
				// adapter validates it under the same version-checked write lock.
				return nil
			}
		}
		doc, err := format.Parse(m.Original)
		if err != nil {
			return reject(err.Error())
		}
		return ValidateItem(cfg, doc.Item())
	}
	return nil
}

// linearCreate names the template fields issueCreate can't set, before
// the remote wrapper reduces source refusals to a generic message.
func linearCreate(sourceID string, item model.Item) error {
	refs := item.BlockedBy
	if item.Parent != nil {
		refs = append([]model.Ref{*item.Parent}, refs...)
	}
	for _, ref := range refs {
		if ref.Source != "" && ref.Source != sourceID && !strings.HasPrefix(ref.Source, "linear/") {
			return source.Refuse("rejected", "Linear parent and blocked_by must be Linear issues, not "+ref.Source+":"+ref.ID)
		}
	}
	if len(item.Assignees) > 1 {
		return source.Refuse("rejected", "Linear issues take at most one assignee")
	}
	var unsupported []string
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"type", item.Type != "" && item.Type != "issue"}, {"rank", item.Rank != ""}, {"milestone", item.Milestone != ""}, {"due", item.Due != ""},
		{"blocks", len(item.Blocks) > 0}, {"related", len(item.Related) > 0}, {"duplicate_of", item.DuplicateOf != nil}, {"refs", len(item.Refs) > 0}, {"files", len(item.Files) > 0},
		{"criteria", len(item.Criteria) > 0}, {"dod", len(item.DoD) > 0}, {"plan", item.Plan != ""}, {"notes", item.Notes != ""}, {"summary", item.Summary != ""},
	} {
		if f.set {
			unsupported = append(unsupported, f.name)
		}
	}
	if len(unsupported) > 0 {
		return source.Refuse("rejected", "Linear create doesn't support "+strings.Join(unsupported, ", ")+"; it sets title, body, status, assignee, priority, labels, sprint, parent and blocked_by")
	}
	return nil
}

// CreateSource resolves the source a new item goes to; the first match wins:
// explicit (--source or MCP source) or a +ID quick-add token, which must agree;
// defaults.source; then the only source that can create. caps reports a
// source's capabilities, nil when they can't be read (the write then reports
// why); a nil caps opens each source to ask it. In a workspace, a bare ID names
// the anchor project's source.
func CreateSource(ctx context.Context, cfg project.Config, explicit, token string, caps func(string) model.Capabilities) (string, error) {
	if explicit != "" && token != "" && explicit != token {
		return "", source.Refuse("usage", "--source "+explicit+" and +"+token+" name different sources")
	}
	if caps == nil {
		caps = func(id string) model.Capabilities {
			for _, c := range cfg.Sources {
				if c.ID != id {
					continue
				}
				backend, err := readSource(cfg, c)
				if err != nil {
					return nil
				}
				if initializer, ok := backend.(interface{ Initialize(context.Context) error }); ok && initializer.Initialize(ctx) != nil {
					return nil
				}
				return backend.Info().Capabilities
			}
			return nil
		}
	}
	creatable := func() (ids []string) {
		for _, c := range cfg.Sources {
			if caps(c.ID).Allows(model.Create) {
				ids = append(ids, c.ID)
			}
		}
		return ids
	}
	refuse := func(message string) error {
		if ids := creatable(); len(ids) > 0 {
			return source.Refuse("usage", message+"; creatable sources: "+strings.Join(ids, ", "))
		}
		return source.Refuse("usage", message+"; no source can create items")
	}
	if id := cmp.Or(explicit, token, cfg.Defaults.Source); id != "" {
		name := "source " + id
		if explicit == "" && token == "" {
			name = "defaults.source " + id
		}
		for _, c := range cfg.Sources {
			if c.ID == id || cfg.Workspace != nil && c.ID == url.QueryEscape(cfg.Project.ID)+"/"+id {
				if granted := caps(c.ID); granted == nil || granted.Allows(model.Create) {
					return c.ID, nil
				}
				return "", refuse(name + " cannot create items")
			}
		}
		return "", refuse("unknown " + name)
	}
	ids := creatable()
	if len(ids) == 1 {
		return ids[0], nil
	}
	if len(ids) == 0 {
		return "", source.Refuse("usage", "no source can create items")
	}
	return "", source.Refuse("usage", "several sources can create items; name one or set defaults.source: "+strings.Join(ids, ", "))
}

// ApplyCreateDefaults fills defaults.type and defaults.dod into a new item
// for the sources that store them: native and Backlog.md. Linear has neither,
// so its creates omit them, while linearCreate still refuses explicit values.
func ApplyCreateDefaults(cfg project.Config, sourceID string, item *model.Item) {
	p, local := cfg.SourceProject(sourceID)
	for _, c := range p.Sources {
		if c.ID == local && (c.Type == "native" || c.Type == "backlog-md") {
			if item.Type == "" {
				item.Type = p.Defaults.Type
			}
			for _, d := range p.Defaults.DOD {
				item.DoD = append(item.DoD, model.Criterion{Text: d})
			}
		}
	}
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
