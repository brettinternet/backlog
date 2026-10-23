// Package tui adapts immutable engine projections to a viewport-only UI.
package tui

import (
	"context"
	"image/color"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/textutil"
)

type Options struct {
	Workspace                               *project.Config
	WorkspaceScope                          bool
	Automation                              *project.Config
	HighContrast, NoColor                   bool
	Project                                 string // display name
	Store                                   *engine.Store
	Projector                               *engine.Projector
	Context                                 context.Context
	Sources                                 []string
	Statuses                                []project.StatusConfig
	Workflow                                project.WorkflowConfig
	Labels                                  []string
	Milestones                              []project.Milestone
	Me                                      string
	CreateSource, CreateStatus, DefaultType string
	DefaultDoD                              []string
	ValidateItem                            func(model.Item) error
	ResolveRef                              func(context.Context, string, model.Ref) (model.Ref, error)
	NativeBytes                             func(context.Context, model.Item) ([]byte, error)
	NativeSource                            func(string) bool
	EditorDir, CommentAuthor                string
	OpenURL                                 func(context.Context, string) error
	Read                                    func(context.Context, model.Ref) (model.Item, error)
	Render                                  func(string, int) (string, error)
	Views                                   []project.SavedView
	Session                                 *Session
	Warning                                 string
	TrustWarning                            string
	SaveView                                func(project.ViewConfig, bool) (project.SavedView, error)
	Keys                                    map[string]string
	GoToItems                               func(context.Context) ([]GoToItem, error)
	Now                                     func() time.Time
}

type screenRow struct {
	row   engine.Row
	group int
	value string // group value; folds and selection survive live projections
	// header is the display label of a group header row; empty for items.
	header   string
	groupKey string // query group field of a header
	category model.StatusCategory
	count    int // items in a header's group
	ordinal  int // 1-based item position, including folded items
	// guide is a tree row's ancestor connectors, prepared off the update loop.
	guide                      string
	depth, parent              int
	branch, collapsed, context bool
}
type prepared struct {
	rows   []screenRow
	starts []int
	// open holds the header rows of groups that show items, so J and K can
	// skip folded groups without scanning them.
	open            []int
	pending, failed int
	items           int
	widths          widths
}
type snapshotMsg struct {
	snapshot *engine.Snapshot
	sources  []sourceBadge
	syncing  bool
}
type projectionMsg engine.Projection
type foldedMsg struct {
	serial uint64
	data   prepared
	// selected is the anchor's new index, or -1. It applies only while the
	// cursor still sits at from, so moves made during preparation win.
	selected, from int
}
type viewDue uint64

// Immutable fold requests avoid copying a backlog-sized map on Update.
type foldState struct {
	group    string
	previous *foldState
}

type Model struct {
	baseContext              context.Context
	claimExpiry              time.Time
	claimTimerAt             time.Time
	width, height            int
	opts                     Options
	theme                    theme
	fg, bg                   color.Color
	query                    string
	shown                    query.Query // parsed query of the displayed rows
	editor                   queryEditor
	queryError, queryWarning string
	highlights               []string
	foldAll                  bool
	ungrouped                bool
	selected, top, view      int
	count                    int
	prefix                   string
	serial                   uint64
	inputSerial              uint64
	groups                   []engine.RowGroup
	folded                   *foldState
	data                     prepared
	loading                  bool
	failure                  string
	sources                  []sourceBadge
	syncing                  bool
	snapshots                <-chan *engine.Snapshot
	snapshot                 *engine.Snapshot
	request                  uint64
	applied, prepared        uint64 // latest projection and preparation shown
	detailOpen               bool
	detailFocus              bool
	detailTab                detailTab
	detailExpanded           bool
	detailPending            bool
	detailLoading            bool
	detailTruncated          bool
	detailLines              []string
	detailCriteria           []int // detailLines index of each criterion
	detailCounts             tabCounts
	detailError              string
	detailScroll             int
	criteriaCursor           int
	detailGeneration         uint64
	detailIdentity           detailIdentity
	detailWorker             *detailWorker
	views                    []viewState
	restore                  *selection
	splitWidth               int
	save                     savePrompt
	notice                   string
	picker                   pickerState
	pickerSerial             int
	marks                    *markSet
	visual                   bool
	visualAnchor             int
	lastEdit                 *editAction
	archiveConfirm           bool
	actionConfirm            *engine.ActionChoice
	actionSerial             uint64
	actionCatalogSerial      uint64
	capture                  captureState
	longEdit                 longEditState
	board                    boardState
	keymap                   []action
	keyError                 error
	keyPrefix                string
	launcher                 launcherState
	launcherSerial           uint64
	goToSession              uint64
	goToWorker               *goToWorker
	goToSelection            *engine.Row
	mouse                    mouseState
}

