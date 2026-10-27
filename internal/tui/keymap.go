package tui

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"

	tea "charm.land/bubbletea/v2"
)

// action is the shared catalog for dispatch, help, which-key and the palette.
// A binding is a space-separated sequence of Bubble Tea key names.
type action struct {
	id, label, canonical, unavailable string
	group                             string // starts a help section
	bindings                          []string
}

func defaultKeymap() []action {
	// Order is the help and palette order; group starts a help section.
	actions := []action{
		// Emacs aliases (ctrl+n, alt+< and alt+>, ctrl+v and alt+v, ctrl+g, alt+x)
		// follow the Vim keys; ctrl+p already opens go-to.
		{group: "Navigate", id: "move-down", label: "Move down", canonical: "j", bindings: []string{"j", "down", "ctrl+n"}},
		{id: "move-up", label: "Move up", canonical: "k", bindings: []string{"k", "up"}},
		{id: "next-item", label: "Next item", canonical: "J", bindings: []string{"J"}},
		{id: "previous-item", label: "Previous item", canonical: "K", bindings: []string{"K"}},
		{id: "first", label: "First row", canonical: "g g", bindings: []string{"g g", "home", "alt+<"}},
		{id: "last", label: "Last row, or row N", canonical: "G", bindings: []string{"G", "end", "alt+>"}},
		{id: "screen-top", label: "Top of screen", canonical: "H", bindings: []string{"H"}},
		{id: "screen-middle", label: "Middle of screen", canonical: "M", bindings: []string{"M"}},
		{id: "screen-bottom", label: "Bottom of screen", canonical: "L", bindings: []string{"L"}},
		{id: "half-down", label: "Half page down", canonical: "ctrl+d", bindings: []string{"ctrl+d", "pagedown"}},
		{id: "half-up", label: "Half page up", canonical: "ctrl+u", bindings: []string{"ctrl+u", "pageup"}},
		{id: "page-down", label: "Page down", canonical: "ctrl+f", bindings: []string{"ctrl+f", "ctrl+v"}},
		{id: "page-up", label: "Page up", canonical: "ctrl+b", bindings: []string{"ctrl+b", "alt+v"}},
		{id: "previous-group", label: "Previous group", canonical: "[", bindings: []string{"["}},
		{id: "next-group", label: "Next group", canonical: "]", bindings: []string{"]"}},
		{id: "left", label: "Back / parent / left", canonical: "h", bindings: []string{"h", "left"}},
		{id: "right", label: "Open / child / right", canonical: "l", bindings: []string{"l", "right"}},
		{id: "go-to", label: "Go to item", bindings: []string{"ctrl+p"}},
		{group: "Detail", id: "detail", label: "Toggle detail", canonical: "enter", bindings: []string{"enter"}},
		{id: "next-detail-tab", label: "Next tab", canonical: "tab", bindings: []string{"tab"}},
		{id: "previous-detail-tab", label: "Previous tab", canonical: "shift+tab", bindings: []string{"shift+tab"}},
		{id: "back", label: "Back to previous item", canonical: "backspace", bindings: []string{"backspace"}},
		{id: "split-left", label: "Shrink list", canonical: "ctrl+left", bindings: []string{"ctrl+left"}},
		{id: "split-right", label: "Grow list", canonical: "ctrl+right", bindings: []string{"ctrl+right"}},
		{group: "Views", id: "next-view", label: "Next view", canonical: "g t", bindings: []string{"g t"}},
		{id: "previous-view", label: "Previous view", canonical: "g T", bindings: []string{"g T"}},
		{id: "board", label: "Board layout", canonical: "B", bindings: []string{"B"}},
		{id: "tree", label: "Tree layout", canonical: "T", bindings: []string{"T"}},
		{id: "query", label: "Edit query / find", canonical: "/", bindings: []string{"/"}},
		{id: "reset-view", label: "Reset view", canonical: "F", bindings: []string{"F"}},
		{id: "save-view", label: "Save view", canonical: "ctrl+s", bindings: []string{"ctrl+s"}},
		{id: "workspace", label: "Toggle workspace", bindings: []string{"W"}},
		{id: "refresh", label: "Resync sources", canonical: "R", bindings: []string{"R"}},
		{group: "Edit", id: "status", label: "Status…", canonical: "s", bindings: []string{"s"}},
		{id: "previous-status", label: "Previous status", canonical: "<", bindings: []string{"<"}},
		{id: "next-status", label: "Next status", canonical: ">", bindings: []string{">"}},
		{id: "start", label: "Start", canonical: "S", bindings: []string{"S"}},
		{id: "done", label: "Done", canonical: "D", bindings: []string{"D"}},
		{id: "priority", label: "Priority…", canonical: "p", bindings: []string{"p"}},
		{id: "assignees", label: "Assignees…", canonical: "a", bindings: []string{"a"}},
		{id: "assign-me", label: "Assign me", canonical: "A", bindings: []string{"A"}},
		{id: "labels", label: "Labels…", canonical: "#", bindings: []string{"#"}},
		{id: "milestone", label: "Milestone… / more", canonical: "m", bindings: []string{"m"}},
		{id: "sprint", label: "Sprint…", canonical: "w", bindings: []string{"w"}},
		{id: "blocker", label: "Blockers…", canonical: "b", bindings: []string{"b"}},
		{id: "parent", label: "Parent…", canonical: "P", bindings: []string{"P"}},
		{id: "title", label: "Edit title", canonical: "t", bindings: []string{"t"}},
		{id: "description", label: "Edit description", canonical: "e", bindings: []string{"e"}},
		{id: "edit", label: "Edit all fields", canonical: "E", bindings: []string{"E"}},
		{id: "comment", label: "Comment", canonical: "c", bindings: []string{"c"}},
		{id: "note", label: "Edit notes", canonical: "i", bindings: []string{"i"}},
		{id: "criterion", label: "Toggle criterion", canonical: "x", bindings: []string{"x"}},
		{id: "claim", label: "Claim / release", canonical: "C", bindings: []string{"C"}},
		{id: "archive", label: "Archive", canonical: "d d", bindings: []string{"d d"}},
		{id: "new", label: "New item / next match", canonical: "n", bindings: []string{"n"}},
		{id: "new-child", label: "New child / previous match", canonical: "N", bindings: []string{"N"}},
		{id: "repeat", label: "Repeat edit", canonical: ".", bindings: []string{"."}},
		{id: "undo", label: "Undo", canonical: "u", bindings: []string{"u"}},
		{id: "redo", label: "Redo", canonical: "ctrl+r", bindings: []string{"ctrl+r"}},
		{id: "actions", label: "Actions…", bindings: []string{"!"}},
		{id: "open-url", label: "Open URL", canonical: "O", bindings: []string{"O"}},
		{group: "Select", id: "mark", label: "Mark", canonical: "space", bindings: []string{"space"}},
		{id: "visual", label: "Visual range", canonical: "V", bindings: []string{"V"}},
		{id: "mark-all", label: "Mark all", canonical: "ctrl+a", bindings: []string{"ctrl+a"}},
		{id: "cancel", label: "Cancel / clear", canonical: "esc", bindings: []string{"esc", "ctrl+g"}},
		{group: "Copy", id: "yank-id", label: "Copy ID", canonical: "y y", bindings: []string{"y y"}},
		{id: "yank-title", label: "Copy title", canonical: "y t", bindings: []string{"y t"}},
		{id: "yank-branch", label: "Copy branch", canonical: "y b", bindings: []string{"y b"}},
		{id: "yank-url", label: "Copy URL", canonical: "y u", bindings: []string{"y u"}},
		{id: "yank-markdown", label: "Copy Markdown link", canonical: "y m", bindings: []string{"y m"}},
		{group: "General", id: "help", label: "Help", bindings: []string{"?"}},
		{id: "palette", label: "Commands", bindings: []string{":", "alt+x"}},
		{id: "quit", label: "Quit / back", canonical: "q", bindings: []string{"q"}},
		{id: "quit-now", label: "Quit now", canonical: "ctrl+c", bindings: []string{"ctrl+c"}},
	}
	for _, prefix := range []string{"f", "o", "z"} {
		name := prefixNames[prefix]
		for i, b := range queryMenus[prefix] {
			id, label := strings.ToLower(name)+"-"+b.value, name+": "+b.label
			switch {
			case b.value == "":
				id = "filter-clear"
			case prefix == "z" && zCommand(b.value):
				id, label = b.value, b.label
			}
			sequence := prefix + " " + b.key
			a := action{id: id, label: label, canonical: sequence, bindings: []string{sequence}}
			if i == 0 {
				a.group = name
			}
			actions = append(actions, a)
		}
	}
	for i := 1; i <= 9; i++ {
		key := fmt.Sprint(i)
		a := action{id: "view-" + key, label: "View " + key, canonical: key, bindings: []string{key}}
		if i == 1 {
			a.group = "Jump"
		}
		actions = append(actions, a)
	}
	return actions
}

