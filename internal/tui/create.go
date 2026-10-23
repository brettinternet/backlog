package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/query"
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
		return checklistValue(item.Criteria)
	case "dod":
		return checklistValue(item.DoD)
	}
	return ""
}

// checklistValue writes criteria as task-list lines joined by a literal \n,
// the form's line break, with evidence on an indented line as in item files.
func checklistValue(criteria []model.Criterion) string {
	lines := make([]string, 0, len(criteria))
	for _, c := range criteria {
		mark := " "
		if c.Checked {
			mark = "x"
		}
		lines = append(lines, "- ["+mark+"] "+c.Text)
		if c.Evidence != "" {
			lines = append(lines, "  Evidence: "+c.Evidence)
		}
	}
	return strings.Join(lines, `\n`)
}

var checklistLine = regexp.MustCompile(`^[-*] \[([ xX])\] ?(.*)$`)

// parseChecklist reads checklistValue's form. A line without a checkbox is
// an unchecked entry, so plain lines need no markup.
func parseChecklist(value string) []model.Criterion {
	var criteria []model.Criterion
	for _, line := range strings.Split(value, `\n`) {
		trimmed := strings.TrimSpace(line)
		if evidence, ok := strings.CutPrefix(trimmed, "Evidence:"); ok && len(criteria) > 0 && trimmed != line {
			criteria[len(criteria)-1].Evidence = strings.TrimSpace(evidence)
			continue
		}
		if trimmed == "" {
			continue
		}
		if m := checklistLine.FindStringSubmatch(trimmed); m != nil {
			criteria = append(criteria, model.Criterion{Text: strings.TrimSpace(m[2]), Checked: m[1] != " "})
			continue
		}
		criteria = append(criteria, model.Criterion{Text: trimmed})
	}
	return criteria
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
			item.Criteria = parseChecklist(v)
		case "dod":
			item.DoD = parseChecklist(v)
		}
	}
	if item.Title == "" {
		return item, fmt.Errorf("title is required")
	}
	return item, nil
}

// expandCapture moves quick-add into the full form. Tab may open the form
// before a title is typed (untitled); its title field takes the title there.
func (m *Model) expandCapture(untitled bool) {
	item, err := query.QuickAdd(m.capture.line, m.opts.Me)
	if err != nil && untitled {
		// Only a missing title fails if a placeholder title alone parses.
		if parsed, retry := query.QuickAdd("x "+m.capture.line, m.opts.Me); retry == nil && parsed.Title == "x" {
			item, err = parsed, nil
			item.Title = ""
		}
	}
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
			m.expandCapture(true)
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
			m.expandCapture(false)
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