// Next is a ranked list, so it is never grouped. Board hides drafts, which are
// never ready to work; fb toggles them.
var viewNames = []string{"Next", "Active", "Mine", "Open", "Board"}
var viewQueries = []string{"is:ready sort:next", "category:active group:status", "assignee:me is:open group:status", "is:open group:status", "is:open " + hideDrafts + " group:status"}

// hideDrafts excludes drafts: items in a backlog-category status (design.md D29).
const hideDrafts = "-category:backlog"

func New(opts Options) Model {
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	model := Model{baseContext: opts.Context, width: 80, height: 24, opts: opts, folded: nil, loading: opts.Store != nil, query: viewQueries[0], marks: newMarkSet()}
	model.theme = newTheme(opts.NoColor, opts.HighContrast, nil, nil)
	if opts.Read != nil {
		render := renderMarkdown
		if custom := opts.Render; custom != nil {
			render = func(body string, width int, _ string) (string, error) { return custom(body, width) }
		}
		model.detailWorker = newDetailWorker(opts.Context, opts.Read, render)
	}
	model.initViews()
	model.shown, _ = query.Parse(model.query, query.Options{})
	model.notice = opts.Warning
	var configured []project.ActionConfig
	if opts.Automation != nil {
		configured = opts.Automation.Actions
	}
	model.keymap, model.keyError = buildKeymap(opts.Keys, configured...)
	if opts.GoToItems != nil {
		model.goToWorker = newGoToWorker(opts.Context, opts.GoToItems)
	}
	return model
}

// Bind subscribes before the program starts. The caller owns unsubscribe and
// closes the engine after Run, never on Bubble Tea's update loop.
func (m *Model) Bind(ch <-chan *engine.Snapshot) { m.snapshots = ch }
func (m Model) Init() tea.Cmd {
	var commands []tea.Cmd
	commands = append(commands, m.claimPoll())
	if !m.opts.NoColor {
		// Neutral shades are mixed from the terminal's own colors once known.
		commands = append(commands, tea.RequestBackgroundColor, tea.RequestForegroundColor)
	}
	if m.opts.Store != nil {
		commands = append(commands, m.waitSnapshot(), m.waitProjection())
	}
	if m.goToWorker != nil {
		commands = append(commands, m.waitGoTo())
	}
	if m.detailWorker != nil {
		m.detailWorker.start()
		commands = append(commands, m.waitDetail())
	}
	return tea.Batch(commands...)
}
func (m Model) waitSnapshot() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.opts.Context.Done():
			return nil
		case s := <-m.snapshots:
			sources := make([]sourceBadge, 0, len(m.opts.Sources))
			syncing := false
			for _, id := range m.opts.Sources {
				status := s.Source(id)
				badge := sourceBadge{id: id, state: "synced"}
				switch {
				case status.Error != "":
					badge.state, badge.err = "error", status.Error
				case status.Coverage == "unknown":
					badge.state = "syncing"
					syncing = true
				case status.Coverage != "complete":
					badge.state = "partial"
				}
				sources = append(sources, badge)
			}
			return snapshotMsg{s, sources, syncing}
		}
	}
}
func (m Model) waitProjection() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.opts.Context.Done():
			return nil
		case p := <-m.opts.Projector.Results():
			return projectionMsg(p)
		}
	}
}
func (m Model) waitDetail() tea.Cmd {
	if m.detailWorker == nil {
		return nil
	}
	worker := m.detailWorker
	return func() tea.Msg {
		select {
		case <-worker.ctx.Done():
			return nil
		case result := <-worker.results:
			return detailResultMsg(result)
		}
	}
}
func (m Model) selectedRow() (engine.Row, bool) {
	if m.goToSelection != nil && m.detailOpen {
		return *m.goToSelection, true
	}
	if m.selected < 0 || m.selected >= len(m.data.rows) {
		return engine.Row{}, false
	}
	row := m.data.rows[m.selected]
	return row.row, row.header == ""
}
func (m Model) detailWidth() int {
	if m.width >= 120 {
		listWidth := m.listWidth()
		return max(1, m.width-listWidth-1)
	}
	return max(1, m.width)
}
func (m *Model) scheduleDetail() tea.Cmd {
	m.routeSelectedProject()
	m.detailGeneration++
	row, ok := m.selectedRow()
	m.detailIdentity = identityFor(row, ok)
	m.detailLines = nil
	m.detailCriteria = nil
	m.detailCounts = tabCounts{}
	m.detailScroll = 0
	m.detailTruncated = false
	m.detailError = ""
	m.detailLoading = m.detailOpen && ok
	if !m.detailOpen {
		return nil
	}
	if !m.detailPending {
		m.detailPending = true
		generation := m.detailGeneration
		return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return detailDue(generation) })
	}
	return nil
}
func (m *Model) submitDetail() {
	row, ok := m.selectedRow()
	if !ok {
		m.detailLoading = false
		m.detailError = "Select an item to view details."
		return
	}
	if m.detailWorker == nil {
		m.detailLoading = false
		m.detailError = "detail reading is unavailable"
		return
	}
	m.detailLoading = true
	m.detailWorker.submit(m.detailRequest(row))
}

