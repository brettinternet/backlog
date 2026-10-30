// Package tui adapts immutable engine projections to a viewport-only UI.
package tui

import (
	"context"
	"image/color"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/history"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/textutil"
)

type Options struct {
	Workspace                  *project.Config
	WorkspaceScope             bool
	Automation                 *project.Config
	HighContrast, NoColor      bool
	Project                    string // display name
	Store                      *engine.Store
	Projector                  *engine.Projector
	Changes                    *changes.Tracker // nil when no project reads changes
	Context                    context.Context
	Sources                    []string
	Statuses                   []project.StatusConfig
	Labels                     []string
	Milestones                 []project.Milestone
	Me                         map[string]string // source ID to the caller's assignee value there
	CreateSource, CreateStatus string
	ValidateItem               func(model.Item) error
	ResolveRef                 func(context.Context, string, model.Ref) (model.Ref, error)
	NativeBytes                func(context.Context, model.Item) ([]byte, error)
	NativeSource               func(string) bool
	EditorDir                  string
	OpenURL                    func(context.Context, string) error
	Read                       func(context.Context, model.Ref) (model.Item, error)
	History                    func(context.Context, model.Item, []changes.Change) history.Result
	HistoryPosition            func(model.Ref) string // changes when the item's event log grows
	Render                     func(string, int) (string, error)
	Views                      []project.SavedView
	Filter                     string // ANDed with every tab's declared query
	View                       string // starting tab; overrides the restored one
	Session                    *Session
	Warning                    string
	TrustWarning               string
	SaveView                   func(project.ViewConfig, bool) (project.SavedView, error)
	Keys                       map[string]string
	GoToItems                  func(context.Context) ([]GoToItem, error)
	Now                        func() time.Time
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
	snapshot   *engine.Snapshot
	sources    []sourceBadge
	syncing    bool
	refreshing bool
}
type projectionMsg engine.Projection
type changesMsg *changes.State
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
	mineClaims               map[engine.ClaimKey]time.Time
	renewTimerAt             time.Time
	renewing                 bool
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
	loads                    [loadKinds]indicator
	held                     *heldFrame
	failure                  string
	sources                  []sourceBadge
	syncing                  bool
	refreshing               bool // a source load is running; only the status bar shows it
	snapshots                <-chan *engine.Snapshot
	snapshot                 *engine.Snapshot
	changes                  *changes.State // linked into each projection
	request                  uint64
	applied, prepared        uint64 // latest projection and preparation shown
	detailOpen               bool
	detailFocus              bool
	detailTab                detailTab
	detailExpanded           bool
	detailPending            bool
	detailLoading            bool
	detailMasked             bool   // the loading placeholder covers the body
	detailSubmitted          uint64 // the last generation sent to the worker
	detailTruncated          bool
	detailFiles, detailHunks []int     // the Diff tab's file and hunk header lines
	detailRows               []diffRow // what each Diff entry shows
	detailHead               string    // the commit the Diff shows
	detailDirty              []string  // the Diff's uncommitted files
	diffOrigin, diffCursor   int       // the scroll and cursor before [ or ], which [c and ]c restore
	detailLines              []string  // the last body, kept while the next loads
	detailEntries            []int     // detailLines index of each criterion or relation
	detailTargets            []engine.Relation
	detailChanges            []changes.Change
	detailCommits            []changes.Commit
	detailIDs                []string        // the history entry of each History entry
	detailHints              []string        // the key hint of each History entry
	historyOpen              map[string]bool // expanded History entries of historyRef
	historyRef               model.Ref
	actionSession            string              // the History session row ! opened the actions menu on
	actionChange             *hooks.LinkedChange // the Changes entry ! or enter opened the actions menu on
	detailShownRef           model.Ref
	detailShownTab           detailTab
	detailError              string
	detailScroll             int
	entryCursor              int
	detailGeneration         uint64
	detailIdentity           detailIdentity
	detailWorker             *detailWorker
	views                    []viewState
	restore                  *selection
	splitWidth               int
	save                     savePrompt
	find                     findState
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
	itemChoices              map[string]actionChoicesMsg // by choicesKey, the last fetched
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
	// detached is an item detail shows outside the list: a go-to result, a
	// followed relation, or an item focused detail held when it left the view.
	// heldPlace says where a held item was: 1 where the list selection is now,
	// -1 after the last row, 0 nowhere.
	detached      *engine.Row
	heldPlace     int
	detailHistory []detailStop // items followed from, oldest first
	catalog       *engine.Catalog
	mouse         mouseState
	// change is the linked change or commit the change view shows, opened
	// from changeTab of changeRef: the Changes entry at changeCursor, or the
	// History entry changeEntry, found by ID as History rows shift.
	// changeParent is the change view a Commits entry at parentCursor opened
	// change from.
	change       *changes.Change
	changeRef    model.Ref
	changeTab    detailTab
	changeCursor int
	changeEntry  string
	changeParent *changes.Change
	parentCursor int
	reviewer     reviewer
	review       reviewState
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
		model.detailWorker.history, model.detailWorker.position = opts.History, opts.HistoryPosition
		if tracker := opts.Changes; tracker != nil {
			model.detailWorker.changeDetail, model.detailWorker.changeReview, model.detailWorker.enrich = tracker.Detail, tracker.Review, tracker.Enrich
			model.detailWorker.changeDraft = tracker.Draft
		}
	}
	if opts.Changes != nil {
		model.reviewer = opts.Changes
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
	if m.opts.Changes != nil {
		commands = append(commands, m.waitChanges())
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
			syncing, refreshing := false, false
			for _, id := range m.opts.Sources {
				status := s.Source(id)
				refreshing = refreshing || status.Loading
				badge := sourceBadge{id: id, state: "synced"}
				switch {
				case status.Error != "":
					badge.state, badge.err = "error", status.Error
				case status.Coverage == "unknown" || status.Initial || status.SprintsPending:
					badge.state = "syncing"
					syncing = true
				case status.Coverage != "complete":
					badge.state = "partial"
				}
				sources = append(sources, badge)
			}
			return snapshotMsg{s, sources, syncing, refreshing}
		}
	}
}
func (m Model) waitChanges() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.opts.Context.Done():
			return nil
		case <-m.opts.Changes.Updates():
			return changesMsg(m.opts.Changes.Latest())
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

