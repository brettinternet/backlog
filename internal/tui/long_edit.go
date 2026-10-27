package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
)

type longEditState struct {
	ref                            model.Ref
	version, section, choice, path string
	original                       []byte
	appendOp                       model.Operation
}
type editPreparedMsg struct {
	state longEditState
	err   error
}
type editFinishedMsg struct{ err error }
type editSavedMsg struct {
	err               error
	conflict, invalid bool
}

func (m *Model) beginLongEdit(section string) tea.Cmd {
	row, ok := m.selectedRow()
	if !ok {
		m.notice = "Select an item before editing."
		return nil
	}
	if m.opts.Store == nil {
		m.notice = "Editing is unavailable without an engine store."
		return nil
	}
	op := model.Restore
	if section != "" {
		op = model.ReplaceSection
	}
	if reason := m.capability(row.Ref, op); reason != "" {
		m.notice = "Disabled: " + reason
		return nil
	}
	item, _, ok := m.currentSnapshot().Get(row.Ref)
	if !ok {
		m.notice = "Item is no longer available."
		return nil
	}
	state := longEditState{ref: row.Ref, version: row.Version, section: section}
	return m.prepareLongEdit(state, item)
}

// beginNotes opens Notes with their current text, or an empty note where the
// source can only append.
func (m *Model) beginNotes() tea.Cmd {
	if row, ok := m.selectedRow(); ok && m.capability(row.Ref, model.ReplaceSection, model.AppendNote) == "" {
		return m.beginLongEdit("Notes")
	}
	return m.beginTextAppend(model.AppendNote)
}
func (m *Model) beginTextAppend(op model.Operation) tea.Cmd {
	row, ok := m.selectedRow()
	if !ok {
		m.notice = "Select an item before editing."
		return nil
	}
	if reason := m.capability(row.Ref, op); reason != "" {
		m.notice = "Disabled: " + reason
		return nil
	}
	if op == model.AppendComment && m.opts.Me[row.Ref.Source] == "" {
		m.notice = "Disabled: comment author is not configured."
		return nil
	}
	item, _, ok := m.currentSnapshot().Get(row.Ref)
	if !ok {
		m.notice = "Item is no longer available."
		return nil
	}
	state := longEditState{ref: row.Ref, version: row.Version, appendOp: op}
	return m.prepareLongEdit(state, item)
}
func (m Model) prepareLongEdit(state longEditState, item model.Item) tea.Cmd {
	native, read, ctx := m.opts.NativeBytes, m.opts.Read, m.opts.Context
	if m.opts.NativeSource == nil || !m.opts.NativeSource(item.Ref.Source) {
		native = nil
	}
	return func() tea.Msg {
		var content []byte
		var err error
		if state.appendOp == "" && state.section == "" {
			if native == nil {
				err = fmt.Errorf("native file reading unavailable")
			} else {
				content, err = native(ctx, item)
			}
			if err == nil {
				var doc *format.Document
				doc, err = format.Parse(content)
				if err == nil && doc.Version() != state.version {
					err = fmt.Errorf("item changed before editor opened")
				}
			}
		} else if state.appendOp == "" {
			if native != nil {
				var data []byte
				data, err = native(ctx, item)
				if err == nil {
					var doc *format.Document
					doc, err = format.Parse(data)
					if err == nil {
						if doc.Version() != state.version {
							err = fmt.Errorf("item changed before editor opened")
						} else {
							content = []byte(doc.SectionText(state.section) + "\n")
						}
					}
				}
			}
			if native == nil && read != nil {
				var latest model.Item
				latest, err = read(ctx, item.Ref)
				if err == nil {
					if latest.Version != state.version {
						err = fmt.Errorf("item changed before editor opened")
					} else if state.section == "Notes" {
						content = []byte(latest.Notes + "\n")
					} else {
						content = []byte(latest.Description + "\n")
					}
				}
			}
			if native == nil && read == nil {
				err = fmt.Errorf("item reading unavailable")
			}
		}
		if err != nil {
			return editPreparedMsg{err: err}
		}
		file, err := os.CreateTemp("", "bl-tui-edit-*.md")
		if err != nil {
			return editPreparedMsg{err: err}
		}
		state.path = file.Name()
		state.original = content
		if _, err = file.Write(content); err != nil {
			file.Close()
			return editPreparedMsg{err: err}
		}
		if err = file.Close(); err != nil {
			return editPreparedMsg{err: err}
		}
		return editPreparedMsg{state: state}
	}
}
func (m Model) editorProcess(path string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.CommandContext(m.opts.Context, "/bin/sh", "-c", editor+` "$1"`, "bl-tui-edit", path)
	cmd.Dir = m.opts.EditorDir
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return editFinishedMsg{err: err} })
}
func (m Model) editorPrepared(msg editPreparedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.notice = "Editor: " + boundedLine(msg.err.Error())
		return m, nil
	}
	m.longEdit = msg.state
	return m, m.editorProcess(msg.state.path)
}
func (m Model) editorFinished(msg editFinishedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.notice = "Editor failed; buffer retained at " + m.longEdit.path + ": " + boundedLine(msg.err.Error())
		m.longEdit = longEditState{}
		return m, nil
	}
	state := m.longEdit
	validate := m.opts.ValidateItem
	store, ctx := m.opts.Store, m.opts.Context
	author := m.opts.Me[state.ref.Source]
	return m, func() tea.Msg {
		yours, err := os.ReadFile(state.path)
		if err != nil {
			return editSavedMsg{err: err}
		}
		// After "reopen with both" the buffer must be resolved before any write;
		// otherwise markers would land in the item or quitting would drop yours.
		if hasConflictMarkers(yours) {
			return editSavedMsg{err: fmt.Errorf("unresolved conflict markers"), invalid: true}
		}
		if bytes.Equal(yours, state.original) {
			return editSavedMsg{}
		}
		if state.section == "" && state.appendOp == "" {
			var doc *format.Document
			doc, err = format.Parse(yours)
			if err == nil && (doc.Item().Ref.ID != state.ref.ID || validate != nil && validate(doc.Item()) != nil) {
				err = fmt.Errorf("invalid item or frontmatter")
			}
			if err != nil {
				return editSavedMsg{err: err, invalid: true}
			}
		}
		mutation := longMutation(state, yours, author)
		current, _, ok := store.Current().Get(state.ref)
		if !ok {
			return editSavedMsg{err: fmt.Errorf("item is no longer available")}
		}
		if current.Version != state.version {
			return editSavedMsg{conflict: true}
		}
		select {
		case err = <-store.Mutate(mutation):
			if err != nil {
				if strings.Contains(err.Error(), "conflict") {
					return editSavedMsg{conflict: true}
				}
				return editSavedMsg{err: err}
			}
			return editSavedMsg{}
		case <-ctx.Done():
			return editSavedMsg{err: ctx.Err()}
		}
	}
}
func hasConflictMarkers(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "<<<<<<< yours" || line == ">>>>>>> theirs" {
			return true
		}
	}
	return false
}
func longMutation(state longEditState, yours []byte, author string) model.Mutation {
	mutation := model.Mutation{Ref: state.ref, ExpectedVersion: state.version}
	switch state.appendOp {
	case model.AppendNote:
		mutation.Op = model.AppendNote
		mutation.Text = string(yours)
	case model.AppendComment:
		mutation.Op = model.AppendComment
		mutation.Comment = model.Comment{Author: author, Body: string(yours), Time: time.Now()}
	default:
		if state.section != "" {
			mutation.Op = model.ReplaceSection
			mutation.Section = state.section
			mutation.Text = string(yours)
		} else {
			mutation.Op = model.Restore
			mutation.Original = yours
		}
	}
	return mutation
}
func (m Model) editorSaved(msg editSavedMsg) (tea.Model, tea.Cmd) {
	if msg.conflict {
		m.longEdit.choice = "conflict"
		m.notice = ""
		return m, nil
	}
	if msg.err != nil {
		if msg.invalid {
			m.longEdit.choice = "invalid"
			m.notice = boundedLine(msg.err.Error())
			return m, nil
		}
		m.notice = "Editor save failed; buffer retained at " + m.longEdit.path + ": " + boundedLine(msg.err.Error())
		m.longEdit = longEditState{}
		return m, nil
	}
	path := m.longEdit.path
	m.longEdit = longEditState{}
	m.notice = "Editor saved."
	return m, func() tea.Msg {
		if path != "" {
			_ = os.Remove(path)
		}
		return nil
	}
}
func (m *Model) updateLongEdit(key string) tea.Cmd {
	state := m.longEdit
	switch state.choice {
	case "invalid":
		switch key {
		case "r":
			m.longEdit.choice = ""
			return m.editorProcess(state.path)
		case "d", "esc":
			m.longEdit = longEditState{}
			m.notice = "Invalid editor changes discarded."
			return func() tea.Msg { _ = os.Remove(state.path); return nil }
		}
	case "conflict":
		switch key {
		case "t", "esc":
			m.longEdit = longEditState{}
			m.notice = "Kept source changes."
			return func() tea.Msg { _ = os.Remove(state.path); return nil }
		case "y":
			m.longEdit.choice = ""
			store, ctx, author := m.opts.Store, m.opts.Context, m.opts.Me[state.ref.Source]
			return func() tea.Msg {
				yours, err := os.ReadFile(state.path)
				if err != nil {
					return editSavedMsg{err: err}
				}
				current, _, ok := store.Current().Get(state.ref)
				if !ok {
					return editSavedMsg{err: fmt.Errorf("item is no longer available")}
				}
				state.version = current.Version // a fresh, checked version; the engine and source still enforce it
				mutation := longMutation(state, yours, author)
				select {
				case err = <-store.Mutate(mutation):
					if err != nil {
						return editSavedMsg{err: err, conflict: strings.Contains(err.Error(), "conflict")}
					}
					return editSavedMsg{}
				case <-ctx.Done():
					return editSavedMsg{err: ctx.Err()}
				}
			}
		case "r":
			m.longEdit.choice = ""
			native, read, store, ctx := m.opts.NativeBytes, m.opts.Read, m.opts.Store, m.opts.Context
			if m.opts.NativeSource == nil || !m.opts.NativeSource(state.ref.Source) {
				native = nil
			}
			return func() tea.Msg {
				yours, err := os.ReadFile(state.path)
				if err != nil {
					return editPreparedMsg{err: err}
				}
				current, _, ok := store.Current().Get(state.ref)
				if !ok {
					return editPreparedMsg{err: fmt.Errorf("item is no longer available")}
				}
				var theirs []byte
				if state.section == "" && state.appendOp == "" && native != nil {
					theirs, err = native(ctx, current)
				} else if state.section != "" && native != nil {
					var raw []byte
					raw, err = native(ctx, current)
					if err == nil {
						var doc *format.Document
						doc, err = format.Parse(raw)
						if err == nil {
							theirs = []byte(doc.SectionText(state.section))
						}
					}
				} else if read != nil {
					var item model.Item
					item, err = read(ctx, state.ref)
					if err == nil {
						theirs = []byte(item.Description)
					}
				}
				if err != nil {
					return editPreparedMsg{err: err}
				}
				// Conflict markers deliberately require the user to resolve both versions before saving.
				combined := []byte("<<<<<<< yours\n")
				combined = append(combined, yours...)
				combined = append(combined, []byte("\n=======\n")...)
				combined = append(combined, theirs...)
				combined = append(combined, []byte("\n>>>>>>> theirs\n")...)
				if err = os.WriteFile(state.path, combined, 0600); err != nil {
					return editPreparedMsg{err: err}
				}
				state.version = current.Version
				state.original = combined
				return editPreparedMsg{state: state}
			}
		}
	}
	return nil
}