func (m Model) detailRequest(row engine.Row) detailRequest {
	return detailRequest{
		generation: m.detailGeneration, row: row, width: max(1, m.detailWidth()-2*detailPad),
		tab: m.detailTab, expanded: m.detailExpanded, milestones: m.opts.Milestones,
		statuses: m.opts.Statuses, theme: m.theme, now: m.now(),
	}
}

// Flattening and folding are commands: even a single giant group cannot put
// backlog-sized work on Update. Each result is fenced by the model's serial.
func (m Model) prepare() tea.Cmd {
	groups, folded, serial, from := m.groups, m.folded, m.serial, m.selected
	foldAll, ungrouped := m.foldAll, m.ungrouped
	board, statusGroup := m.isBoard(), m.boardStatusGroup()
	tree, snapshot := m.isTree(), m.snapshot
	groupKey, statuses := m.shown.Group, m.opts.Statuses
	if groupKey == "" && !ungrouped {
		groupKey = "status"
	}
	if board && statusGroup {
		groupKey = "status"
	}
	empty := boardEmpty(m.shown, statuses)
	var anchor screenRow
	if m.loading || from >= len(m.data.rows) {
		from = -1 // a new view starts at the top
	} else {
		anchor = m.data.rows[from]
	}
	var restore *selection
	if m.restore != nil {
		copy := *m.restore
		restore = &copy
		from = m.selected
	}
	return func() tea.Msg {
		states := map[string]bool{}
		if foldAll {
			for _, g := range groups {
				states[g.Value] = true
			}
		}
		for f := folded; f != nil; f = f.previous {
			states[f.group] = !states[f.group]
		}
		if board {
			groups = boardGroups(groups, statuses, statusGroup, empty)
			states = nil // board columns never fold
		}
		data := prepareGroupedRows(groups, states, board || !ungrouped, groupKey, statuses)
		if tree {
			data = prepareTree(groups, snapshot, folded, foldAll)
		}
		data.widths = measure(data.rows, statusNames(statuses))
		selected := -1
		for i, r := range data.rows {
			matches := (board || tree || r.value == anchor.value) && r.row.Ref == anchor.row.Ref && (r.header == "") == (anchor.header == "")
			if board && anchor.header != "" {
				matches = r.value == anchor.value && r.header != ""
			}
			if restore != nil {
				matches = (restore.Header && r.header != "" && r.value == restore.Group) || (!restore.Header && r.header == "" && r.row.Ref == restore.Ref)
			}
			if matches {
				selected = i
				break
			}
		}
		return foldedMsg{serial, data, selected, from}
	}
}
func prepareRows(groups []engine.RowGroup, folded map[string]bool) prepared {
	p := prepareGroupedRows(groups, folded, true, "status", nil)
	p.widths = measure(p.rows, nil)
	return p
}
func prepareGroupedRows(groups []engine.RowGroup, folded map[string]bool, grouped bool, groupKey string, statuses []project.StatusConfig) prepared {
	p := prepared{}
	categories := make(map[string]model.StatusCategory, len(statuses))
	for _, s := range statuses {
		categories[s.Key] = model.StatusCategory(s.Category)
	}
	for g, group := range groups {
		p.starts = append(p.starts, len(p.rows))
		if grouped {
			label := textutil.Line(group.Value)
			if label == "" {
				label = "None"
			}
			header := screenRow{group: g, value: group.Value, header: label, groupKey: groupKey, count: len(group.Rows), collapsed: folded[group.Value], ordinal: p.items + min(1, len(group.Rows))}
			if groupKey == "status" {
				header.category = categories[group.Value]
				if len(group.Rows) > 0 {
					header.category = group.Rows[0].Category
				}
			}
			p.rows = append(p.rows, header)
		}
		for _, row := range group.Rows {
			p.items++
			if row.Pending {
				p.pending++
			}
			if row.Error != "" {
				p.failed++
			}
			if !grouped || !folded[group.Value] {
				p.rows = append(p.rows, screenRow{row: row, group: g, value: group.Value, ordinal: p.items})
			}
		}
		if grouped && !folded[group.Value] && len(group.Rows) > 0 {
			p.open = append(p.open, p.starts[g])
		}
	}
	return p
}

