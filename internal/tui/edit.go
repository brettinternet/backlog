package tui

import (
	"cmp"
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
	"github.com/brettinternet/backlog/internal/query"
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
	pickerSprint    pickerKind = "sprint"
	pickerBlocker   pickerKind = "blocker"
	pickerParent    pickerKind = "parent"
	pickerTitle     pickerKind = "title"
)

var pickerTitles = map[pickerKind]string{
	pickerStatus: "Status", pickerPriority: "Priority", pickerAssignees: "Assignees", pickerLabels: "Labels",
	pickerMilestone: "Milestone", pickerSprint: "Sprint", pickerBlocker: "Add blocker", pickerParent: "Parent", pickerTitle: "Title",
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
	notice     string // closes the picker when choices can't load
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
	// workflow resolves the status per item source off the UI loop (D35).
	workflow model.StatusCategory
	// step moves each item to its source's previous (-1) or next (1) status.
	step int
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
	if kind == pickerSprint {
		m.opts.Store.UseSprints()
	}
	return m.loadPickerChoices(p.generation, kind, item, false)
}

// loadPickerChoices builds choices in a command, since the statuses a source
// accepts may need provider I/O (D35).
func (m Model) loadPickerChoices(generation int, kind pickerKind, item model.Item, remove bool) tea.Cmd {
	store := m.opts.Store
	settings := pickerSettings{statuses: slices.Clone(m.opts.Statuses), labels: slices.Clone(m.opts.Labels), milestones: slices.Clone(m.opts.Milestones), me: m.opts.Me[item.Ref.Source], backlogMD: m.isBacklogMD(item.Ref.Source)}
	return func() tea.Msg {
		if kind == pickerStatus {
			accepted, err := store.StatusChoices(item)
			if err != nil {
				return pickerChoicesMsg{generation: generation, notice: "Status choices unavailable: " + boundedLine(engine.ErrorMessage(err))}
			}
			settings.accepted = accepted
		}
		return preparePickerChoices(generation, buildPickerChoices(store.Current(), kind, item, settings, remove))
	}
}

