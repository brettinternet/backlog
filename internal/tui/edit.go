package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/textutil"
)

type markSet struct {
	mu    sync.RWMutex
	refs  map[model.Ref]string
	count int
}

func newMarkSet() *markSet { return &markSet{refs: map[model.Ref]string{}} }
func (set *markSet) marked(ref model.Ref) bool {
	set.mu.RLock()
	defer set.mu.RUnlock()
	_, ok := set.refs[ref]
	return ok
}
func (set *markSet) size() int {
	set.mu.RLock()
	defer set.mu.RUnlock()
	return set.count
}
func (set *markSet) toggle(ref model.Ref, version string) {
	set.mu.Lock()
	defer set.mu.Unlock()
	if _, ok := set.refs[ref]; ok {
		delete(set.refs, ref)
		set.count--
	} else {
		set.refs[ref] = version
		set.count++
	}
}
func (set *markSet) replace(refs map[model.Ref]string) {
	set.mu.Lock()
	set.refs = refs
	set.count = len(refs)
	set.mu.Unlock()
}
func (set *markSet) copy() map[model.Ref]string {
	set.mu.RLock()
	defer set.mu.RUnlock()
	refs := make(map[model.Ref]string, set.count)
	for ref, version := range set.refs {
		refs[ref] = version
	}
	return refs
}
func (set *markSet) clear() { set.replace(map[model.Ref]string{}) }

type pickerKind string

const (
	pickerStatus    pickerKind = "status"
	pickerPriority  pickerKind = "priority"
	pickerAssignees pickerKind = "assignee"
	pickerLabels    pickerKind = "label"
	pickerMilestone pickerKind = "milestone"
	pickerBlocker   pickerKind = "blocker"
	pickerParent    pickerKind = "parent"
	pickerTitle     pickerKind = "title"
)

var pickerTitles = map[pickerKind]string{
	pickerStatus: "Status", pickerPriority: "Priority", pickerAssignees: "Assignees", pickerLabels: "Labels",
	pickerMilestone: "Milestone", pickerBlocker: "Add blocker", pickerParent: "Parent", pickerTitle: "Title",
}

type pickerChoice struct {
	value, label string
	hint         string // secondary text, such as a key that differs from its name
	current      bool
}
type pickerState struct {
	open, loading, multi, free, inputOnly, remove bool
	kind                                          pickerKind
	title                                         string
	query                                         lineInput
	choices, filtered                             []pickerChoice
	byValue                                       map[string]pickerChoice
	selected                                      map[string]bool
	cursor, generation                            int
}
type pickerChoicesMsg struct {
	generation int
	choices    []pickerChoice
	selected   map[string]bool
	byValue    map[string]pickerChoice
	cursor     int
}
type pickerFilterMsg struct {
	generation int
	query      string
	choices    []pickerChoice
	cursor     int
}
type editPart struct {
	op    model.Operation
	field string
	value any
}
type editKind uint8

const (
	editSimple editKind = iota
	editParts
	editSelfToggle
	editCriterionToggle
	editArchive
)

type editAction struct {
	kind    editKind
	op      model.Operation
	field   string
	value   any
	parts   []editPart
	section string
	index   int
	me      string
}
type editResultMsg struct {
	message string
}
type openResultMsg struct{ err string }

type editTargets struct {
	rows                 []screenRow
	marks                *markSet
	selected             model.Ref
	visual               bool
	visualAnchor, cursor int
}

func (targets editTargets) displayedVersion(ref model.Ref) string {
	for _, row := range targets.rows {
		if row.header == "" && row.row.Ref == ref {
			return row.row.Version
		}
	}
	return ""
}