// nextItem is the nearest item row after (or before) row from. A header row is
// followed by its group's items, so past a header it searches the open groups.
func (p prepared) nextItem(from int, forward bool) (int, bool) {
	step := 1
	if !forward {
		step = -1
	}
	i := from + step
	if i < 0 || i >= len(p.rows) {
		return 0, false
	}
	if p.rows[i].header == "" {
		return i, true
	}
	g := sort.SearchInts(p.open, i)
	if forward {
		if g == len(p.open) {
			return 0, false
		}
		return p.open[g] + 1, true
	}
	if g == 0 {
		return 0, false
	}
	// The last item of an open group sits just before the next group's header.
	end := len(p.rows)
	if next := sort.SearchInts(p.starts, p.open[g-1]+1); next < len(p.starts) {
		end = p.starts[next]
	}
	return end - 1, true
}
func (m *Model) project() {
	if m.snapshot != nil && m.opts.Projector != nil {
		if m.hasWorkspace() {
			text, field, prefix := m.query, "", ""
			if m.editor.open {
				text = m.editor.text
				field, prefix, _, _ = m.editor.completion()
			}
			m.request = m.opts.Projector.RequestScope(m.snapshot, text, m.scopeProject(), field, prefix, false)
			return
		}
		if m.editor.open {
			field, prefix, _, _ := m.editor.completion()
			m.request = m.opts.Projector.RequestEditor(m.snapshot, m.editor.text, field, prefix)
		} else {
			m.request = m.opts.Projector.Request(m.snapshot, m.query)
		}
	}
}
func (m *Model) switchView(n int) {
	if n < 0 || n >= len(m.views) {
		return
	}
	m.captureView()
	m.serial++
	m.view = n
	m.goToSelection = nil
	m.query = m.views[n].Config.Query
	m.restore = &m.views[n].Selection
	m.editor = queryEditor{}
	m.queryError = ""
	m.queryWarning = ""
	m.highlights = nil
	m.foldAll = m.views[n].foldAll
	m.selected = 0
	m.top = 0
	m.board = boardState{}
	m.loading = true
	m.folded = m.views[n].folded
	m.project()
}