// ValidateKeys is called before starting the program, so conflicting or unknown
// bindings fail startup rather than silently shadowing another action.
func ValidateKeys(overrides map[string]string, configured ...project.ActionConfig) error {
	_, err := buildKeymap(overrides, configured...)
	return err
}
func buildKeymap(overrides map[string]string, configured ...project.ActionConfig) ([]action, error) {
	actions := defaultKeymap()
	for _, configured := range configured {
		a := action{id: "action:" + configured.Name, label: configured.Name}
		if !strings.HasPrefix(actions[len(actions)-1].id, "action:") {
			a.group = "Actions"
		}
		if configured.Key != "" {
			a.bindings = []string{configured.Key}
		}
		actions = append(actions, a)
	}
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
	// Terminals report Alt with a capital as alt+shift+x, which bindings can't
	// spell, so only lowercase letters and symbols follow alt+.
	if rest, ok := strings.CutPrefix(key, "alt+"); ok {
		r := []rune(rest)
		return len(r) == 1 && !unicode.IsUpper(r[0]) && validKey(rest)
	}
	return false
}

// shiftedKeys maps US keys to their shifted symbols.
var shiftedKeys = map[string]string{"`": "~", "1": "!", "2": "@", "3": "#", "4": "$", "5": "%", "6": "^", "7": "&", "8": "*", "9": "(", "0": ")", "-": "_", "=": "+", "[": "{", "]": "}", "\\": "|", ";": ":", "'": "\"", ",": "<", ".": ">", "/": "?"}

