package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/textutil"
)

type captureState struct {
	open, full, child, choosing bool
	line, errorText             string
	sources                     []string
	fields                      []captureField
	cursor                      int
	original                    model.Item
	version                     string
	source                      string
}
type captureField struct{ name, value string }

var captureNames = []string{"title", "status", "type", "priority", "rank", "assignees", "labels", "milestone", "due", "parent", "blocked_by", "related", "duplicate_of", "refs", "files", "description", "criteria", "dod", "plan", "notes", "summary"}

func (m *Model) beginCapture(child bool) {
	if m.opts.Store == nil {
		m.notice = "Editing is unavailable without an engine store."
		return
	}
	source := m.opts.CreateSource
	if child {
		row, ok := m.selectedRow()
		if !ok {
			m.notice = "Select a parent first."
			return
		}
		source = row.Ref.Source
	}
	if !child && source == "" {
		capable := []string{}
		for _, id := range m.opts.Sources {
			if m.hasWorkspace() && !m.opts.WorkspaceScope {
				p, _ := m.opts.Workspace.SourceProject(id)
				if p.Project.ID != m.scopeProject() {
					continue
				}
			}
			if m.opts.Store.Capabilities(id).Allows(model.Create) {
				capable = append(capable, id)
			}
		}
		if len(capable) == 0 {
			m.notice = "Disabled: no source supports creation."
			return
		}
		if len(capable) == 1 {
			source = capable[0]
		} else {
			m.capture = captureState{open: true, choosing: true, sources: capable}
			m.notice = ""
			return
		}
	}
	if source == "" || !m.opts.Store.Capabilities(source).Allows(model.Create) {
		m.notice = "Disabled: source does not support creation."
		return
	}
	m.routeSourceProject(source)
	m.capture = captureState{open: true, child: child, source: source}
	m.notice = ""
}
func (m *Model) beginFullEdit() {
	row, ok := m.selectedRow()
	if !ok {
		m.notice = "Select an item before editing."
		return
	}
	if m.opts.Store == nil {
		m.notice = "Editing is unavailable without an engine store."
		return
	}
	item, _, ok := m.currentSnapshot().Get(row.Ref)
	if !ok {
		m.notice = "Item is no longer available."
		return
	}
	m.capture = captureState{open: true, full: true, original: item, version: row.Version, source: row.Ref.Source}
	m.capture.loadFields(item, m.isBacklogMD(m.capture.source))
}
func (c *captureState) loadFields(item model.Item, backlogMD bool) {
	c.fields = make([]captureField, 0, len(captureNames))
	for _, name := range captureNames {
		if backlogMD && name == "rank" {
			continue
		}
		c.fields = append(c.fields, captureField{name, captureValue(item, name)})
	}
}
func captureValue(item model.Item, name string) string {
	switch name {
	case "title":
		return item.Title
	case "status":
		return item.Status
	case "type":
		return item.Type
	case "priority":
		return string(item.Priority)
	case "rank":
		return item.Rank
	case "milestone":
		return item.Milestone
	case "due":
		return item.Due
	case "parent":
		if item.Parent != nil {
			return qualifiedRef(*item.Parent, item.Ref.Source)
		}
	case "duplicate_of":
		if item.DuplicateOf != nil {
			return qualifiedRef(*item.DuplicateOf, item.Ref.Source)
		}
	case "assignees":
		return strings.Join(item.Assignees, ", ")
	case "labels":
		return strings.Join(item.Labels, ", ")
	case "blocked_by":
		return refValues(item.BlockedBy, item.Ref.Source)
	case "related":
		return refValues(item.Related, item.Ref.Source)
	case "refs":
		return strings.Join(item.Refs, ", ")
	case "files":
		return strings.Join(item.Files, ", ")
	case "description":
		return strings.ReplaceAll(item.Description, "\n", `\n`)
	case "plan":
		return strings.ReplaceAll(item.Plan, "\n", `\n`)
	case "notes":
		return strings.ReplaceAll(item.Notes, "\n", `\n`)
	case "summary":
		return strings.ReplaceAll(item.Summary, "\n", `\n`)
	case "criteria":
		b, _ := json.Marshal(item.Criteria)
		return string(b)
	case "dod":
		b, _ := json.Marshal(item.DoD)
		return string(b)
	}
	return ""
}
func refValues(refs []model.Ref, source string) string {
	values := make([]string, len(refs))
	for i, r := range refs {
		values[i] = qualifiedRef(r, source)
	}
	return strings.Join(values, ", ")
}
func splitValues(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
func parseRef(value, source string) model.Ref {
	if s, id, ok := strings.Cut(value, ":"); ok {
		return model.Ref{Source: s, ID: id}
	}
	return model.Ref{ID: value}
}
func captureItem(fields []captureField, source string) (model.Item, error) {
	item := model.Item{Ref: model.Ref{Source: source}}
	for _, f := range fields {
		v := strings.TrimSpace(f.value)
		switch f.name {
		case "title":
			item.Title = v
		case "status":
			item.Status = v
		case "type":
			item.Type = v
		case "priority":
			item.Priority = model.Priority(v)
		case "rank":
			item.Rank = v
		case "milestone":
			item.Milestone = v
		case "due":
			item.Due = v
		case "assignees":
			item.Assignees = splitValues(v)
		case "labels":
			item.Labels = splitValues(v)
		case "refs":
			item.Refs = splitValues(v)
		case "files":
			item.Files = splitValues(v)
		case "parent":
			if v != "" {
				r := parseRef(v, source)
				item.Parent = &r
			}
		case "duplicate_of":
			if v != "" {
				r := parseRef(v, source)
				item.DuplicateOf = &r
			}
		case "blocked_by":
			for _, s := range splitValues(v) {
				item.BlockedBy = append(item.BlockedBy, parseRef(s, source))
			}
		case "related":
			for _, s := range splitValues(v) {
				item.Related = append(item.Related, parseRef(s, source))
			}
		case "description":
			item.Description = strings.ReplaceAll(f.value, `\n`, "\n")
		case "plan":
			item.Plan = strings.ReplaceAll(f.value, `\n`, "\n")
		case "notes":
			item.Notes = strings.ReplaceAll(f.value, `\n`, "\n")
		case "summary":
			item.Summary = strings.ReplaceAll(f.value, `\n`, "\n")
		case "criteria":
			if v != "" {
				if err := json.Unmarshal([]byte(v), &item.Criteria); err != nil {
					return item, fmt.Errorf("criteria: %w", err)
				}
			}
		case "dod":
			if v != "" {
				if err := json.Unmarshal([]byte(v), &item.DoD); err != nil {
					return item, fmt.Errorf("dod: %w", err)
				}
			}
		}
	}
	if item.Title == "" {
		return item, fmt.Errorf("title is required")
	}
	return item, nil
}
func (m *Model) expandCapture() {
	item, err := query.QuickAdd(m.capture.line, m.opts.Me)
	if err != nil {
		m.capture.errorText = err.Error()
		return
	}
	if m.capture.child {
		row, _ := m.selectedRow()
		item.Parent = &model.Ref{ID: row.Ref.ID}
	}
	if item.Type == "" {
		item.Type = m.opts.DefaultType
	}
	if item.Status == "" {
		item.Status = m.opts.CreateStatus
		if m.isBoard() && m.boardStatusGroup() && len(m.data.starts) > 0 {
			item.Status = m.data.rows[m.data.starts[m.boardColumn()]].value
		}
		if item.Status == "" {
			for _, s := range m.opts.Statuses {
				if s.Category == "todo" {
					item.Status = s.Key
					break
				}
			}
			if item.Status == "" && len(m.opts.Statuses) > 0 {
				item.Status = m.opts.Statuses[0].Key
			}
		}
	}
	for _, s := range m.opts.DefaultDoD {
		item.DoD = append(item.DoD, model.Criterion{Text: s})
	}
	m.capture.loadFields(item, m.isBacklogMD(m.capture.source))
	m.capture.full = true
	m.capture.cursor = 0
	m.capture.errorText = ""
}
func (m *Model) updateCapture(key string) tea.Cmd {
	c := &m.capture
	if c.choosing {
		switch key {
		case "esc":
			m.capture = captureState{}
		case "down", "tab":
			c.cursor = (c.cursor + 1) % len(c.sources)
		case "up", "shift+tab":
			c.cursor = (c.cursor + len(c.sources) - 1) % len(c.sources)
		case "enter":
			c.source = c.sources[c.cursor]
			m.routeSourceProject(c.source)
			c.choosing = false
			c.cursor = 0
		}
		return nil
	}
	switch key {
	case "esc":
		m.capture = captureState{}
		return nil
	case "tab":
		if !c.full {
			m.expandCapture()
		} else {
			c.cursor = (c.cursor + 1) % len(c.fields)
		}
		return nil
	case "shift+tab":
		if c.full {
			c.cursor = (c.cursor + len(c.fields) - 1) % len(c.fields)
		}
		return nil
	case "up":
		if c.full {
			c.cursor = max(0, c.cursor-1)
		}
		return nil
	case "down":
		if c.full {
			c.cursor = min(len(c.fields)-1, c.cursor+1)
		}
		return nil
	case "enter", "ctrl+s":
		if !c.full {
			m.expandCapture()
			if c.errorText != "" {
				return nil
			}
		}
		item, err := captureItem(c.fields, c.source)
		if err == nil && m.opts.ValidateItem != nil {
			checked := item
			// ResolveRef runs in the command, not Update. Substitute only numeric
			// IDs during inline validation so unrelated malformed fields still
			// remain in the form; validate the actual resolved item before writing.
			if c.version == "" && m.opts.ResolveRef != nil {
				if item.Parent != nil {
					if _, numericErr := strconv.ParseUint(item.Parent.ID, 10, 64); numericErr == nil {
						parent := *item.Parent
						parent.ID = "BL-1"
						checked.Parent = &parent
					}
				}
				checked.BlockedBy = slices.Clone(item.BlockedBy)
				for n, ref := range checked.BlockedBy {
					if _, numericErr := strconv.ParseUint(ref.ID, 10, 64); numericErr == nil {
						checked.BlockedBy[n].ID = "BL-1"
					}
				}
			}
			err = m.opts.ValidateItem(checked)
		}
		if err != nil {
			c.errorText = boundedLine(err.Error())
			return nil
		}
		mutation := model.Mutation{Op: model.Create, Ref: model.Ref{Source: c.source}, Item: &item}
		var sequential []model.Mutation
		if c.version != "" {
			changes := []model.Mutation{}
			for _, f := range c.fields {
				if f.value == captureValue(c.original, f.name) {
					continue
				}
				if section, ok := map[string]string{"description": "Description", "criteria": "Acceptance criteria", "dod": "Definition of done", "plan": "Plan", "notes": "Notes", "summary": "Summary"}[f.name]; ok {
					text := strings.ReplaceAll(f.value, `\n`, "\n")
					if f.name == "criteria" || f.name == "dod" {
						var criteria []model.Criterion
						if f.name == "criteria" {
							criteria = item.Criteria
						} else {
							criteria = item.DoD
						}
						text = ""
						for _, entry := range criteria {
							mark := " "
							if entry.Checked {
								mark = "x"
							}
							text += "- [" + mark + "] " + entry.Text + "\n"
							if entry.Evidence != "" {
								text += "  Evidence: " + entry.Evidence + "\n"
							}
						}
					}
					changes = append(changes, model.Mutation{Op: model.ReplaceSection, Section: section, Text: text})
				} else {
					value := captureValue(item, f.name)
					var typed any = value
					switch f.name {
					case "assignees":
						typed = item.Assignees
					case "labels":
						typed = item.Labels
					case "blocked_by":
						typed = splitValues(refValues(item.BlockedBy, c.source))
					case "related":
						typed = splitValues(refValues(item.Related, c.source))
					case "refs":
						typed = item.Refs
					case "files":
						typed = item.Files
					case "parent":
						typed = captureValue(item, "parent")
					case "duplicate_of":
						typed = captureValue(item, "duplicate_of")
					}
					op := model.Set
					if value == "" {
						op = model.Unset
					}
					changes = append(changes, model.Mutation{Op: op, Field: f.name, Value: typed})
				}
			}
			if len(changes) == 0 {
				m.capture = captureState{}
				m.notice = "No changes."
				return nil
			}
			mutation = model.Mutation{Op: model.Batch, Ref: c.original.Ref, ExpectedVersion: c.version, Changes: changes}
		}
		caps := m.opts.Store.Capabilities(c.source)
		if mutation.Op == model.Batch && !caps.Allows(model.Batch) {
			for _, change := range mutation.Changes {
				if !caps.Allows(change.Op) {
					c.errorText = "source does not support " + string(change.Op)
					return nil
				}
			}
			sequential = mutation.Changes
			if len(sequential) == 1 {
				mutation = sequential[0]
				mutation.Ref = c.original.Ref
				mutation.ExpectedVersion = c.version
				sequential = nil
			}
		}
		if reason := mutationCapabilityReason(caps, mutation); reason != "" && len(sequential) == 0 {
			c.errorText = reason
			return nil
		}
		m.capture = captureState{}
		ctx, store, resolve, validate := m.opts.Context, m.opts.Store, m.opts.ResolveRef, m.opts.ValidateItem
		return func() tea.Msg {
			if mutation.Op == model.Create && resolve != nil {
				if item.Parent != nil {
					parent, err := resolve(ctx, mutation.Ref.Source, *item.Parent)
					if err != nil {
						return editResultMsg{message: boundedLine(err.Error())}
					}
					item.Parent = &parent
				}
				for n, ref := range item.BlockedBy {
					resolved, err := resolve(ctx, mutation.Ref.Source, ref)
					if err != nil {
						return editResultMsg{message: boundedLine(err.Error())}
					}
					item.BlockedBy[n] = resolved
				}
				mutation.Item = &item
				if validate != nil {
					if err := validate(item); err != nil {
						return editResultMsg{message: boundedLine(err.Error())}
					}
				}
			}
			writes := []model.Mutation{mutation}
			if len(sequential) > 0 {
				writes = sequential
			}
			version := mutation.ExpectedVersion
			for index, write := range writes {
				if len(sequential) > 0 {
					write.Ref = mutation.Ref
					write.ExpectedVersion = version
				}
				select {
				case err := <-store.Mutate(write):
					if err != nil {
						return editResultMsg{message: boundedLine(err.Error())}
					}
					if len(sequential) > 0 && index+1 < len(writes) {
						latest, _, ok := store.Current().Get(write.Ref)
						if !ok {
							return editResultMsg{message: "Item disappeared during edit."}
						}
						version = latest.Version
					}
				case <-ctx.Done():
					return editResultMsg{message: "Edit canceled."}
				}
			}
			return editResultMsg{message: "Item saved."}
		}
	case "backspace":
		if c.full {
			r := []rune(c.fields[c.cursor].value)
			if len(r) > 0 {
				c.fields[c.cursor].value = string(r[:len(r)-1])
			}
		} else {
			r := []rune(c.line)
			if len(r) > 0 {
				c.line = string(r[:len(r)-1])
			}
		}
		c.errorText = ""
		return nil
	case "ctrl+u":
		if c.full {
			c.fields[c.cursor].value = ""
		} else {
			c.line = ""
		}
		c.errorText = ""
		return nil
	}
	if key == "space" {
		key = " "
	}
	if len([]rune(key)) == 1 && !strings.HasPrefix(key, "ctrl+") {
		if c.full {
			c.fields[c.cursor].value += key
		} else {
			c.line += key
		}
		c.errorText = ""
	}
	return nil
}
func (m Model) captureFormView() tea.View {
	c := m.capture
	lines := []string{"backlog · " + textutil.Line(m.opts.Project)}
	if c.choosing {
		lines = append(lines, "Create source · up/down select · enter continue · esc cancel")
		for i, id := range c.sources {
			marker := "  "
			if i == c.cursor {
				marker = "> "
			}
			lines = append(lines, marker+textutil.Line(id))
		}
	} else if !c.full {
		lines = append(lines, "Quick-add · tab full form · enter create · esc cancel", "> "+textutil.Line(c.line))
		item, err := query.QuickAdd(c.line, m.opts.Me)
		if err != nil {
			item, err = query.QuickAdd("Captured "+c.line, m.opts.Me)
		}
		if err == nil {
			lines = append(lines, "chips: "+textutil.Line(fmt.Sprintf("priority %s · @%s · #%s · parent %s · blockers %s · ~%s · :%s", item.Priority, strings.Join(item.Assignees, ","), strings.Join(item.Labels, ","), captureValue(item, "parent"), refValues(item.BlockedBy, c.source), item.Milestone, item.Type)))
		}
	} else {
		label := "Create item"
		if c.version != "" {
			label = "Edit item"
		}
		lines = append(lines, label+" · tab/shift-tab fields · enter save · esc cancel")
		start := max(0, c.cursor-max(1, m.height-6))
		end := min(len(c.fields), start+max(1, m.height-5))
		for i := start; i < end; i++ {
			prefix := "  "
			if i == c.cursor {
				prefix = "> "
			}
			lines = append(lines, prefix+c.fields[i].name+": "+textutil.Line(c.fields[i].value))
		}
	}
	if c.errorText != "" {
		lines = append(lines, "! "+textutil.Line(c.errorText))
	}
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = cell(lines[i], m.width)
	}
	if len(lines) > m.height {
		lines = slices.Clone(lines[:m.height])
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}