// NoticeMsg shows a background warning in the status line. Writing it to the
// terminal instead would corrupt the alternate screen.
type NoticeMsg string

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case NoticeMsg:
		m.notice = boundedLine(string(msg))
		return m, nil
	case goToResult:
		return m.receiveGoTo(msg)
	case launcherFiltered:
		if m.launcher.open && msg.serial == m.launcherSerial {
			m.launcher.rows, m.launcher.cursor, m.launcher.loading = msg.rows, 0, false
		}
		return m, nil
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.BackgroundColorMsg:
		m.bg = msg.Color
		return m, m.retheme()
	case tea.ForegroundColorMsg:
		m.fg = msg.Color
		return m, m.retheme()
	case tea.WindowSizeMsg:
		oldDetailWidth := m.detailWidth()
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		if m.width < 120 {
			m.detailFocus = m.detailOpen // the detail pane replaces the list
		}
		if m.detailOpen && m.detailWidth() != oldDetailWidth {
			return m, m.scheduleDetail()
		}
	case claimsPoll:
		m.refreshClaims()
		return m, m.claimPoll()
	case claimsExpiry:
		if time.Time(msg).Equal(m.claimTimerAt) {
			m.claimTimerAt = time.Time{}
		}
		if time.Time(msg).Equal(m.claimExpiry) {
			m.project()
		}
		return m, nil
	case claimOperationMsg:
		m.notice = textutil.Line(msg.message)
		m.refreshClaims()
		return m, nil
	case snapshotMsg:
		m.snapshot, m.sources, m.syncing = msg.snapshot, msg.sources, msg.syncing
		m.project()
		return m, m.waitSnapshot()
	case projectionMsg:
		if msg.Request != m.request {
			return m, m.waitProjection()
		}
		m.applied = msg.Request
		m.claimExpiry = msg.NextClaimExpiry
		if m.editor.open {
			m.editor.candidates = msg.Completions
			m.editor.candidate = 0
			m.editor.ready = m.request
		}
		if msg.Error != nil {
			m.queryError = textutil.Line(msg.Error.Error())
			m.loading = false
			// Nothing is in flight: the last valid rows stay, without a spinner.
			m.prepared = m.serial
			return m, m.waitProjection()
		}
		m.queryError = ""
		m.queryWarning = ""
		if msg.Warning != nil {
			m.queryWarning = textutil.Line(msg.Warning.Error())
		}
		text := m.query
		if m.editor.open {
			text = m.editor.text
		}
		parsed, _ := query.Parse(text, query.Options{})
		m.shown = parsed
		m.highlights = nil
		for _, term := range parsed.Terms {
			if term.Field == "" && !term.Negated {
				for _, value := range term.Values {
					m.highlights = append(m.highlights, value.Text)
				}
			}
		}
		m.failure = ""
		m.groups = msg.Groups
		m.ungrouped = msg.Ungrouped
		m.serial++
		expiry := m.expiryCommand()
		return m, tea.Batch(m.prepare(), m.waitProjection(), expiry)
	case viewSaved:
		m.saved(msg)
		return m, nil
	case foldedMsg:
		if msg.serial != m.serial {
			return m, nil
		}
		m.data = msg.data
		m.prepared = msg.serial
		m.loading = false
		// The store can publish its empty loading snapshot before source data.
		// Keep the restart anchor until rows exist to resolve it against.
		if m.data.items > 0 {
			m.restore = nil
		}
		if msg.selected >= 0 && m.selected == msg.from {
			m.selected = msg.selected
		}
		m.selected = max(0, min(m.selected, len(m.data.rows)-1))
		m.keepBoardVisible()
		if m.detailOpen && identityForSelected(m) != m.detailIdentity {
			return m, m.scheduleDetail()
		}
	case viewDue:
		if uint64(msg) == m.inputSerial && m.count > 0 && m.prefix == "" {
			m.switchView(m.count - 1)
			m.count = 0
		}
	case detailDue:
		if !m.detailOpen {
			m.detailPending = false
			return m, nil
		}
		if uint64(msg) != m.detailGeneration {
			generation := m.detailGeneration
			return m, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return detailDue(generation) })
		}
		m.detailPending = false
		m.submitDetail()
		return m, nil
	case detailResultMsg:
		result := detailResult(msg)
		if m.detailOpen && result.generation == m.detailGeneration && result.identity.Ref == m.detailIdentity.Ref {
			m.detailLoading = false
			m.detailError = result.err
			m.detailLines = result.lines
			m.detailCriteria = result.criteria
			m.detailCounts = result.counts
			m.detailTruncated = result.truncated
			m.detailScroll = 0
			m.keepCriterionVisible()
		}
		return m, m.waitDetail()
	case pickerChoicesMsg:
		if m.picker.open && msg.generation == m.picker.generation {
			updatePickerChoices(&m.picker, msg)
			if m.picker.query != "" {
				return m, m.filterPicker()
			}
		}
		return m, nil
	case pickerFilterMsg:
		if m.picker.open && msg.generation == m.picker.generation && msg.query == m.picker.query {
			m.picker.filtered = msg.choices
			m.picker.loading = false
			m.picker.cursor = msg.cursor
		}
		return m, nil
	case actionChoicesMsg:
		return m.receiveActionChoices(msg)
	case actionPreparedMsg:
		return m.receiveActionPrepared(msg)
	case actionFinishedMsg:
		if msg.err != nil {
			m.notice = "Action: " + boundedLine(msg.err.Error())
		} else {
			m.notice = msg.message
		}
		return m, nil
	case editPreparedMsg:
		return m.editorPrepared(msg)
	case editFinishedMsg:
		return m.editorFinished(msg)
	case editSavedMsg:
		return m.editorSaved(msg)
	case editResultMsg:
		m.editResult(msg)
		m.refreshClaims()
		return m, nil
	case openResultMsg:
		m.notice = msg.err
		return m, nil
	case tea.KeyPressMsg:
		// Any new input supersedes action preparation still running on a worker.
		// In particular, Enter in a newer modal must never confirm an older action.
		m.actionSerial++
		// A notice lasts until the next key, then the query shows again.
		m.notice = ""
		s := keyString(msg)
		if code, ok := emacsInputKeys[s]; ok && m.modalOpen() {
			msg = tea.KeyPressMsg{Code: code}
			s = msg.String()
		}
		if s == "ctrl+c" && (m.longEdit.choice != "" || m.capture.open || m.picker.open || m.archiveConfirm || m.actionConfirm != nil || m.save.open || m.editor.open) {
			return m, tea.Quit
		}
		if m.actionConfirm != nil {
			switch s {
			case "y", "enter":
				choice := *m.actionConfirm
				m.actionConfirm = nil
				return m, m.runConfiguredAction(choice)
			case "n", "esc":
				m.actionConfirm = nil
			}
			return m, nil
		}
		if m.launcher.open {
			return m.updateLauncher(s, msg.Text)
		}
		if m.longEdit.choice != "" {
			return m, m.updateLongEdit(s)
		}
		if m.capture.open {
			return m, m.updateCapture(s)
		}
		if m.picker.open {
			return m, m.updatePickerKey(s)
		}
		if m.archiveConfirm {
			switch s {
			case "esc", "n":
				m.archiveConfirm = false
				return m, nil
			case "enter", "y":
				return m, m.confirmArchive()
			default:
				return m, nil
			}
		}
		if m.save.open {
			return m.updateSave(msg)
		}
		if m.editor.open {
			return m.updateQuery(msg)
		}
		return m.dispatchKey(s)
	}
	m.keepBoardVisible()
	m.keepSelectionVisible()
	return m, nil
}

