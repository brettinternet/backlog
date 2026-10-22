package tui

import (
	"fmt"
	"slices"
	"sort"
	"strconv"

	"github.com/brettinternet/backlog/internal/model"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// action is the shared catalog for dispatch, help, which-key and the palette.
// A binding is a space-separated sequence of Bubble Tea key names.
type action struct {
	id, label, canonical, unavailable string
	bindings                          []string
}

func defaultKeymap() []action {
	actions := []action{
		{id: "move-down", label: "Move down / scroll detail", canonical: "j", bindings: []string{"j", "down"}},
		{id: "move-up", label: "Move up / scroll detail", canonical: "k", bindings: []string{"k", "up"}},
		{id: "first", label: "First row", canonical: "g g", bindings: []string{"g g"}},
		{id: "last", label: "Last row / count G to row N", canonical: "G", bindings: []string{"G"}},
		{id: "half-down", label: "Half page down", canonical: "ctrl+d", bindings: []string{"ctrl+d", "pagedown"}},
		{id: "half-up", label: "Half page up", canonical: "ctrl+u", bindings: []string{"ctrl+u", "pageup"}},
		{id: "page-down", label: "Full page down", canonical: "ctrl+f", bindings: []string{"ctrl+f"}},
		{id: "page-up", label: "Full page up", canonical: "ctrl+b", bindings: []string{"ctrl+b"}},
		{id: "previous-group", label: "Previous group / board column", canonical: "[", bindings: []string{"["}},
		{id: "next-group", label: "Next group / board column", canonical: "]", bindings: []string{"]"}},
		{id: "left", label: "Tree parent / board left / focus list", canonical: "h", bindings: []string{"h", "left"}},
		{id: "right", label: "Tree child / board right / labels in list", canonical: "l", bindings: []string{"l", "right"}},
		{id: "detail", label: "Toggle detail", canonical: "enter", bindings: []string{"enter"}},
		{id: "next-detail-tab", label: "Focus detail / next detail tab", canonical: "tab", bindings: []string{"tab"}},
		{id: "previous-detail-tab", label: "Focus detail / previous detail tab", canonical: "shift+tab", bindings: []string{"shift+tab"}},
		{id: "cancel", label: "Close detail / clear marks / cancel", canonical: "esc", bindings: []string{"esc"}},
		{id: "next-view", label: "Next view", canonical: "g t", bindings: []string{"g t"}},
		{id: "previous-view", label: "Previous view", canonical: "g T", bindings: []string{"g T"}},
		{id: "go-to", label: "Go to any item (including archives)", bindings: []string{"ctrl+p"}},
		{id: "help", label: "Search help", bindings: []string{"?"}},
		{id: "palette", label: "Command palette", bindings: []string{":"}},
		{id: "workspace", label: "Toggle project / workspace scope", bindings: []string{"W"}, unavailable: "workspace support is not available in this build"},
		{id: "refresh", label: "Resync sources", canonical: "R", bindings: []string{"R"}},
		{id: "quit", label: "Back from detail / quit", canonical: "q", bindings: []string{"q"}},
		{id: "quit-now", label: "Quit", canonical: "ctrl+c", bindings: []string{"ctrl+c"}},
		{id: "query", label: "Edit query", canonical: "/", bindings: []string{"/"}},
		{id: "clear-filter", label: "Clear filter", canonical: "F", bindings: []string{"F"}},
		{id: "save-view", label: "Save view", canonical: "ctrl+s", bindings: []string{"ctrl+s"}},
		{id: "status", label: "Status picker", canonical: "s", bindings: []string{"s"}},
		{id: "previous-status", label: "Previous workflow status", canonical: "<", bindings: []string{"<"}},
		{id: "next-status", label: "Next workflow status", canonical: ">", bindings: []string{">"}},
		{id: "priority", label: "Priority picker", canonical: "p", bindings: []string{"p"}},
		{id: "assignees", label: "Assignee picker", canonical: "a", bindings: []string{"a"}},
		{id: "assign-me", label: "Toggle assigning yourself", canonical: "A", bindings: []string{"A"}},
		{id: "milestone", label: "Milestone picker / expand detail body", canonical: "m", bindings: []string{"m"}},
		{id: "blocker", label: "Blocker editor", canonical: "b", bindings: []string{"b"}},
		{id: "parent", label: "Parent picker", canonical: "P", bindings: []string{"P"}},
		{id: "title", label: "Edit title", canonical: "t", bindings: []string{"t"}},
		{id: "description", label: "Edit description in editor", canonical: "e", bindings: []string{"e"}},
		{id: "edit", label: "Edit whole item", canonical: "E", bindings: []string{"E"}},
		{id: "comment", label: "Add comment", canonical: "c", bindings: []string{"c"}},
		{id: "note", label: "Append note", canonical: "i", bindings: []string{"i"}},
		{id: "criterion", label: "Toggle criterion (Criteria tab)", canonical: "x", bindings: []string{"x"}},
		{id: "start", label: "Start item", canonical: "S", bindings: []string{"S"}},
		{id: "done", label: "Finish item", canonical: "D", bindings: []string{"D"}},
		{id: "claim", label: "Claim / release", bindings: []string{"C"}, unavailable: "coordination support awaits BL-32"},
		{id: "archive", label: "Archive (confirm)", canonical: "d d", bindings: []string{"d d"}},
		{id: "repeat", label: "Repeat last edit", canonical: ".", bindings: []string{"."}},
		{id: "undo", label: "Undo", canonical: "u", bindings: []string{"u"}},
		{id: "redo", label: "Redo", canonical: "ctrl+r", bindings: []string{"ctrl+r"}},
		{id: "new", label: "Quick-add", canonical: "n", bindings: []string{"n"}},
		{id: "new-child", label: "Quick-add child", canonical: "N", bindings: []string{"N"}},
		{id: "actions", label: "Actions menu", bindings: []string{"!"}, unavailable: "configured actions await BL-30"},
		{id: "yank-id", label: "Copy ID", canonical: "y y", bindings: []string{"y y"}},
		{id: "yank-title", label: "Copy title", canonical: "y t", bindings: []string{"y t"}},
		{id: "yank-branch", label: "Copy branch name", canonical: "y b", bindings: []string{"y b"}},
		{id: "yank-url", label: "Copy URL", canonical: "y u", bindings: []string{"y u"}},
		{id: "yank-markdown", label: "Copy Markdown link", canonical: "y m", bindings: []string{"y m"}},
		{id: "open-url", label: "Open safe provider URL", canonical: "O", bindings: []string{"O"}},
		{id: "mark", label: "Mark item / toggle criterion", canonical: "space", bindings: []string{"space"}},
		{id: "visual", label: "Visual range", canonical: "V", bindings: []string{"V"}},
		{id: "mark-all", label: "Mark current view", canonical: "ctrl+a", bindings: []string{"ctrl+a"}},
		{id: "split-left", label: "Shrink list pane", canonical: "ctrl+left", bindings: []string{"ctrl+left"}},
		{id: "split-right", label: "Grow list pane", canonical: "ctrl+right", bindings: []string{"ctrl+right"}},
	}
	for _, prefix := range []string{"f", "o", "z"} {
		for _, b := range queryMenus[prefix] {
			id := map[string]string{"f": "filter", "o": "sort", "z": "group"}[prefix] + "-" + b.value
			if b.value == "" {
				id = "filter-clear"
			}
			sequence := prefix + " " + b.key
			actions = append(actions, action{id: id, label: map[string]string{"f": "Filter: ", "o": "Sort: ", "z": "Group: "}[prefix] + b.label, canonical: sequence, bindings: []string{sequence}})
		}
	}
	for i := 1; i <= 9; i++ {
		key := fmt.Sprint(i)
		actions = append(actions, action{id: "view-" + key, label: "Jump to view " + key, canonical: key, bindings: []string{key}})
	}
	return actions
}

// ValidateKeys is called before starting the program, so conflicting or unknown
// bindings fail startup rather than silently shadowing another action.
func ValidateKeys(overrides map[string]string) error { _, err := buildKeymap(overrides); return err }
func buildKeymap(overrides map[string]string) ([]action, error) {
	actions := defaultKeymap()
	known := map[string]bool{}
	for i := range actions {
		a := &actions[i]
		known[a.id] = true
		if binding, ok := overrides[a.id]; ok {
			a.bindings = nil
			if binding != "" {
				a.bindings = []string{binding}
			}
		}
	}
	var ids []string
	for id := range overrides {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !known[id] {
			return nil, fmt.Errorf("keys: unknown action %q", id)
		}
	}
	type bound struct{ key, id string }
	var bindings []bound
	for _, a := range actions {
		for _, key := range a.bindings {
			if strings.Join(strings.Fields(key), " ") != key {
				return nil, fmt.Errorf("keys: invalid sequence %q for %s", key, a.id)
			}
			for _, token := range strings.Fields(key) {
				if !validKey(token) {
					return nil, fmt.Errorf("keys: invalid key %q for %s", token, a.id)
				}
			}
			for _, b := range bindings {
				if key == b.key || strings.HasPrefix(key, b.key+" ") || strings.HasPrefix(b.key, key+" ") {
					return nil, fmt.Errorf("keys: conflicting bindings %q (%s) and %q (%s)", key, a.id, b.key, b.id)
				}
			}
			bindings = append(bindings, bound{key, a.id})
		}
	}
	return actions, nil
}
func validKey(key string) bool {
	if len([]rune(key)) == 1 {
		return !strings.ContainsAny(key, "\x00\x1b\r\n\t ")
	}
	if slices.Contains([]string{"enter", "esc", "space", "tab", "shift+tab", "up", "down", "left", "right", "home", "end", "pageup", "pagedown", "backspace", "delete"}, key) {
		return true
	}
	if strings.HasPrefix(key, "ctrl+") {
		rest := strings.TrimPrefix(key, "ctrl+")
		return len(rest) == 1 && rest[0] >= 'a' && rest[0] <= 'z' || slices.Contains([]string{"left", "right", "up", "down"}, rest)
	}
	return false
}

func (m Model) dispatchKey(key string) (tea.Model, tea.Cmd) {
	if m.keyError != nil {
		m.notice = m.keyError.Error()
		return m, nil
	}
	if key == " " {
		key = "space"
	}
	if m.keyPrefix != "" && key == "esc" {
		m.keyPrefix = ""
		m.count = 0
		m.inputSerial++
		return m, nil
	}
	sequence := key
	if m.keyPrefix != "" {
		sequence = m.keyPrefix + " " + key
	}
	for _, a := range m.keymap {
		if slices.Contains(a.bindings, sequence) {
			m.keyPrefix = ""
			return m.executeAction(a)
		}
	}
	for _, a := range m.keymap {
		for _, binding := range a.bindings {
			if strings.HasPrefix(binding, sequence+" ") {
				m.keyPrefix = sequence
				m.inputSerial++
				return m, nil
			}
		}
	}
	m.keyPrefix = ""
	// Digits that aren't direct view shortcuts still extend a motion count.
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		m.count = min(1000000, m.count*10+int(key[0]-'0'))
		m.inputSerial++
		return m, nil
	}
	return m, nil
}
func (m Model) executeAction(a action) (tea.Model, tea.Cmd) {
	if a.unavailable != "" {
		m.notice = "Disabled: " + a.unavailable
		return m, nil
	}
	if a.id == "go-to" && m.goToWorker == nil {
		m.notice = "Disabled: go-to search is unavailable"
		return m, nil
	}
	switch a.id {
	case "go-to":
		return m.openGoTo()
	case "help", "palette":
		return m.openActions(a.id == "help")
	case "quit-now":
		return m, tea.Quit
	}
	var commands []tea.Cmd
	for _, key := range strings.Fields(a.canonical) {
		next, cmd := m.canonicalKey(key)
		m = next.(Model)
		commands = append(commands, cmd)
	}
	return m, tea.Batch(commands...)
}
func (m Model) disabledReason(a action) string {
	if a.unavailable != "" {
		return a.unavailable
	}
	if a.id == "go-to" && m.goToWorker == nil {
		return "go-to search is unavailable"
	}
	if a.id == "save-view" && m.opts.SaveView == nil {
		return "view saving is unavailable"
	}
	if a.id == "refresh" && m.opts.Store == nil {
		return "source refresh is unavailable"
	}
	if (a.id == "split-left" || a.id == "split-right") && (!m.detailOpen || m.width < 120) {
		return "open split detail first"
	}
	if strings.HasPrefix(a.id, "view-") {
		n, _ := strconv.Atoi(strings.TrimPrefix(a.id, "view-"))
		if n > len(m.views) {
			return "view is not configured"
		}
	}
	if a.id == "assign-me" && m.opts.Me == "" {
		return "no current user is configured"
	}
	if a.id == "repeat" && m.lastEdit == nil {
		return "no edit to repeat"
	}
	if (a.id == "undo" || a.id == "redo") && m.opts.Store == nil {
		return "session history is unavailable"
	}
	if a.id == "criterion" && (!m.detailFocus || m.detailTab != tabCriteria) {
		return "open the Criteria tab"
	}
	if m.detailFocus {
		if slices.Contains([]string{"move-down", "move-up", "half-down", "half-up", "page-down", "page-up", "left", "detail", "next-detail-tab", "previous-detail-tab", "cancel", "next-view", "previous-view", "go-to", "help", "palette", "quit", "quit-now", "save-view", "split-left", "split-right"}, a.id) {
			return ""
		}
		if a.id == "milestone" {
			if !m.detailTruncated && !m.detailExpanded {
				return "body is not truncated"
			}
			return ""
		}
		if (a.id == "criterion" || a.id == "mark") && m.detailTab == tabCriteria {
			row, ok := m.selectedRow()
			if !ok {
				return "select an item"
			}
			return m.capability(row.Ref, model.CheckCriterion, model.UncheckCriterion)
		}
		return "focus the list first"
	}
	kind := map[string]pickerKind{"status": pickerStatus, "priority": pickerPriority, "assignees": pickerAssignees, "assign-me": pickerAssignees, "milestone": pickerMilestone, "blocker": pickerBlocker, "parent": pickerParent, "title": pickerTitle}
	if a.id == "right" && !m.isBoard() && !m.isTree() {
		kind[a.id] = pickerLabels
	}
	if picker, ok := kind[a.id]; ok {
		row, found := m.selectedRow()
		if !found {
			return "select an item"
		}
		return m.pickerCapability(row.Ref, picker)
	}
	if a.id == "new" || a.id == "new-child" {
		if m.opts.Store == nil {
			return "creation is unavailable without an engine store"
		}
		if a.id == "new-child" {
			row, ok := m.selectedRow()
			if !ok {
				return "select a parent"
			}
			return m.capability(row.Ref, model.Create)
		}
		if m.opts.CreateSource != "" {
			return m.capability(model.Ref{Source: m.opts.CreateSource}, model.Create)
		}
		for _, id := range m.opts.Sources {
			if m.opts.Store.Capabilities(id).Allows(model.Create) {
				return ""
			}
		}
		return "no source supports creation"
	}
	ops := map[string]model.Operation{"description": model.ReplaceSection, "comment": model.AppendComment, "note": model.AppendNote, "archive": model.Archive, "previous-status": model.Set, "next-status": model.Set, "start": model.Set, "done": model.Set}
	if op, ok := ops[a.id]; ok {
		row, found := m.selectedRow()
		if !found {
			return "select an item"
		}
		if reason := m.capability(row.Ref, op); reason != "" {
			return reason
		}
		if a.id == "comment" && m.opts.CommentAuthor == "" {
			return "comment author is not configured"
		}
	}
	if a.id == "edit" {
		if _, ok := m.selectedRow(); !ok {
			return "select an item"
		}
		if m.opts.Store == nil {
			return "editing is unavailable without an engine store"
		}
	}
	if strings.HasPrefix(a.id, "yank-") || a.id == "open-url" {
		row, ok := m.selectedRow()
		if !ok {
			return "select an item"
		}
		if a.id == "open-url" {
			if _, err := safeURL(row.URL); err != nil {
				return err.Error()
			}
		}
		if (a.id == "yank-url" || a.id == "yank-markdown") && row.URL == "" {
			return "selected item has no URL"
		}
	}
	return ""
}

func (m Model) prefixLines() []string {
	if m.keyPrefix == "" {
		return nil
	}
	lines := []string{m.keyPrefix + " · esc cancel"}
	for _, a := range m.keymap {
		for _, binding := range a.bindings {
			if suffix, ok := strings.CutPrefix(binding, m.keyPrefix+" "); ok {
				lines = append(lines, suffix+"  "+a.label)
			}
		}
	}
	return lines
}