func (m Model) targetRefs(targets editTargets, marks map[model.Ref]string) []model.Ref {
	refs := make([]model.Ref, 0, len(marks)+1)
	seen := make(map[model.Ref]bool, len(marks)+1)
	for ref := range marks {
		if _, ok := marks[ref]; ok {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	if targets.visual {
		start, end := targets.visualAnchor, targets.cursor
		if start > end {
			start, end = end, start
		}
		for index := start; index <= end && index < len(targets.rows); index++ {
			if index < 0 || targets.rows[index].header != "" {
				continue
			}
			ref := targets.rows[index].row.Ref
			if !seen[ref] {
				seen[ref] = true
				refs = append(refs, ref)
			}
		}
	}
	if len(refs) == 0 && !targets.visual && targets.selected != (model.Ref{}) {
		refs = append(refs, targets.selected)
	}
	return refs
}

func (m Model) captureTargets() editTargets {
	selected, ok := m.selectedRow()
	var ref model.Ref
	if ok {
		ref = selected.Ref
	}
	// Detail edits the item it shows, never marks or a visual range.
	if m.detailOpen && m.detailFocus {
		return editTargets{rows: m.data.rows, selected: ref}
	}
	return editTargets{rows: m.data.rows, marks: m.marks, selected: ref, visual: m.visual, visualAnchor: m.visualAnchor, cursor: m.selected}
}

func (m Model) capability(ref model.Ref, ops ...model.Operation) string {
	if m.opts.Store == nil {
		return "editing is unavailable without an engine store"
	}
	caps := m.opts.Store.Capabilities(ref.Source)
	for _, op := range ops {
		if !caps.Allows(op) {
			return fmt.Sprintf("source %s does not support %s", ref.Source, op)
		}
	}
	return ""
}

func (m *Model) beginPicker(kind pickerKind) tea.Cmd {
	row, ok := m.selectedRow()
	if !ok {
		m.notice = "Select an item before editing."
		return nil
	}
	if reason := m.pickerCapability(row.Ref, kind); reason != "" {
		m.notice = "Disabled: " + reason
		return nil
	}
	item, _, found := m.currentSnapshot().Get(row.Ref)
	if !found {
		m.notice = "The selected item is no longer available."
		return nil
	}
	p := pickerState{open: true, kind: kind, title: pickerTitles[kind], selected: map[string]bool{}}
	switch kind {
	case pickerAssignees, pickerLabels:
		p.multi, p.free = true, true
	case pickerMilestone:
		p.free = true
	case pickerTitle:
		p.inputOnly = true
		p.query = newLine(item.Title)
	}
	if kind == pickerTitle {
		m.picker = p
		m.notice = ""
		return nil
	}
	m.pickerSerial++
	p.loading = true
	p.generation = m.pickerSerial
	m.picker = p
	m.notice = ""
	generation := p.generation
	store := m.opts.Store
	settings := pickerSettings{statuses: slices.Clone(m.opts.Statuses), labels: slices.Clone(m.opts.Labels), milestones: slices.Clone(m.opts.Milestones), me: m.opts.Me, backlogMD: m.isBacklogMD(row.Ref.Source)}
	selectedItem := item
	return func() tea.Msg {
		choices := buildPickerChoices(store.Current(), kind, selectedItem, settings, false)
		return preparePickerChoices(generation, choices)
	}
}

type pickerSettings struct {
	statuses   []project.StatusConfig
	labels     []string
	milestones []project.Milestone
	me         string
	backlogMD  bool
}

func (m Model) pickerCapability(ref model.Ref, kind pickerKind) string {
	if m.opts.Store == nil {
		return "editing is unavailable without an engine store"
	}
	caps := m.opts.Store.Capabilities(ref.Source)
	var options []model.Operation
	switch kind {
	case pickerStatus, pickerPriority:
		options = []model.Operation{model.Set}
	case pickerMilestone, pickerParent:
		options = []model.Operation{model.Set, model.Unset}
	case pickerAssignees, pickerLabels, pickerBlocker:
		options = []model.Operation{model.Add, model.Remove}
	case pickerTitle:
		options = []model.Operation{model.Set}
	}
	for _, op := range options {
		if caps.Allows(op) {
			return ""
		}
	}
	if len(options) > 0 {
		return fmt.Sprintf("source %s does not support %s", ref.Source, strings.Join(operationNames(options), " or "))
	}
	return ""
}

func operationNames(ops []model.Operation) []string {
	values := make([]string, len(ops))
	for i, op := range ops {
		values[i] = string(op)
	}
	return values
}

func buildPickerChoices(snapshot *engine.Snapshot, kind pickerKind, current model.Item, settings pickerSettings, remove bool) []pickerChoice {
	choices := []pickerChoice{}
	add := func(value, label, currentValue string) {
		if value == "" && label == "" {
			return
		}
		for i := range choices {
			if choices[i].value == value {
				if choices[i].label == "" {
					choices[i].label = label
				}
				choices[i].current = choices[i].current || value == currentValue
				return
			}
		}
		choices = append(choices, pickerChoice{value: value, label: label, current: value == currentValue})
	}
	items := []model.Item{}
	if snapshot != nil {
		items = snapshot.Items()
	}
	switch kind {
	case pickerStatus:
		if transitions, ok := current.Fields["jiraTransitions"].(map[string]any); ok {
			names := map[string]bool{}
			for name := range transitions {
				names[name] = true
			}
			for _, name := range sortedKeys(names) {
				add(name, name, current.Status)
			}
			break
		}
		for _, status := range settings.statuses {
			label := status.Name
			if label == "" {
				label = status.Key
			}
			add(status.Key, label, current.Status)
			if index := choiceIndex(choices, status.Key); index >= 0 && label != status.Key {
				choices[index].hint = status.Key
			}
		}
		add(current.Status, current.Status, current.Status)
	case pickerPriority:
		for _, priority := range []model.Priority{model.Urgent, model.High, model.Medium, model.Low} {
			if settings.backlogMD && priority == model.Urgent {
				continue
			}
			add(string(priority), string(priority), string(current.Priority))
		}
		add("", "None", string(current.Priority))
	case pickerAssignees:
		values := map[string]bool{}
		for _, item := range items {
			for _, value := range item.Assignees {
				values[value] = true
			}
		}
		for _, value := range current.Assignees {
			values[value] = true
		}
		if settings.me != "" {
			values[settings.me] = true
		}
		addSorted := sortedKeys(values)
		for _, value := range addSorted {
			add(value, value, "")
		}
		for _, value := range current.Assignees {
			if choiceIndex(choices, value) >= 0 {
				choices[choiceIndex(choices, value)].current = true
			}
		}
	case pickerLabels:
		values := map[string]bool{}
		for _, item := range items {
			for _, value := range item.Labels {
				values[value] = true
			}
		}
		for _, value := range current.Labels {
			values[value] = true
		}
		for _, value := range settings.labels {
			add(value, value, "")
			delete(values, value)
		}
		for _, value := range sortedKeys(values) {
			add(value, value, "")
		}
		for _, value := range current.Labels {
			if index := choiceIndex(choices, value); index >= 0 {
				choices[index].current = true
			}
		}
	case pickerMilestone:
		values := map[string]bool{}
		for _, item := range items {
			if item.Milestone != "" {
				values[item.Milestone] = true
			}
		}
		for _, milestone := range settings.milestones {
			if milestone.Archived {
				delete(values, milestone.Key)
				continue
			}
			label := milestone.Name
			if label == "" {
				label = milestone.Key
			}
			add(milestone.Key, label, current.Milestone)
			if index := choiceIndex(choices, milestone.Key); index >= 0 && label != milestone.Key {
				choices[index].hint = milestone.Key
			}
			delete(values, milestone.Key)
		}
		for _, value := range sortedKeys(values) {
			add(value, value, current.Milestone)
		}
		add("", "None", current.Milestone)
	case pickerBlocker:
		if remove {
			for _, ref := range current.BlockedBy {
				value := qualifiedRef(ref, current.Ref.Source)
				add(value, relationLabel(snapshot, ref, current.Ref.Source), "")
			}
		} else {
			blocked := map[string]bool{}
			for _, ref := range current.BlockedBy {
				blocked[qualifiedRef(ref, current.Ref.Source)] = true
			}
			for _, item := range items {
				if item.Ref == current.Ref || blocked[qualifiedRef(item.Ref, current.Ref.Source)] {
					continue
				}
				add(qualifiedRef(item.Ref, current.Ref.Source), item.Ref.ID+" · "+item.Title, "")
			}
		}
	case pickerParent:
		currentParent := ""
		if current.Parent != nil {
			currentParent = qualifiedRef(*current.Parent, current.Ref.Source)
		}
		add("", "None", currentParent)
		for _, item := range items {
			if item.Ref != current.Ref {
				value := qualifiedRef(item.Ref, current.Ref.Source)
				add(value, item.Ref.ID+" · "+item.Title, currentParent)
			}
		}
	}
	return choices
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	slices.Sort(keys)
	return keys
}
func choiceIndex(choices []pickerChoice, value string) int {
	for i, choice := range choices {
		if choice.value == value {
			return i
		}
	}
	return -1
}
func qualifiedRef(ref model.Ref, source string) string {
	if ref.Source == "" || ref.Source == source {
		return ref.ID
	}
	return ref.Source + ":" + ref.ID
}
func relationLabel(snapshot *engine.Snapshot, ref model.Ref, source string) string {
	id := qualifiedRef(ref, source)
	if snapshot == nil {
		return id
	}
	if ref.Source == "" {
		ref.Source = source
	}
	item, _, ok := snapshot.Get(ref)
	if !ok {
		return id
	}
	return id + " · " + item.Title
}

func pickerScore(query, value string) (int, bool) {
	query, value = strings.ToLower(query), strings.ToLower(value)
	if query == "" {
		return 0, true
	}
	at, score := 0, 0
	for _, char := range query {
		found := strings.IndexRune(value[at:], char)
		if found < 0 {
			return 0, false
		}
		score += found
		at += found + len(string(char))
	}
	return score, true
}

func (m *Model) filterPicker() tea.Cmd {
	p := m.picker
	m.pickerSerial++
	generation := m.pickerSerial
	m.picker.generation = generation
	m.picker.loading = true
	choices, text := p.choices, p.query.text
	return func() tea.Msg {
		matches := make([]pickerChoice, 0, len(choices))
		scores := make(map[string]int)
		for _, choice := range choices {
			score, ok := pickerScore(text, choice.label+" "+choice.value)
			if ok {
				matches = append(matches, choice)
				scores[choice.value] = score
			}
		}
		if text != "" {
			sort.SliceStable(matches, func(i, j int) bool {
				if scores[matches[i].value] != scores[matches[j].value] {
					return scores[matches[i].value] < scores[matches[j].value]
				}
				return strings.ToLower(matches[i].label) < strings.ToLower(matches[j].label)
			})
		}
		cursor := 0
		for i, choice := range matches {
			if choice.current {
				cursor = i
				break
			}
		}
		return pickerFilterMsg{generation: generation, query: text, choices: matches, cursor: cursor}
	}
}

func (m *Model) updatePickerKey(key string) tea.Cmd {
	p := &m.picker
	if key == "esc" {
		m.picker = pickerState{}
		m.notice = ""
		return nil
	}
	if key == "up" {
		p.cursor = max(0, p.cursor-1)
		return nil
	}
	if key == "down" {
		if len(p.filtered) > 0 {
			p.cursor = min(len(p.filtered)-1, p.cursor+1)
		} else {
			p.cursor = 0
		}
		return nil
	}
	if key == "space" || key == " " {
		if p.multi && p.query.text == "" {
			if p.cursor >= 0 && p.cursor < len(p.filtered) {
				value := p.filtered[p.cursor].value
				p.selected[value] = !p.selected[value]
			}
			return nil
		}
		key = " "
	}
	if p.kind == pickerPriority && p.query.text == "" && !p.loading {
		values := map[string]string{"u": "urgent", "h": "high", "m": "medium", "l": "low", "n": ""}
		if value, ok := values[key]; ok && choiceIndex(p.choices, value) >= 0 {
			action, _ := pickerAction(pickerPriority, value, false)
			m.picker = pickerState{}
			m.startEdit(action)
			return m.editCommand(action, false)
		}
	}
	if key == "d" && p.kind == pickerBlocker && !p.remove {
		p.remove = true
		p.title = "Remove blocker"
		return m.reloadPickerChoices()
	}
	if key == "enter" {
		if p.loading {
			return nil
		}
		if p.inputOnly {
			action := editAction{kind: editSimple, op: model.Set, field: "title", value: p.query.text}
			m.picker = pickerState{}
			m.startEdit(action)
			return m.editCommand(action, false)
		}
		if p.multi {
			selected := make(map[string]bool, len(p.selected)+1)
			for value, yes := range p.selected {
				if yes {
					selected[value] = true
				}
			}
			if p.query.text != "" {
				if choice, ok := p.byValue[strings.ToLower(p.query.text)]; ok && !selected[choice.value] {
					selected[choice.value] = true
				} else if !ok && len(p.filtered) > 0 {
					index := max(0, min(p.cursor, len(p.filtered)-1))
					selected[p.filtered[index].value] = true
				} else if p.free && !ok {
					selected[strings.TrimSpace(p.query.text)] = true
				}
			}
			parts := make([]editPart, 0)
			for _, choice := range p.byValue {
				if choice.current && !selected[choice.value] {
					parts = append(parts, editPart{op: model.Remove, field: string(p.kind), value: choice.value})
				}
			}
			for value := range selected {
				if !p.byValue[strings.ToLower(value)].current {
					parts = append(parts, editPart{op: model.Add, field: string(p.kind), value: value})
				}
			}
			field := "labels"
			if p.kind == pickerAssignees {
				field = "assignees"
			}
			for i := range parts {
				parts[i].field = field
			}
			m.picker = pickerState{}
			if len(parts) == 0 {
				m.notice = "No changes."
				return nil
			}
			action := editAction{kind: editParts, parts: parts}
			m.startEdit(action)
			return m.editCommand(action, false)
		}
		var choice pickerChoice
		found := p.cursor >= 0 && p.cursor < len(p.filtered)
		if found {
			choice = p.filtered[p.cursor]
		} else if p.free && strings.TrimSpace(p.query.text) != "" {
			choice = pickerChoice{value: strings.TrimSpace(p.query.text), label: strings.TrimSpace(p.query.text)}
			found = true
		}
		if !found {
			return nil
		}
		kind := p.kind
		remove := p.remove
		m.picker = pickerState{}
		action, err := pickerAction(kind, choice.value, remove)
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		m.startEdit(action)
		return m.editCommand(action, false)
	}
	if _, changed := p.query.edit(key, ""); !changed || p.inputOnly || p.loading && len(p.choices) == 0 {
		return nil
	}
	return m.filterPicker()
}

func (m *Model) reloadPickerChoices() tea.Cmd {
	p := m.picker
	row, ok := m.selectedRow()
	if !ok {
		return nil
	}
	item, _, found := m.currentSnapshot().Get(row.Ref)
	if !found {
		return nil
	}
	m.pickerSerial++
	generation := m.pickerSerial
	m.picker.generation = generation
	m.picker.loading = true
	store := m.opts.Store
	settings := pickerSettings{statuses: slices.Clone(m.opts.Statuses), labels: slices.Clone(m.opts.Labels), milestones: slices.Clone(m.opts.Milestones), me: m.opts.Me, backlogMD: m.isBacklogMD(row.Ref.Source)}
	kind, remove := p.kind, p.remove
	return func() tea.Msg {
		choices := buildPickerChoices(store.Current(), kind, item, settings, remove)
		return preparePickerChoices(generation, choices)
	}
}

func pickerAction(kind pickerKind, value string, remove bool) (editAction, error) {
	switch kind {
	case pickerStatus:
		return editAction{kind: editSimple, op: model.Set, field: "status", value: value}, nil
	case pickerPriority:
		return editAction{kind: editSimple, op: model.Set, field: "priority", value: value}, nil
	case pickerMilestone:
		if value == "" {
			return editAction{kind: editSimple, op: model.Unset, field: "milestone"}, nil
		}
		return editAction{kind: editSimple, op: model.Set, field: "milestone", value: value}, nil
	case pickerBlocker:
		op := model.Add
		if remove {
			op = model.Remove
		}
		return editAction{kind: editSimple, op: op, field: "blocked_by", value: value}, nil
	case pickerParent:
		if value == "" {
			return editAction{kind: editSimple, op: model.Unset, field: "parent"}, nil
		}
		return editAction{kind: editSimple, op: model.Set, field: "parent", value: value}, nil
	default:
		return editAction{}, fmt.Errorf("unsupported picker %s", kind)
	}
}

func (m *Model) startEdit(action editAction) {
	m.lastEdit = &action
	m.notice = ""
}

func (m Model) editCommand(action editAction, currentVersion bool) tea.Cmd {
	store := m.opts.Store
	if store == nil {
		return func() tea.Msg { return editResultMsg{message: "Editing is unavailable without an engine store."} }
	}
	targets := m.captureTargets()
	return func() tea.Msg {
		marks := map[model.Ref]string{}
		if targets.marks != nil {
			marks = targets.marks.copy()
		}
		refs := m.targetRefs(targets, marks)
		if len(refs) == 0 {
			return editResultMsg{message: "No editable items are selected."}
		}
		snapshot := store.Current()
		if coordinator, _ := engine.Coordination(m.opts.Context); coordinator != nil && len(refs) > 1 {
			for _, ref := range refs {
				item, _, ok := snapshot.Get(ref)
				if !ok {
					continue
				}
				mutation, ok := action.mutation(item)
				if !ok {
					continue
				}
				cfg := project.Config{Settings: project.Settings{Statuses: m.opts.Statuses}}
				_, category, changes := engine.StatusTarget(cfg, mutation)
				if changes && category == model.Active {
					return editResultMsg{message: "Coordination refuses multi-item activation."}
				}
			}
		}
		type result struct {
			ref    model.Ref
			result <-chan error
		}
		pending := make([]result, 0, len(refs))
		disabled := []string{}
		noChanges := false
		for _, ref := range refs {
			item, _, ok := snapshot.Get(ref)
			if !ok {
				continue
			}
			mutation, ok := action.mutation(item)
			if !ok {
				noChanges = true
				continue
			}
			mutation.Ref = ref
			mutation.ExpectedVersion = targets.displayedVersion(ref)
			if mutation.ExpectedVersion == "" {
				mutation.ExpectedVersion = marks[ref]
			}
			if currentVersion || mutation.ExpectedVersion == "" {
				mutation.ExpectedVersion = item.Version
			}
			caps := store.Capabilities(ref.Source)
			if reason := mutationCapabilityReason(caps, mutation); reason != "" {
				disabled = append(disabled, ref.ID+": "+reason)
				continue
			}
			pending = append(pending, result{ref: ref, result: store.Mutate(mutation)})
		}
		failures := []string{}
		for _, write := range pending {
			select {
			case err := <-write.result:
				if err != nil {
					failures = append(failures, write.ref.ID+": "+boundedLine(err.Error()))
				}
			case <-m.opts.Context.Done():
				return editResultMsg{message: "Edit canceled while waiting for the source."}
			}
		}
		parts := []string{}
		if len(pending) > 0 {
			parts = append(parts, fmt.Sprintf("Edited %d item(s).", len(pending)-len(failures)))
		}
		if len(failures) > 0 {
			parts = append(parts, issueSummary("Failed", failures))
		}
		if len(disabled) > 0 {
			parts = append(parts, issueSummary("Disabled", disabled))
		}
		if len(parts) == 0 {
			if noChanges {
				parts = append(parts, "No changes.")
			} else {
				parts = append(parts, "No source supports this edit.")
			}
		}
		return editResultMsg{message: strings.Join(parts, " ")}
	}
}

func boundedLine(value string) string {
	line := textutil.Line(value)
	runes := []rune(line)
	if len(runes) > 200 {
		return string(runes[:197]) + "…"
	}
	return line
}

func issueSummary(label string, issues []string) string {
	shown := issues[:min(3, len(issues))]
	message := fmt.Sprintf("%s (%d): %s", label, len(issues), strings.Join(shown, "; "))
	if len(issues) > len(shown) {
		message += fmt.Sprintf("; and %d more", len(issues)-len(shown))
	}
	return boundedLine(message)
}

func mutationCapabilityReason(caps model.Capabilities, mutation model.Mutation) string {
	if !caps.Allows(mutation.Op) {
		return "source does not support " + string(mutation.Op)
	}
	if mutation.Op == model.Batch {
		for _, change := range mutation.Changes {
			if !caps.Allows(change.Op) {
				return "source does not support " + string(change.Op)
			}
		}
	}
	return ""
}

func (action editAction) mutation(item model.Item) (model.Mutation, bool) {
	mutation := model.Mutation{Ref: item.Ref}
	switch action.kind {
	case editSimple:
		mutation.Op, mutation.Field, mutation.Value = action.op, action.field, action.value
	case editParts:
		changes := make([]model.Mutation, 0, len(action.parts))
		for _, part := range action.parts {
			present := false
			switch part.field {
			case "labels":
				present = slices.Contains(item.Labels, part.value.(string))
			case "assignees":
				present = slices.Contains(item.Assignees, part.value.(string))
			}
			if part.op == model.Add && present || part.op == model.Remove && !present {
				continue
			}
			changes = append(changes, model.Mutation{Ref: item.Ref, Op: part.op, Field: part.field, Value: part.value})
		}
		if len(changes) == 0 {
			return model.Mutation{}, false
		}
		if len(changes) == 1 {
			mutation = changes[0]
		} else {
			mutation.Op, mutation.Changes = model.Batch, changes
		}
	case editSelfToggle:
		mutation.Field, mutation.Value = "assignees", action.me
		mutation.Op = model.Add
		if slices.Contains(item.Assignees, action.me) {
			mutation.Op = model.Remove
		}
	case editCriterionToggle:
		var criteria []model.Criterion
		if action.section == "Acceptance criteria" {
			criteria = item.Criteria
		} else if action.section == "Definition of done" {
			criteria = item.DoD
		} else {
			return model.Mutation{}, false
		}
		if action.index < 0 || action.index >= len(criteria) {
			return model.Mutation{}, false
		}
		mutation.Section, mutation.Index = action.section, action.index+1
		mutation.Op = model.CheckCriterion
		if criteria[action.index].Checked {
			mutation.Op = model.UncheckCriterion
		}
	case editArchive:
		mutation.Op = model.Archive
	}
	return mutation, true
}

func (m *Model) editResult(msg editResultMsg) { m.notice = msg.message }

func (m *Model) markCurrent() {
	row, ok := m.selectedRow()
	if !ok {
		m.notice = "Select an item to mark."
		return
	}
	if m.marks == nil {
		m.marks = newMarkSet()
	}
	m.marks.toggle(row.Ref, row.Version)
	m.visual = false
	m.notice = ""
}
func (m *Model) markAll() tea.Cmd {
	groups := m.groups
	marks := m.marks
	if marks == nil {
		marks = newMarkSet()
		m.marks = marks
	}
	m.visual = false
	return func() tea.Msg {
		refs := map[model.Ref]string{}
		for _, group := range groups {
			for _, row := range group.Rows {
				refs[row.Ref] = row.Version
			}
		}
		marks.replace(refs)
		return editResultMsg{message: fmt.Sprintf("Marked %d item(s).", len(refs))}
	}
}
func (m Model) isMarked(index int, ref model.Ref) bool {
	if m.marks != nil && m.marks.marked(ref) {
		return true
	}
	if m.visual {
		start, end := m.visualAnchor, m.selected
		if start > end {
			start, end = end, start
		}
		return index >= start && index <= end
	}
	return false
}

func (m *Model) archivePrompt() {
	row, ok := m.selectedRow()
	if !ok {
		m.notice = "Select an item before archiving."
		return
	}
	if reason := m.capability(row.Ref, model.Archive); reason != "" {
		m.notice = "Disabled: " + reason
		return
	}
	m.archiveConfirm = true
	m.notice = ""
}
func (m *Model) confirmArchive() tea.Cmd {
	m.archiveConfirm = false
	action := editAction{kind: editArchive}
	m.startEdit(action)
	return m.editCommand(action, false)
}

func (m *Model) workflowStatus(direction int) tea.Cmd {
	row, ok := m.selectedRow()
	if !ok {
		m.notice = "Select an item before changing status."
		return nil
	}
	if reason := m.capability(row.Ref, model.Set); reason != "" {
		m.notice = "Disabled: " + reason
		return nil
	}
	index := -1
	for i, status := range m.opts.Statuses {
		if status.Key == row.Status {
			index = i
			break
		}
	}
	next := index + direction
	if index < 0 || next < 0 || next >= len(m.opts.Statuses) {
		m.notice = "No previous or next configured status."
		return nil
	}
	action := editAction{kind: editSimple, op: model.Set, field: "status", value: m.opts.Statuses[next].Key}
	m.startEdit(action)
	return m.editCommand(action, false)
}

func (m *Model) workflowTransition(status, category string) tea.Cmd {
	if status == "" {
		for _, configured := range m.opts.Statuses {
			if configured.Category == category {
				status = configured.Key
				break
			}
		}
	}
	if status == "" {
		m.notice = "Disabled: no configured " + category + " status."
		return nil
	}
	row, ok := m.selectedRow()
	if !ok {
		m.notice = "Select an item before changing status."
		return nil
	}
	if reason := m.capability(row.Ref, model.Set); reason != "" {
		m.notice = "Disabled: " + reason
		return nil
	}
	action := editAction{kind: editSimple, op: model.Set, field: "status", value: status}
	m.startEdit(action)
	return m.editCommand(action, false)
}

func safeURL(value string) (string, error) {
	if len(value) > 1024 {
		return "", fmt.Errorf("URL exceeds 1 KiB")
	}
	if strings.ContainsAny(value, "\x00\r\n\x1b") {
		return "", fmt.Errorf("URL contains terminal control characters")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || (strings.ToLower(parsed.Scheme) != "https" && strings.ToLower(parsed.Scheme) != "http") {
		return "", fmt.Errorf("only absolute HTTP(S) URLs without credentials can be opened")
	}
	return parsed.String(), nil
}

func openURL(ctx context.Context, value string) error {
	value, err := safeURL(value)
	if err != nil {
		return err
	}
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{value}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", value}
	default:
		name, args = "xdg-open", []string{value}
	}
	return exec.CommandContext(ctx, name, args...).Run()
}

func (m Model) openSelectedURL() tea.Cmd {
	row, ok := m.selectedRow()
	if !ok || row.URL == "" {
		return func() tea.Msg { return openResultMsg{err: "Selected item has no URL."} }
	}
	value, err := safeURL(row.URL)
	if err != nil {
		return func() tea.Msg { return openResultMsg{err: err.Error()} }
	}
	open := m.opts.OpenURL
	if open == nil {
		open = openURL
	}
	ctx := m.opts.Context
	return func() tea.Msg {
		if err := open(ctx, value); err != nil {
			return openResultMsg{err: boundedLine(err.Error())}
		}
		return openResultMsg{err: "Opened item URL."}
	}
}

func osc52(value string) tea.Cmd {
	sequence := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(value)) + "\a"
	return tea.Raw(sequence)
}

func (m *Model) yank(key string) tea.Cmd {
	row, ok := m.selectedRow()
	if !ok {
		return nil
	}
	value := ""
	switch key {
	case "y":
		value = row.Ref.ID
	case "t":
		value = row.Title
	case "b":
		value = model.BranchName(model.Item{Ref: row.Ref, Title: row.Title})
	case "u":
		value = row.URL
	case "m":
		if row.URL != "" {
			value = fmt.Sprintf("[%s](%s)", row.Title, row.URL)
		}
	default:
		m.notice = "Unknown yank target."
		return nil
	}
	if value == "" {
		m.notice = "Selected item has no value for that yank."
		return nil
	}
	m.notice = "Copied " + textutil.Line(value)
	return osc52(value)
}

func pickerChoiceMap(choices []pickerChoice) map[string]pickerChoice {
	values := make(map[string]pickerChoice, len(choices))
	for _, choice := range choices {
		values[strings.ToLower(choice.value)] = choice
	}
	return values
}

func preparePickerChoices(generation int, choices []pickerChoice) pickerChoicesMsg {
	selected := make(map[string]bool)
	cursor, foundCurrent := 0, false
	for i, choice := range choices {
		if choice.current {
			selected[choice.value] = true
			if !foundCurrent {
				cursor, foundCurrent = i, true
			}
		}
	}
	return pickerChoicesMsg{generation: generation, choices: choices, selected: selected, byValue: pickerChoiceMap(choices), cursor: cursor}
}

func updatePickerChoices(p *pickerState, message pickerChoicesMsg) {
	p.choices = message.choices
	p.byValue = message.byValue
	p.selected = message.selected
	p.cursor = message.cursor
	p.filtered = message.choices
	p.loading = false
}