// Prompts, pickers and menus own their keys, so the keymap's Emacs aliases
// don't reach them; these translate to the keys every input handles.
var emacsInputKeys = map[string]rune{"ctrl+g": tea.KeyEscape, "ctrl+n": tea.KeyDown, "ctrl+p": tea.KeyUp}

// modalOpen reports an input or prompt that takes every key and the mouse.
func (m Model) modalOpen() bool {
	return m.launcher.open || m.picker.open || m.capture.open || m.longEdit.choice != "" || m.editor.open || m.save.open || m.archiveConfirm || m.actionConfirm != nil
}

// retheme rebuilds the palette once the terminal reports its colors. Rendered
// detail lines embed theme colors, so an open detail is prepared again.
func (m *Model) retheme() tea.Cmd {
	previous := m.theme.key()
	m.theme = newTheme(m.opts.NoColor, m.opts.HighContrast, m.fg, m.bg)
	if m.detailOpen && m.theme.key() != previous {
		return m.scheduleDetail()
	}
	return nil
}

// keepSelectionVisible scrolls the list viewport to the cursor.
func (m *Model) keepSelectionVisible() {
	page := m.page()
	if m.selected < m.top {
		m.top = m.selected
	}
	if m.selected >= m.top+page {
		m.top = m.selected - page + 1
	}
	m.top = max(0, min(m.top, max(0, len(m.data.rows)-page)))
}

// screenOffset is the visible row H, M or L selects. A count N selects the Nth
// row from the top (H) or bottom (L), as in Vim.
func screenOffset(key string, visible, n int) int {
	visible = max(1, visible)
	switch key {
	case "H":
		return min(n, visible) - 1
	case "L":
		return max(0, visible-n)
	}
	return (visible - 1) / 2
}