type pickerSettings struct {
	statuses   []project.StatusConfig
	accepted   []model.StatusChoice // statuses the item's source accepts
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
	case pickerSprint:
		if !m.opts.Store.SprintSource(ref.Source) {
			return fmt.Sprintf("source %s has no sprints", ref.Source)
		}
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
		names := map[string]string{}
		for _, status := range settings.statuses {
			names[status.Key] = status.Name
		}
		entries := project.Settings{Statuses: settings.statuses}
		for _, key := range statusOrder(settings.statuses, settings.accepted) {
			label := cmp.Or(names[entries.StatusEntry(key)], current.Names[key], key)
			add(key, label, current.Status)
			if index := choiceIndex(choices, key); index >= 0 && label != key {
				choices[index].hint = key
			}
		}
	case pickerPriority:
		for _, priority := range []model.Priority{model.Urgent, model.High, model.Medium, model.Low} {
			if settings.backlogMD && priority == model.Urgent {
				continue
			}
			add(string(priority), string(priority), string(current.Priority))
		}
		add("", "None", string(current.Priority))
	case pickerAssignees:
		values, names := map[string]bool{}, map[string]string{}
		collect := func(item model.Item) {
			for _, value := range item.Assignees {
				values[value] = true
				if name := item.Names[value]; name != "" {
					names[value] = name
				}
			}
		}
		for _, item := range items {
			collect(item)
		}
		collect(current)
		if settings.me != "" {
			values[settings.me] = true
		}
		sorted := sortedKeys(values)
		slices.SortStableFunc(sorted, func(a, b string) int {
			return strings.Compare(strings.ToLower(cmp.Or(names[a], a)), strings.ToLower(cmp.Or(names[b], b)))
		})
		for _, value := range sorted {
			add(value, cmp.Or(names[value], value), "")
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
	case pickerSprint:
		// Open sprints of the item's source: current, next, then later ones.
		var catalog query.Snapshot
		if snapshot != nil {
			catalog.Sprints = snapshot.Sprints()
		}
		roles := map[string]int{"current": 0, "next": 1, "future": 2}
		undated := func(s model.Sprint) int {
			if s.Start.IsZero() {
				return 1
			}
			return 0
		}
		open := []model.Sprint{}
		for _, sprint := range catalog.Sprints {
			if _, role, _ := catalog.Sprint(sprint.Key); sprint.Source == current.Ref.Source && role != "closed" {
				open = append(open, sprint)
			}
		}
		slices.SortStableFunc(open, func(a, b model.Sprint) int {
			_, ra, _ := catalog.Sprint(a.Key)
			_, rb, _ := catalog.Sprint(b.Key)
			return cmp.Or(cmp.Compare(roles[ra], roles[rb]), cmp.Compare(undated(a), undated(b)), a.Start.Compare(b.Start), strings.Compare(a.Name, b.Name))
		})
		for _, sprint := range open {
			add(sprint.Key, sprint.Name, current.Sprint)
		}
		add("", "None", current.Sprint)
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
	m.picker.generation = m.pickerSerial
	m.picker.loading = true
	return m.loadPickerChoices(m.pickerSerial, p.kind, item, p.remove)
}

func pickerAction(kind pickerKind, value string, remove bool) (editAction, error) {
	switch kind {
	case pickerStatus:
		return editAction{kind: editSimple, op: model.Set, field: "status", value: value}, nil
	case pickerPriority:
		return editAction{kind: editSimple, op: model.Set, field: "priority", value: value}, nil
	case pickerSprint:
		return editAction{kind: editSimple, op: model.Set, field: "sprint", value: value}, nil
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
		mutations := make([]model.Mutation, 0, len(refs))
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
			if action.workflow != "" {
				status, err := store.WorkflowStatus(item, action.workflow)
				if err != nil {
					disabled = append(disabled, ref.ID+": "+engine.ErrorMessage(err))
					continue
				}
				mutation.Value = status
			}
			if action.step != 0 {
				accepted, err := store.StatusChoices(item)
				if err != nil {
					disabled = append(disabled, ref.ID+": "+engine.ErrorMessage(err))
					continue
				}
				status := stepStatus(m.opts.Statuses, accepted, item.Status, action.step)
				if status == "" {
					disabled = append(disabled, fmt.Sprintf("%s: source %s accepts no %s status", ref.ID, ref.Source, map[int]string{-1: "previous", 1: "next"}[action.step]))
					continue
				}
				mutation.Value = status
			}
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
			mutations = append(mutations, mutation)
		}
		if coordinator, _ := engine.Coordination(m.opts.Context); coordinator != nil && len(refs) > 1 {
			cfg := project.Config{Settings: project.Settings{Statuses: m.opts.Statuses}}
			for _, mutation := range mutations {
				_, category, changes := engine.StatusTarget(cfg, mutation)
				if changes && category == model.Active || action.workflow == model.Active {
					return editResultMsg{message: "Coordination refuses multi-item activation."}
				}
			}
		}
		type result struct {
			ref    model.Ref
			result <-chan error
		}
		pending := make([]result, 0, len(mutations))
		for _, mutation := range mutations {
			pending = append(pending, result{ref: mutation.Ref, result: store.Mutate(mutation)})
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
		if action.field == "sprint" {
			// The displayed sprint guards the write, as the version guards the rest.
			mutation.ExpectedSprint = new(item.Sprint)
		}
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
	action := editAction{kind: editSimple, op: model.Set, field: "status", step: direction}
	m.startEdit(action)
	return m.editCommand(action, false)
}

// statusOrder lists the accepted keys in config order, an included key at its
// entry's position (D36), then the source's own keys config doesn't declare.
func statusOrder(statuses []project.StatusConfig, accepted []model.StatusChoice) []string {
	settings := project.Settings{Statuses: statuses}
	keys := []string{}
	for _, status := range statuses {
		for _, key := range settings.StatusMembers(status.Key) {
			if slices.ContainsFunc(accepted, func(c model.StatusChoice) bool { return c.Key == key }) && !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
	}
	for _, choice := range accepted {
		if !slices.Contains(keys, choice.Key) {
			keys = append(keys, choice.Key)
		}
	}
	return keys
}

// stepStatus is the accepted status before (-1) or after (1) current's column
// in statusOrder, skipping statuses the source doesn't accept, or "" if none.
// An unaccepted current status keeps its config position.
func stepStatus(statuses []project.StatusConfig, accepted []model.StatusChoice, current string, direction int) string {
	settings := project.Settings{Statuses: statuses}
	keys := statusOrder(statuses, accepted)
	rank := func(key string) int {
		entry := settings.StatusEntry(key)
		if i := slices.IndexFunc(statuses, func(s project.StatusConfig) bool { return s.Key == entry }); i >= 0 {
			return i
		}
		if i := slices.Index(keys, key); i >= 0 {
			return len(statuses) + i
		}
		return -1
	}
	at := rank(current)
	if at < 0 {
		return ""
	}
	best := ""
	for _, key := range keys {
		if r := rank(key); direction > 0 && r > at && best == "" || direction < 0 && r < at {
			best = key
		}
	}
	return best
}

func (m *Model) workflowTransition(category model.StatusCategory) tea.Cmd {
	row, ok := m.selectedRow()
	if !ok {
		m.notice = "Select an item before changing status."
		return nil
	}
	if reason := m.capability(row.Ref, model.Set); reason != "" {
		m.notice = "Disabled: " + reason
		return nil
	}
	action := editAction{kind: editSimple, op: model.Set, field: "status", workflow: category}
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