// keyString names a key press the way bindings spell it. Terminals report Alt
// with a shifted symbol three ways: alt+< (an ESC prefix), alt+shift+< (xterm's
// modifyOtherKeys) and alt+shift+, (the kitty protocol, which sends the
// unshifted key of a US layout). All three become alt+<.
func keyString(msg tea.KeyPressMsg) string {
	s := msg.String()
	rest, ok := strings.CutPrefix(s, "alt+shift+")
	if r := []rune(rest); !ok || len(r) != 1 || unicode.IsLetter(r[0]) {
		return s
	}
	if shifted, ok := shiftedKeys[rest]; ok {
		rest = shifted
	}
	return "alt+" + rest
}

func (m Model) dispatchKey(key string) (tea.Model, tea.Cmd) {
	if m.keyError != nil {
		m.notice = m.keyError.Error()
		return m, nil
	}
	if key == " " {
		key = "space"
	}
	if m.keyPrefix != "" && (key == "esc" || key == "ctrl+g") {
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
	m.routeSelectedProject()
	if a.id == "workspace" {
		if !m.hasWorkspace() {
			m.notice = "No workspace configured for this project."
			return m, nil
		}
		m.opts.WorkspaceScope = !m.opts.WorkspaceScope
		m.marks = newMarkSet()
		m.project()
		return m, nil
	}
	if strings.HasPrefix(a.id, "action:") {
		return m.prepareConfiguredAction(strings.TrimPrefix(a.id, "action:"))
	}
	if a.unavailable != "" {
		m.notice = "Disabled: " + a.unavailable
		return m, nil
	}
	if a.id == "go-to" && m.goToWorker == nil {
		m.notice = "Disabled: go-to search is unavailable"
		return m, nil
	}
	// A list action's later keys would otherwise reach detail as item keys:
	// f s would open the status picker.
	if m.disabledReason(a) == focusList {
		m.notice = "Disabled: " + focusList
		return m, nil
	}
	switch a.id {
	case "actions":
		return m.openConfiguredActions()
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

const focusList = "focus the list first"

// listViewAction reports a filter, sort, group, fold or layout action, which
// changes the list without acting on an item.
func listViewAction(id string) bool {
	return id == "board" || id == "tree" || id == "reset-view" || zCommand(id) ||
		strings.HasPrefix(id, "filter-") || strings.HasPrefix(id, "sort-") || strings.HasPrefix(id, "group-")
}

func (m Model) disabledReason(a action) string {
	if a.id == "workspace" && !m.hasWorkspace() {
		return "no workspace configured for this project"
	}
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
	if a.id == "assign-me" {
		if row, ok := m.selectedRow(); ok && m.opts.Me[row.Ref.Source] == "" {
			return "no current user is configured"
		}
	}
	if a.id == "repeat" && m.lastEdit == nil {
		return "no edit to repeat"
	}
	if (a.id == "undo" || a.id == "redo") && m.opts.Store == nil {
		return "session history is unavailable"
	}
	if a.id == "back" && (!m.detailFocus || m.detached == nil || len(m.detailHistory) == 0) {
		return "no previous item"
	}
	if a.id == "criterion" && (!m.detailFocus || m.detailTab != tabCriteria) {
		return "open the Criteria tab"
	}
	if m.detailFocus {
		if a.id == "query" || m.split() && listViewAction(a.id) {
			return ""
		}
		if slices.Contains([]string{"move-down", "move-up", "next-item", "previous-item", "first", "last", "half-down", "half-up", "page-down", "page-up", "left", "detail", "next-detail-tab", "previous-detail-tab", "back", "cancel", "next-view", "previous-view", "go-to", "help", "palette", "quit", "quit-now", "save-view", "split-left", "split-right"}, a.id) {
			return ""
		}
		if a.id == "milestone" && (m.detailTruncated || m.detailExpanded) {
			return "" // m shows more of the cut-off body
		}
		if (a.id == "criterion" || a.id == "mark") && m.detailTab == tabCriteria {
			row, ok := m.selectedRow()
			if !ok {
				return "select an item"
			}
			return m.capability(row.Ref, model.CheckCriterion, model.UncheckCriterion)
		}
		// Item actions work on the item in detail; view and selection actions
		// belong to the list.
		if !strings.HasPrefix(a.id, "yank-") && !slices.Contains([]string{"status", "previous-status", "next-status", "start", "done", "priority", "assignees", "assign-me", "labels", "milestone", "sprint", "blocker", "parent", "title", "description", "edit", "comment", "note", "claim", "archive", "new", "new-child", "repeat", "undo", "redo", "actions", "open-url"}, a.id) {
			return focusList
		}
	}
	kind := map[string]pickerKind{"status": pickerStatus, "priority": pickerPriority, "assignees": pickerAssignees, "assign-me": pickerAssignees, "labels": pickerLabels, "milestone": pickerMilestone, "sprint": pickerSprint, "blocker": pickerBlocker, "parent": pickerParent, "title": pickerTitle}
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
		if a.id == "note" && m.capability(row.Ref, model.ReplaceSection) == "" {
			return ""
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