// canonicalKey keeps the established contextual action semantics in one place.
func (m Model) canonicalKey(s string) (tea.Model, tea.Cmd) {
	if !m.detailOpen {
		m.goToSelection = nil
	}
	if s == "pagedown" {
		s = "ctrl+d"
	}
	if s == "pageup" {
		s = "ctrl+u"
	}
	if s == "ctrl+s" {
		m.save = savePrompt{open: true, name: m.views[m.view].Config.Name}
		m.notice = ""
		return m, nil
	}
	if m.detailOpen && m.width >= 120 && (s == "ctrl+left" || s == "ctrl+right") {
		delta := 5
		if s == "ctrl+left" {
			delta = -5
		}
		m.splitWidth = max(30, min(m.listWidth()+delta, m.width-31))
		return m, m.scheduleDetail()
	}
	if m.prefix == "g" && (s == "t" || s == "T") {
		m.prefix = ""
		m.count = 0
		m.inputSerial++
		if s == "t" {
			m.switchView((m.view + 1) % len(m.views))
		} else {
			m.switchView((m.view + len(m.views) - 1) % len(m.views))
		}
		return m, nil
	}
	if m.prefix == "y" && s != "esc" {
		m.prefix = ""
		m.count = 0
		m.inputSerial++
		return m, m.yank(s)
	}
	if m.prefix == "d" && s != "esc" {
		m.prefix = ""
		m.count = 0
		m.inputSerial++
		if s == "d" {
			m.archivePrompt()
		}
		return m, nil
	}
	if (m.prefix == "f" || m.prefix == "o" || m.prefix == "z") && s != "esc" {
		prefix := m.prefix
		m.prefix = ""
		m.count = 0
		m.inputSerial++
		_, cmd := m.queryMenu(prefix, s)
		return m, cmd
	}
	if !m.detailFocus {
		switch s {
		case "/":
			m.editQuery(m.query, false)
			return m, nil
		case "f", "o", "z":
			m.prefix = s
			m.count = 0
			m.inputSerial++
			return m, nil
		case "B", "T":
			m.count = 0
			m.inputSerial++
			return m, m.toggleLayout(map[string]string{"B": "board", "T": "tree"}[s])
		case "F":
			m.query = clearQuery(m.query)
			m.queryError = ""
			m.queryWarning = ""
			m.serial++
			m.project()
			return m, nil
		}
	}
	if s == "J" || s == "K" {
		return m, m.stepItem(s == "J")
	}
	if s == "esc" {
		if m.detailOpen {
			m.closeDetail()
			return m, nil
		}
		m.count = 0
		m.prefix = ""
		m.inputSerial++
		m.visual = false
		if m.marks != nil && m.marks.size() > 0 {
			m.marks.clear()
			m.notice = "Marks cleared."
		}
		return m, nil
	}
	if m.detailOpen && m.detailFocus {
		switch s {
		case "h", "left":
			if !m.split() {
				m.closeDetail()
				return m, nil
			}
			m.detailFocus = false
			if m.goToSelection != nil {
				m.goToSelection = nil
				return m, m.scheduleDetail()
			}
			return m, nil
		case "q", "enter":
			m.closeDetail()
			return m, nil
		case "tab":
			m.detailTab = (m.detailTab + 1) % detailTab(len(detailTabNames))
			m.detailExpanded = false
			if m.detailTab == tabCriteria {
				m.criteriaCursor = 0
			}
			return m, m.scheduleDetail()
		case "shift+tab":
			m.detailTab = (m.detailTab + detailTab(len(detailTabNames)) - 1) % detailTab(len(detailTabNames))
			m.detailExpanded = false
			if m.detailTab == tabCriteria {
				m.criteriaCursor = 0
			}
			return m, m.scheduleDetail()
		case "m":
			if m.detailTruncated || m.detailExpanded {
				m.detailExpanded = !m.detailExpanded
				return m, m.scheduleDetail()
			}
			return m, nil
		case "x", "space", " ":
			if m.detailTab == tabCriteria {
				return m, m.toggleCriterion()
			}
		case "j", "down":
			if m.detailTab == tabCriteria {
				if count := m.criterionCount(); count > 0 {
					m.criteriaCursor = min(count-1, m.criteriaCursor+1)
					m.keepCriterionVisible()
				}
				return m, nil
			}
			m.scrollDetail(1)
		case "k", "up":
			if m.detailTab == tabCriteria {
				m.criteriaCursor = max(0, m.criteriaCursor-1)
				m.keepCriterionVisible()
				return m, nil
			}
			m.scrollDetail(-1)
		case "ctrl+d", "pagedown":
			m.scrollDetail(max(1, m.detailVisible()/2))
		case "ctrl+u", "pageup":
			m.scrollDetail(-max(1, m.detailVisible()/2))
		case "ctrl+f":
			m.scrollDetail(m.detailVisible())
		case "ctrl+b":
			m.scrollDetail(-m.detailVisible())
		case "g":
			if m.prefix == "g" {
				m.prefix = ""
				m.detailScroll = 0
				m.criteriaCursor = 0
			} else {
				m.prefix = "g"
			}
		case "G":
			m.scrollDetail(len(m.detailLines))
			m.criteriaCursor = max(0, len(m.detailCriteria)-1)
		}
		return m, nil
	}
	// enter, l and right open detail from a list; they never close it.
	openDetail := s == "enter" || !m.isTree() && !m.isBoard() && (s == "l" || s == "right")
	if openDetail {
		if _, ok := m.selectedRow(); !ok && !m.detailOpen {
			// A group header has no item to open; enter folds it.
			if s == "enter" && m.selected < len(m.data.rows) && m.data.rows[m.selected].header != "" {
				return m, m.fold("fold")
			}
			return m, nil
		}
		opened := !m.detailOpen
		m.detailOpen, m.detailFocus = true, true
		if opened {
			return m, m.scheduleDetail()
		}
		return m, nil
	}
	if m.detailOpen && !m.isTree() && !m.isBoard() && (s == "h" || s == "left") {
		m.closeDetail()
		return m, nil
	}
	if s == "tab" || s == "shift+tab" {
		if !m.detailOpen {
			m.detailOpen = true
			m.detailFocus = true
			m.detailTab = tabOverview
			return m, m.scheduleDetail()
		}
		m.detailFocus = true
		return m, nil
	}
	if s == "q" {
		return m, tea.Quit
	}
	if !m.detailFocus {
		if s == "y" || s == "d" {
			m.prefix = s
			m.inputSerial++
			return m, nil
		}
		if s == "space" || s == " " {
			m.markCurrent()
			return m, nil
		}
		if s == "V" {
			m.visual = !m.visual
			if m.visual {
				m.visualAnchor = m.selected
			}
			return m, nil
		}
		if s == "ctrl+a" {
			return m, m.markAll()
		}
		if m.isTree() && (s == "h" || s == "l" || s == "left" || s == "right") {
			m.count = 0
			m.inputSerial++
			cmd := m.navigateTree(s)
			m.keepSelectionVisible()
			return m, cmd
		}
		if m.isBoard() && (s == "h" || s == "l" || s == "left" || s == "right") {
			previous := m.selected
			m.navigateBoard(s, max(1, m.count), m.count > 0, "")
			m.count = 0
			m.inputSerial++
			if m.detailOpen && m.selected != previous {
				return m, m.scheduleDetail()
			}
			return m, nil
		}
		if cmd, handled := m.updateEditKey(s); handled {
			return m, cmd
		}
	}
	if len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
		m.count = min(1000000, m.count*10+int(s[0]-'0'))
		m.inputSerial++
		serial := m.inputSerial
		return m, tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg { return viewDue(serial) })
	}
	previousSelection := m.selected
	n := max(1, m.count)
	hadCount := m.count > 0
	m.count = 0
	m.inputSerial++
	prefix := m.prefix
	m.prefix = ""
	page := m.page()
	if m.isBoard() && m.navigateBoard(s, n, hadCount, prefix) {
		if m.detailOpen && m.selected != previousSelection {
			return m, m.scheduleDetail()
		}
		return m, nil
	}
	switch {
	case prefix == "g" && s == "g", s == "home":
		m.selected = 0
	case prefix == "g" && s == "t":
		m.switchView((m.view + 1) % len(m.views))
	case prefix == "g" && s == "T":
		m.switchView((m.view + len(m.views) - 1) % len(m.views))
	case s == "g":
		m.prefix = s
		m.count = n
		if !hadCount {
			m.count = 0
		}
	case s == "j" || s == "down":
		m.selected += n
	case s == "k" || s == "up":
		m.selected -= n
	case s == "end":
		m.selected = len(m.data.rows) - 1
	case s == "G":
		if hadCount {
			m.selected = n - 1
		} else {
			m.selected = len(m.data.rows) - 1
		}
	case s == "H" || s == "M" || s == "L":
		m.selected = m.top + screenOffset(s, min(page, len(m.data.rows)-m.top), n)
	case s == "ctrl+d":
		m.selected += max(1, page/2) * n
	case s == "ctrl+u":
		m.selected -= max(1, page/2) * n
	case s == "ctrl+f":
		m.selected += page * n
	case s == "ctrl+b":
		m.selected -= page * n
	case s == "]":
		g := sort.SearchInts(m.data.starts, m.selected+1)
		if g < len(m.data.starts) {
			m.selected = m.data.starts[g]
		}
	case s == "[":
		g := sort.SearchInts(m.data.starts, m.selected) - 1
		if g >= 0 {
			m.selected = m.data.starts[g]
		}
	case s == "R":
		if m.opts.Store != nil {
			m.opts.Store.Refresh()
		}
	}
	m.selected = max(0, min(m.selected, len(m.data.rows)-1))
	m.keepSelectionVisible()
	if m.detailOpen && m.selected != previousSelection {
		return m, m.scheduleDetail()
	}
	m.keepBoardVisible()
	m.keepSelectionVisible()
	return m, nil
}