// isMe reports whether assignee is the caller's identity in source.
func (m Model) isMe(source, assignee string) bool {
	me := m.opts.Me[source]
	return me != "" && assignee == me
}

func (m Model) selectedRow() (engine.Row, bool) {
	if m.detached != nil && m.detailOpen {
		return *m.detached, true
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

// scheduleDetail loads the selected item's detail once the selection rests.
// The previous body stays until the new one arrives, or until the load is
// slow enough to show as loading.
func (m *Model) scheduleDetail() tea.Cmd {
	m.routeSelectedProject()
	m.detailGeneration++
	row, ok := m.selectedRow()
	if m.change != nil && (!ok || row.Ref != m.changeRef) {
		m.change, m.changeParent, m.detailTab, m.entryCursor = nil, nil, m.changeTab, 0
	}
	m.detailIdentity = identityFor(row, ok)
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

// showDetail renders another tab or length of the same item at once; only a
// moving selection waits to settle.
func (m *Model) showDetail() tea.Cmd {
	cmd := m.scheduleDetail()
	if m.detailOpen {
		m.submitDetail(false)
	}
	return cmd
}
func (m *Model) submitDetail(fresh bool) {
	m.detailSubmitted = m.detailGeneration
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
	request := m.detailRequest(row)
	request.fresh = fresh
	m.detailWorker.submit(request)
}

func (m Model) detailRequest(row engine.Row) detailRequest {
	var sprints []model.Sprint
	if m.snapshot != nil {
		sprints = m.snapshot.Source(row.Ref.Source).Sprints
	}
	var change *changes.Change
	if m.change != nil {
		shown := m.shownChange()
		change = &shown
	}
	return detailRequest{
		change: change, generation: m.detailGeneration, row: row, width: max(1, m.detailWidth()-2*detailPad),
		tab: m.detailTab, expanded: m.detailExpanded, open: m.historyOpenIDs(row.Ref), milestones: m.opts.Milestones,
		sprints: sprints, statuses: m.opts.Statuses, theme: m.theme, now: m.now(),
	}
}

func (m Model) historyOpenIDs(ref model.Ref) string {
	if ref != m.historyRef {
		return ""
	}
	ids := make([]string, 0, len(m.historyOpen))
	for id, open := range m.historyOpen {
		if open {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return strings.Join(ids, "\n")
}

// toggleMore raises or restores the limit on a cut-off body.
func (m *Model) toggleMore() tea.Cmd {
	m.detailExpanded = !m.detailExpanded
	return m.showDetail()
}

// toggleHistoryEntry expands or collapses the History entry under the cursor.
func (m *Model) toggleHistoryEntry() tea.Cmd {
	if !m.detailCurrent() || m.entryCursor < 0 || m.entryCursor >= len(m.detailIDs) {
		return nil
	}
	if m.historyRef != m.detailIdentity.Ref {
		m.historyRef, m.historyOpen = m.detailIdentity.Ref, map[string]bool{}
	}
	id := m.detailIDs[m.entryCursor]
	m.historyOpen[id] = !m.historyOpen[id]
	return m.showDetail()
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
		selected, moved := -1, -1
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
			// An item that left its group, such as after a status edit, is
			// followed to its new one.
			if moved < 0 && restore == nil && anchor.header == "" && r.header == "" && r.row.Ref == anchor.row.Ref {
				moved = i
			}
		}
		if selected < 0 {
			selected = moved
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
			// A header names its value like its rows: the first provider name wins.
			if groupKey == "status" || groupKey == "assignee" || groupKey == "sprint" {
				for _, row := range group.Rows {
					if name := row.Names[group.Value]; name != "" {
						header.row.Names = map[string]string{group.Value: name}
						break
					}
				}
			}
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
func (m *Model) project() { m.requestProjection(false) }

// requestProjection asks for the rows of the query, or of the draft and its
// completions while the editor is open. Every request carries the draft, so a
// background refresh can't empty the completion list.
func (m *Model) requestProjection(refreshClaims bool) {
	if m.snapshot == nil || m.opts.Projector == nil {
		return
	}
	q := engine.ProjectionQuery{Text: m.query, RefreshClaims: refreshClaims, Changes: m.changes}
	if m.hasWorkspace() {
		q.Project = m.scopeProject()
	}
	if m.editor.open {
		q.Text, q.Complete = m.editor.text, true
		q.Field, q.Prefix, _, _ = m.editor.completion()
	}
	if m.opts.Store != nil && (q.Field == "sprint" || engine.MentionsSprint(q.Text)) {
		m.opts.Store.UseSprints()
	}
	m.request = m.opts.Projector.RequestQuery(m.snapshot, q)
}
func (m *Model) switchView(n int) {
	if n < 0 || n >= len(m.views) {
		return
	}
	if m.held == nil && !m.unsettled() && !m.loads[listLoad].shown {
		m.held = &heldFrame{body: m.body(m.bodyRows()), position: m.position()}
	}
	m.captureView()
	m.serial++
	m.view = n
	m.detached = nil
	m.query = m.views[n].Config.Query
	m.restore = &m.views[n].Selection
	m.editor = queryEditor{}
	m.queryError = "" // a warning stays until the new result replaces it
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

// Update times loading indicators around every change, so no handler has to
// remember to.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	m = next.(Model)
	busy := [loadKinds]bool{listLoad: m.listPending(), statusLoad: m.stale() || m.syncing || m.refreshing, detailLoad: m.detailLoading}
	commands := []tea.Cmd{cmd}
	for kind := range m.loads {
		commands = append(commands, m.loads[kind].track(loadKind(kind), busy[kind]))
	}
	// Reloading the page in view keeps its body; another item's or tab's body
	// gives way. Either choice holds while the indicator shows.
	shown := m.loads[detailLoad].shown
	m.detailMasked = shown && (m.detailMasked || m.detailLoading && (len(m.detailLines) == 0 || m.detailShownRef != m.detailIdentity.Ref || m.detailShownTab != m.detailTab))
	return m, tea.Batch(commands...)
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case indicatorDue:
		return m, m.loads[msg.kind].due(msg)
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
		m.held = nil
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
	case claimsRenew:
		renew := m.renew(time.Time(msg))
		return m, renew
	case claimRenewed:
		// The refreshed claim list reschedules; a failure retries while the
		// lease is live.
		m.renewing = false
		if msg.message != "" {
			m.notice = textutil.Line(msg.message)
		}
		m.refreshClaims()
		return m, nil
	case snapshotMsg:
		// Nothing matched before the sync finished, which proves nothing; the
		// synced rows are loading.
		if m.syncing && !msg.syncing && len(m.data.rows) == 0 {
			m.loading = true
		}
		m.snapshot, m.sources, m.syncing, m.refreshing = msg.snapshot, msg.sources, msg.syncing, msg.refreshing
		m.project()
		return m, m.waitSnapshot()
	case changesMsg:
		m.changes = (*changes.State)(msg)
		m.project()
		return m, m.waitChanges()
	case projectionMsg:
		if msg.Request != m.request {
			return m, m.waitProjection()
		}
		m.applied = msg.Request
		m.claimExpiry, m.mineClaims = msg.NextClaimExpiry, msg.MineClaims
		// While candidates guide the word being typed, its error or unknown
		// value would only flash; enter reports it.
		guided := func(error) bool { return false }
		if m.editor.open {
			m.editor.receive(msg.Completions)
			guided = func(err error) bool { return len(m.editor.candidates) > 0 && m.editor.inWord(err) }
			m.editor.unfinished = guided(msg.Error) || guided(msg.Warning)
		}
		if msg.Error != nil {
			m.queryError = textutil.Line(msg.Error.Error())
			if guided(msg.Error) {
				m.queryError = ""
			}
			m.loading = false
			m.held = nil
			// Nothing is in flight: the last valid rows stay, without a spinner.
			m.prepared = m.serial
			expiry, renew := m.expiryCommand(), m.renewCommand()
			return m, tea.Batch(m.waitProjection(), expiry, renew)
		}
		m.queryError = ""
		m.queryWarning = ""
		if msg.Warning != nil && !guided(msg.Warning) {
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
		m.catalog = msg.Catalog
		m.refreshDetached()
		m.groups = msg.Groups
		m.ungrouped = msg.Ungrouped
		m.serial++
		expiry, renew := m.expiryCommand(), m.renewCommand()
		return m, tea.Batch(m.prepare(), m.waitProjection(), expiry, renew)
	case viewSaved:
		m.saved(msg)
		return m, nil
	case foldedMsg:
		if msg.serial != m.serial {
			return m, nil
		}
		shown, holding := m.selectedRow()
		holding = holding && m.detailOpen && m.detailFocus && m.detached == nil && m.selected == msg.from
		m.data = msg.data
		m.prepared = msg.serial
		m.loading = false
		m.held = nil
		// The store can publish its empty loading snapshot before source data.
		// Keep the restart anchor until rows exist to resolve it against, or
		// until it resolves: an empty board's headers never become items.
		if m.data.items > 0 || msg.selected >= 0 {
			m.restore = nil
		}
		if msg.selected >= 0 && m.selected == msg.from {
			m.selected = msg.selected
		}
		// Focused detail keeps its item when an edit moves it out of the view,
		// so the next key can't act on the neighbor that took its place.
		if holding && msg.selected < 0 && m.catalog != nil {
			if row, ok := m.catalog.Row(shown.Ref); ok {
				m.detached, m.heldPlace, m.detailHistory = &row, 1, nil
				if m.selected >= len(m.data.rows) {
					m.heldPlace = -1
				}
			}
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
		if !m.detailOpen || m.detailSubmitted == m.detailGeneration {
			m.detailPending = false
			return m, nil
		}
		if uint64(msg) != m.detailGeneration {
			generation := m.detailGeneration
			return m, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return detailDue(generation) })
		}
		m.detailPending = false
		m.submitDetail(false)
		return m, nil
	case detailResultMsg:
		result := detailResult(msg)
		if m.detailOpen && result.generation == m.detailGeneration && result.identity.Ref == m.detailIdentity.Ref {
			// A new version or length of the page in view keeps its scroll.
			same := m.detailLines != nil && m.detailShownRef == result.identity.Ref && m.detailShownTab == m.detailTab
			// History read at its end stays there as entries expand or arrive.
			pinned := same && m.detailTab == tabHistory && m.detailScroll >= len(m.detailLines)-m.detailVisible()
			m.detailLoading = false
			m.detailError = result.err
			m.detailLines = result.lines
			// History keeps the cursor on its entry: expanding a group or a new
			// entry shifts the rows, and the newest-first window drops some.
			if same && m.detailTab == tabHistory && m.entryCursor >= 0 && m.entryCursor < len(m.detailIDs) {
				if n := slices.Index(result.ids, m.detailIDs[m.entryCursor]); n >= 0 {
					m.entryCursor = n
				}
			}
			// The Diff keeps the cursor on its line as files expand or the
			// layout changes.
			var place diffPlace
			keep := same && m.detailTab == tabChangeDiff && result.head == m.detailHead && m.detailCurrent()
			if keep {
				place = placeOf(m.detailRows, m.entryCursor)
			}
			m.detailEntries, m.detailTargets, m.detailIDs, m.detailHints = result.entries, result.targets, result.ids, result.hints
			m.detailChanges, m.detailCommits = result.changes, result.commits
			m.detailFiles, m.detailHunks = result.files, result.hunks
			m.detailRows, m.detailHead, m.detailDirty = result.rows, result.head, result.dirty
			m.review.read, m.review.draft, m.review.failed = result.review, result.draft, result.draftErr
			if keep {
				if n := place.find(m.detailRows); n >= 0 {
					m.entryCursor = n
				}
			}
			m.detailTruncated = result.truncated
			m.detailShownRef, m.detailShownTab = result.identity.Ref, m.detailTab
			if !same {
				m.detailScroll = 0
				// History opens on its newest entry, at the bottom.
				// History opens on its newest entry, at the bottom, or on the
				// commit the change view returns from.
				if m.detailTab == tabHistory {
					m.entryCursor = max(0, len(m.detailIDs)-1)
					m.detailScroll = len(m.detailLines)
					if n := slices.Index(m.detailIDs, m.changeEntry); m.changeEntry != "" && n >= 0 {
						m.entryCursor = n
					}
					m.changeEntry = ""
				}
			}
			if pinned {
				m.detailScroll = len(m.detailLines)
			}
			if m.detailTab == tabChangeDiff {
				// Scroll to the cursor before the scroll pulls it in.
				m.keepEntryVisible()
			}
			m.scrollDetail(0)
			if !same || !m.scrollsEntries() {
				m.keepEntryVisible()
			}
			current := m.find.current
			m.findMatches()
			if same && current < len(m.find.lines) {
				m.find.current = current
			}
		}
		return m, m.waitDetail()
	case pickerChoicesMsg:
		if m.picker.open && msg.generation == m.picker.generation && msg.notice != "" {
			m.picker, m.notice = pickerState{}, msg.notice
		} else if m.picker.open && msg.generation == m.picker.generation {
			updatePickerChoices(&m.picker, msg)
			if m.picker.query.text != "" {
				return m, m.filterPicker()
			}
		}
		return m, nil
	case pickerFilterMsg:
		if m.picker.open && msg.generation == m.picker.generation && msg.query == m.picker.query.text {
			m.picker.filtered = msg.choices
			m.picker.loading = false
			m.picker.cursor = msg.cursor
		}
		return m, nil
	case actionChoicesMsg:
		return m.receiveActionChoices(msg)
	case actionChoicesReveal:
		if m.launcher.open && uint64(msg) == m.actionCatalogSerial {
			m.launcher.hidden = false
		}
		return m, nil
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
		if m.review.edit != nil {
			return m.reviewEditorFinished(msg)
		}
		return m.editorFinished(msg)
	case reviewPreparedMsg:
		return m.reviewPrepared(msg)
	case reviewSavedMsg:
		return m.reviewSaved(msg)
	case reviewSubmittedMsg:
		return m.reviewSubmitted(msg)
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
		if code, ok := emacsInputKeys[s]; ok && m.modalOpen() && !(m.plainField() && (s == "ctrl+j" || s == "ctrl+k")) {
			msg = tea.KeyPressMsg{Code: code}
			s = msg.String()
		}
		if s == "ctrl+c" && (m.longEdit.choice != "" || m.review.prompt || m.capture.open || m.picker.open || m.archiveConfirm || m.actionConfirm != nil || m.save.open || m.find.open || m.editor.open) {
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
		if m.review.prompt {
			return m, m.updateReviewPrompt(s)
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
		if m.find.open {
			return m.updateFind(msg)
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
// don't reach them; these translate to the keys every input handles. ctrl+j
// and ctrl+k move as in fzf, except in a plain field, where ctrl+k deletes to
// the end.
var emacsInputKeys = map[string]rune{"ctrl+g": tea.KeyEscape, "ctrl+n": tea.KeyDown, "ctrl+p": tea.KeyUp, "ctrl+j": tea.KeyDown, "ctrl+k": tea.KeyUp}

// modalOpen reports an input or prompt that takes every key and the mouse.
func (m Model) modalOpen() bool {
	return m.launcher.open || m.picker.open || m.capture.open || m.longEdit.choice != "" || m.review.prompt || m.editor.open || m.save.open || m.find.open || m.archiveConfirm || m.actionConfirm != nil
}

// plainField reports that keys go to a text field without a list to move
// through. It follows Update's modal order.
func (m Model) plainField() bool {
	switch {
	case m.actionConfirm != nil, m.launcher.open, m.longEdit.choice != "", m.review.prompt:
		return false
	case m.capture.open:
		return !m.capture.choosing
	case m.picker.open:
		return m.picker.inputOnly
	case m.archiveConfirm:
		return false
	}
	return m.save.open || m.find.open
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
		m.detached = nil
	}
	if s == "pagedown" {
		s = "ctrl+d"
	}
	if s == "pageup" {
		s = "ctrl+u"
	}
	if s == "ctrl+s" {
		m.save = savePrompt{open: true, name: newLine(m.views[m.view].Config.Name)}
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
	if s == "/" && m.detailFocus {
		m.openFind()
		return m, nil
	}
	// View keys act on the list wherever it shows, including beside detail.
	if !m.detailFocus || m.split() {
		switch s {
		case "/":
			// A trailing space starts a new clause instead of extending the last.
			text := m.query
			if text != "" {
				text += " "
			}
			m.editQuery(text)
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
			// Reset returns the tab to its definition, clearing the • mark.
			v := &m.views[m.view]
			v.Config = v.Base
			m.query = v.Base.Query
			m.queryError = ""
			m.queryWarning = ""
			m.folded = nil
			m.foldAll = false
			m.board = boardState{}
			m.serial++
			m.project()
			return m, nil
		}
	}
	if s == "J" || s == "K" {
		return m, m.stepItem(s == "J")
	}
	if s == "esc" {
		if m.detailFocus && m.find.text != "" {
			m.find = findState{}
			return m, nil
		}
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
		if prefix := m.prefix; prefix == "[" || prefix == "]" {
			m.prefix = ""
			if s == "c" {
				m.detailScroll, m.entryCursor = m.diffOrigin, m.diffCursor
				m.jumpDiff(m.detailHunks, prefix == "]")
				return m, nil
			}
		}
		// The change view drafts and submits a pull request review.
		if m.change != nil && (s == "c" || s == "r") {
			if s == "c" {
				return m, m.beginReviewDraft()
			}
			return m, m.beginSubmitReview()
		}
		switch s {
		case "[", "]":
			if m.detailTab == tabChangeDiff {
				m.diffOrigin, m.diffCursor = m.detailScroll, m.entryCursor
				m.jumpDiff(m.detailFiles, s == "]")
				m.prefix = s
				return m, nil
			}
			cmd, _ := m.updateItemKey(s)
			return m, cmd
		case "h", "left":
			if !m.split() {
				m.closeDetail()
				return m, nil
			}
			m.detailFocus = false
			if m.detached != nil {
				m.detached = nil
				return m, m.scheduleDetail()
			}
			return m, nil
		case "enter":
			if m.detailTab == tabRelations && len(m.detailTargets) > 0 {
				return m, m.followRelation()
			}
			if m.detailTab == tabHistory && len(m.detailIDs) > 0 {
				if cmd, ok := m.openCommit(); ok {
					return m, cmd
				}
				return m, m.toggleHistoryEntry()
			}
			if m.change != nil {
				return m, m.openChangeCommit()
			}
			if c, ok := m.selectedChange(); ok {
				return m, m.openChange(c)
			}
			m.closeDetail()
			return m, nil
		case "q":
			m.closeDetail()
			return m, nil
		case "backspace":
			return m, m.followBack()
		case "R":
			// The focused item rereads from its source; the list resyncs from the list.
			cmd := m.scheduleDetail()
			m.submitDetail(true)
			if m.opts.Changes != nil {
				m.opts.Changes.Refresh()
			}
			return m, cmd
		case "n", "N":
			if m.find.text != "" {
				m.stepFind(s == "n")
				return m, nil
			}
			cmd, _ := m.updateItemKey(s)
			return m, cmd
		case "tab":
			return m, m.switchDetailTab(m.cycleTab(1))
		case "shift+tab":
			return m, m.switchDetailTab(m.cycleTab(-1))
		case "m":
			if m.detailTruncated || m.detailExpanded {
				return m, m.toggleMore()
			}
			cmd, _ := m.updateItemKey(s)
			return m, cmd
		case "x", "space", " ":
			if m.detailTab == tabCriteria {
				return m, m.toggleCriterion()
			}
		case "j", "down":
			// Entries taller than the view, and the description past the
			// last check, scroll until the next entry shows.
			if m.scrollsEntries() {
				if next := m.entryCursor + 1; next < len(m.detailEntries) && m.detailEntries[next] < m.detailScroll+m.detailVisible() {
					m.entryCursor = next
				} else {
					m.scrollDetail(1)
				}
				return m, nil
			}
			if count, ok := m.entryCount(); ok {
				if count > 0 {
					m.entryCursor = min(count-1, m.entryCursor+1)
					m.keepEntryVisible()
				}
				return m, nil
			}
			m.scrollDetail(1)
		case "k", "up":
			if m.scrollsEntries() {
				if m.entryCursor > 0 && m.entryCursor < len(m.detailEntries) && m.detailEntries[m.entryCursor] > m.detailScroll {
					m.entryCursor--
				} else {
					m.scrollDetail(-1)
				}
				return m, nil
			}
			if _, ok := m.entryCount(); ok {
				m.entryCursor = max(0, m.entryCursor-1)
				m.keepEntryVisible()
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
				m.entryCursor = 0
			} else {
				m.prefix = "g"
			}
		case "G":
			m.scrollDetail(len(m.detailLines))
			m.entryCursor = max(0, len(m.detailEntries)-1)
		default:
			cmd, _ := m.updateItemKey(s)
			return m, cmd
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
			return m, m.showDetail()
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
			return m, m.showDetail()
		}
		m.detailFocus = true
		return m, nil
	}
	if s == "q" {
		return m, tea.Quit
	}
	if !m.detailFocus {
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
		if cmd, handled := m.updateItemKey(s); handled {
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
		if m.opts.Changes != nil {
			m.opts.Changes.Refresh()
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

// switchDetailTab shows another tab of the item in view.
func (m *Model) switchDetailTab(tab detailTab) tea.Cmd {
	m.detailTab = tab
	m.detailExpanded = false
	m.entryCursor = 0
	return m.showDetail()
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
	m.detailEntries, m.detailTargets, m.detailIDs, m.detailHints = nil, nil, nil, nil
	m.detailChanges, m.detailCommits = nil, nil
	m.detailFiles, m.detailHunks = nil, nil
	m.detailRows, m.detailHead = nil, ""
	m.historyOpen, m.historyRef = nil, model.Ref{}
	m.detailHistory = nil
	if m.change != nil {
		m.change, m.changeParent, m.detailTab, m.entryCursor = nil, nil, m.changeTab, m.changeCursor
	}
	m.detailTruncated = false
	m.find = findState{}
}

// stepItem moves to the next or previous item, skipping group headers, without
// changing focus, so detail can page through items in place.
func (m *Model) stepItem(forward bool) tea.Cmd {
	m.count = 0
	m.inputSerial++
	if m.detached != nil {
		if m.heldPlace == 0 {
			return nil // go-to and followed items have no place in the list
		}
		// The held item sat just before the list selection, or after the last row.
		target := m.selected
		if target >= len(m.data.rows) || m.data.rows[target].header != "" || forward != (m.heldPlace > 0) {
			next, ok := m.data.nextItem(m.selected, forward)
			if !ok {
				return nil
			}
			target = next
		}
		m.detached, m.selected = nil, target
		if m.isBoard() {
			m.keepBoardVisible()
		} else {
			m.keepSelectionVisible()
		}
		return m.scheduleDetail()
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

// detailStop is an item detail followed a relation from: a detached row, or
// nil for the list selection.
type detailStop struct {
	row    *engine.Row
	cursor int
	place  int // heldPlace of row
}

// followRelation opens the relation under the cursor as go-to does, keeping the
// view and list selection, and remembers the item for followBack. The catalog
// prepares the target's row from the item alone (D14).
func (m *Model) followRelation() tea.Cmd {
	if !m.detailCurrent() || m.entryCursor < 0 || m.entryCursor >= len(m.detailTargets) {
		return nil
	}
	target := m.detailTargets[m.entryCursor]
	var row engine.Row
	ok := false
	if m.catalog != nil {
		row, ok = m.catalog.Row(target.Ref)
	}
	if !ok {
		m.notice = textutil.Line(target.Ref.ID) + " can't open: " + m.unloadedReason(target.Ref) + "."
		return nil
	}
	if m.detached == nil {
		m.detailHistory = nil // a chain starts at the list selection
	}
	m.detailHistory = append(m.detailHistory, detailStop{row: m.detached, cursor: m.entryCursor, place: m.heldPlace})
	m.detached, m.heldPlace = &row, 0
	m.entryCursor = 0
	return m.showDetail()
}

// followBack leaves the change view, or returns to the item a relation was
// followed from.
func (m *Model) followBack() tea.Cmd {
	if m.change != nil {
		return m.closeChange()
	}
	if m.detached == nil || len(m.detailHistory) == 0 {
		m.detailHistory = nil
		m.notice = "No previous item."
		return nil
	}
	stop := m.detailHistory[len(m.detailHistory)-1]
	m.detailHistory = m.detailHistory[:len(m.detailHistory)-1]
	m.detached, m.heldPlace = stop.row, stop.place
	m.refreshDetached()
	m.entryCursor = stop.cursor
	return m.showDetail()
}

// refreshDetached keeps a detached item current, since no projection lists it.
func (m *Model) refreshDetached() {
	if m.detached != nil && m.catalog != nil {
		if row, ok := m.catalog.Row(m.detached.Ref); ok {
			m.detached = &row
		}
	}
}

// unloadedReason says why a relation's target isn't in the snapshot.
func (m Model) unloadedReason(ref model.Ref) string {
	if m.catalog == nil || m.snapshot == nil {
		return "items are still loading"
	}
	state := m.snapshot.Source(ref.Source)
	name := textutil.Line(ref.Source)
	switch {
	case state.Error != "":
		return name + " failed to load"
	case state.Coverage == "":
		return name + " isn't a configured source"
	case state.Initial || state.Coverage == source.Unknown:
		return name + " is still loading"
	case state.Coverage == source.Partial:
		return name + " isn't fully loaded"
	}
	return "it isn't in " + name
}

func identityForSelected(m Model) detailIdentity {
	row, ok := m.selectedRow()
	return identityFor(row, ok)
}
func (m *Model) scrollDetail(delta int) {
	m.detailScroll = max(0, min(m.detailScroll+delta, max(0, len(m.detailLines)-m.detailVisible())))
	if m.detailTab == tabChangeDiff {
		m.diffCursorInView()
	}
}