// toggleLayout switches the current view between list and layout. The view's
// session state keeps the choice.
func (m *Model) toggleLayout(layout string) tea.Cmd {
	v := &m.views[m.view].Config
	if v.Layout == layout {
		v.Layout = "list"
	} else {
		v.Layout = layout
	}
	m.board = boardState{}
	m.serial++
	return m.prepare()
}

func (m *Model) closeDetail() {
	m.detailOpen = false
	m.detailFocus = false
	m.detailGeneration++
	m.detailLoading = false
	m.detailLines = nil
	m.detailCriteria = nil
}

// stepItem moves to the next or previous item, skipping group headers, without
// changing focus, so detail can page through items in place.
func (m *Model) stepItem(forward bool) tea.Cmd {
	m.count = 0
	m.inputSerial++
	if m.goToSelection != nil {
		return nil
	}
	previous := m.selected
	if m.isBoard() {
		key := "k"
		if forward {
			key = "j"
		}
		m.navigateBoard(key, 1, false, "")
	} else {
		if i, ok := m.data.nextItem(m.selected, forward); ok {
			m.selected = i
		}
		m.keepSelectionVisible()
	}
	if m.detailOpen && m.selected != previous {
		return m.scheduleDetail()
	}
	return nil
}
func identityForSelected(m Model) detailIdentity {
	row, ok := m.selectedRow()
	return identityFor(row, ok)
}
func (m *Model) scrollDetail(delta int) {
	m.detailScroll = max(0, min(m.detailScroll+delta, max(0, len(m.detailLines)-m.detailVisible())))
}
