// Package tui adapts immutable engine projections to a viewport-only UI.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

type Options struct {
	HighContrast, NoColor                   bool
	Project                                 string
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
	SaveView                                func(project.ViewConfig, bool) (project.SavedView, error)
}

type screenRow struct {
	row    engine.Row
	group  int
	value  string // group value; folds and selection survive live projections
	header string
}
type prepared struct {
	rows            []screenRow
	starts          []int
	pending, failed int
	items           int
}
type snapshotMsg struct {
	snapshot *engine.Snapshot
	badges   string
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
	width, height            int
	opts                     Options
	title                    lipgloss.Style
	showHelp                 bool
	query                    string
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
	badges, failure          string
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
	capture                  captureState
	longEdit                 longEditState
	board                    boardState
}

var viewNames = []string{"Next", "Active", "Mine", "All", "Board"}
var viewQueries = []string{"is:ready sort:next group:status", "category:active group:status", "assignee:me group:status", "is:open group:status", "is:open group:status"}

func New(opts Options) Model {
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	if opts.Render == nil {
		opts.Render = RenderMarkdown
	}
	style := lipgloss.NewStyle()
	if !opts.NoColor {
		style = style.Bold(true)
		if opts.HighContrast {
			style = style.Underline(true)
		} else {
			style = style.Foreground(lipgloss.Color("6"))
		}
	}
	model := Model{width: 80, height: 24, opts: opts, title: style, folded: nil, loading: opts.Store != nil, query: viewQueries[0], marks: newMarkSet()}
	if opts.Read != nil {
		theme := "color"
		if opts.NoColor {
			theme = "no-color"
		} else if opts.HighContrast {
			theme = "high-contrast"
		}
		model.detailWorker = newDetailWorker(opts.Context, opts.Read, opts.Render, theme)
	}
	model.initViews()
	model.notice = opts.Warning
	return model
}

// Bind subscribes before the program starts. The caller owns unsubscribe and
// closes the engine after Run, never on Bubble Tea's update loop.
func (m *Model) Bind(ch <-chan *engine.Snapshot) { m.snapshots = ch }
func (m Model) Init() tea.Cmd {
	var commands []tea.Cmd
	if m.opts.Store != nil {
		commands = append(commands, m.waitSnapshot(), m.waitProjection())
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
			badges := make([]string, 0, len(m.opts.Sources))
			syncing := false
			for _, id := range m.opts.Sources {
				status := s.Source(id)
				label := "✓ synced"
				if status.Coverage != "complete" {
					label = "~ stale"
				}
				if status.Coverage == "unknown" {
					label = "⠋ syncing"
					syncing = syncing || status.Error == ""
				}
				if status.Error != "" {
					label = "! error: " + textutil.Line(status.Error)
				}
				badges = append(badges, textutil.Line(id)+" "+label)
			}
			return snapshotMsg{s, strings.Join(badges, " · "), syncing}
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
	m.detailGeneration++
	row, ok := m.selectedRow()
	m.detailIdentity = identityFor(row, ok)
	m.detailLines = nil
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
	m.detailWorker.submit(detailRequest{
		generation: m.detailGeneration, row: row, width: max(1, m.detailWidth()-4),
		tab: m.detailTab, expanded: m.detailExpanded, milestones: m.opts.Milestones,
	})
}

// Flattening and folding are commands: even a single giant group cannot put
// backlog-sized work on Update. Each result is fenced by the model's serial.
func (m Model) prepare() tea.Cmd {
	groups, folded, serial, from := m.groups, m.folded, m.serial, m.selected
	foldAll, ungrouped := m.foldAll, m.ungrouped
	board, statusGroup := m.isBoard(), m.boardStatusGroup()
	var statuses []string
	if board {
		for _, s := range m.opts.Statuses {
			statuses = append(statuses, s.Key)
		}
	}
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
			groups = boardGroups(groups, statuses, statusGroup)
			states = nil // board columns never fold
		}
		data := prepareGroupedRows(groups, states, board || !ungrouped)
		selected := -1
		for i, r := range data.rows {
			matches := (board || r.value == anchor.value) && r.row.Ref == anchor.row.Ref && (r.header == "") == (anchor.header == "")
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
	return prepareGroupedRows(groups, folded, true)
}
func prepareGroupedRows(groups []engine.RowGroup, folded map[string]bool, grouped bool) prepared {
	p := prepared{}
	for g, group := range groups {
		p.items += len(group.Rows)
		p.starts = append(p.starts, len(p.rows))
		glyph := "▾"
		if folded[group.Value] {
			glyph = "▸"
		}
		if grouped {
			p.rows = append(p.rows, screenRow{group: g, value: group.Value, header: fmt.Sprintf("%s %s (%d)", glyph, textutil.Line(group.Value), len(group.Rows))})
		}
		for _, row := range group.Rows {
			if row.Pending {
				p.pending++
			}
			if row.Error != "" {
				p.failed++
			}
			if !grouped || !folded[group.Value] {
				p.rows = append(p.rows, screenRow{row: row, group: g, value: group.Value})
			}
		}
	}
	return p
}
func (m *Model) project() {
	if m.snapshot != nil && m.opts.Projector != nil {
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
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		oldDetailWidth := m.detailWidth()
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		if m.width < 120 {
			m.detailFocus = m.detailOpen // the detail pane replaces the list
		}
		if m.detailOpen && m.detailWidth() != oldDetailWidth {
			return m, m.scheduleDetail()
		}
	case snapshotMsg:
		m.snapshot, m.badges, m.syncing = msg.snapshot, msg.badges, msg.syncing
		m.project()
		return m, m.waitSnapshot()
	case projectionMsg:
		if msg.Request != m.request {
			return m, m.waitProjection()
		}
		m.applied = msg.Request
		if m.editor.open {
			m.editor.candidates = msg.Completions
			m.editor.candidate = 0
			m.editor.ready = m.request
		}
		if msg.Error != nil {
			m.queryError = textutil.Line(msg.Error.Error())
			m.loading = false
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
		return m, tea.Batch(m.prepare(), m.waitProjection())
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
			m.detailTruncated = result.truncated
			m.detailScroll = 0
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
	case editPreparedMsg:
		return m.editorPrepared(msg)
	case editFinishedMsg:
		return m.editorFinished(msg)
	case editSavedMsg:
		return m.editorSaved(msg)
	case editResultMsg:
		m.editResult(msg)
		return m, nil
	case openResultMsg:
		m.notice = msg.err
		return m, nil
	case tea.KeyPressMsg:
		s := msg.String()
		if s == "ctrl+c" {
			return m, tea.Quit
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
			case "F":
				m.query = clearQuery(m.query)
				m.queryError = ""
				m.queryWarning = ""
				m.serial++
				m.project()
				return m, nil
			}
		}
		if s == "?" {
			m.showHelp = !m.showHelp
			return m, nil
		}
		if s == "esc" {
			if m.showHelp {
				m.showHelp = false
				return m, nil
			}
			if m.detailOpen {
				m.detailOpen = false
				m.detailFocus = false
				m.detailGeneration++
				m.detailLoading = false
				m.detailLines = nil
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
				if m.width >= 120 {
					m.detailFocus = false
				}
				return m, nil
			case "q", "enter":
				m.detailOpen = false
				m.detailFocus = false
				m.detailGeneration++
				m.detailLoading = false
				m.detailLines = nil
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
				m.scrollDetail(max(1, (m.height-6)/2))
			case "ctrl+u", "pageup":
				m.scrollDetail(-max(1, (m.height-6)/2))
			case "ctrl+f":
				m.scrollDetail(max(1, m.height-6))
			case "ctrl+b":
				m.scrollDetail(-max(1, m.height-6))
			}
			return m, nil
		}
		if s == "enter" {
			m.detailOpen = !m.detailOpen
			m.detailFocus = m.detailOpen
			if m.detailOpen {
				return m, m.scheduleDetail()
			}
			m.detailGeneration++
			m.detailLoading = false
			m.detailLines = nil
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
		page := max(1, m.height-5)
		if m.isBoard() && m.navigateBoard(s, n, hadCount, prefix) {
			if m.detailOpen && m.selected != previousSelection {
				return m, m.scheduleDetail()
			}
			return m, nil
		}
		switch {
		case prefix == "g" && s == "g":
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
		case s == "G":
			if hadCount {
				m.selected = n - 1
			} else {
				m.selected = len(m.data.rows) - 1
			}
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
		if m.selected < m.top {
			m.top = m.selected
		}
		if m.selected >= m.top+page {
			m.top = m.selected - page + 1
		}
		if m.detailOpen && m.selected != previousSelection {
			return m, m.scheduleDetail()
		}
	}
	m.keepBoardVisible()
	page := max(1, m.height-5)
	if m.selected < m.top {
		m.top = m.selected
	}
	if m.selected >= m.top+page {
		m.top = m.selected - page + 1
	}
	m.top = max(0, min(m.top, max(0, len(m.data.rows)-page)))
	return m, nil
}
func identityForSelected(m Model) detailIdentity {
	row, ok := m.selectedRow()
	return identityFor(row, ok)
}
func (m *Model) scrollDetail(delta int) {
	visible := max(1, m.height-6)
	m.detailScroll = max(0, min(m.detailScroll+delta, max(0, len(m.detailLines)-visible)))
}

func cell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(textutil.Line(s), width, "…")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}
func priority(p model.Priority) string {
	switch p {
	case model.Urgent:
		return "▲▲"
	case model.High:
		return "▲"
	case model.Medium:
		return "■"
	case model.Low:
		return "▼"
	}
	return "─"
}
func (m Model) View() tea.View {
	lines := make([]string, 0, m.height)
	tabs := make([]string, len(m.views))
	for i, v := range m.views {
		name := textutil.Line(v.Config.Name)
		tabs[i] = name
		if i == m.view {
			tabs[i] = "[" + name + "]"
		}
	}
	header := "backlog · " + textutil.Line(m.opts.Project) + " · " + strings.Join(tabs, "  ")
	lines = append(lines, m.title.Render(cell(header, m.width)), cell(m.badges, m.width))
	listWidth := m.width
	if m.detailOpen && m.width >= 120 {
		listWidth = m.listWidth()
	}
	wide := listWidth >= 80
	columns := "  ID           Title"
	if wide {
		columns = "  ID           P  Status         ⊘   ▣       Title"
	}
	if custom := m.views[m.view].Config.Columns; wide && len(custom) > 0 {
		columns = "  " + strings.Join(custom, "  ")
	}
	if m.isBoard() {
		columns = m.boardLine(-1)
	}
	if m.detailOpen && m.width >= 120 {
		detailWidth := m.detailWidth()
		lines = append(lines, cell(columns, listWidth)+"│"+cell(m.detailHeader(), detailWidth))
		page := max(0, m.height-5)
		for i := 0; i < page; i++ {
			left := m.highlightListCell(m.top+i, m.listLine(m.top+i, wide), listWidth)
			if m.isBoard() {
				left = m.boardLine(i)
			}
			right := m.detailPaneLine(i)
			lines = append(lines, left+"│"+cell(right, detailWidth))
		}
	} else if m.detailOpen {
		lines = append(lines, cell(m.detailHeader(), m.width), cell(m.detailTabs(), m.width))
		for i := 0; i < max(0, m.height-6); i++ {
			lines = append(lines, cell(m.detailContentLine(i), m.width))
		}
	} else {
		lines = append(lines, cell(columns, m.width))
		for i := 0; i < max(0, m.height-5); i++ {
			line := m.highlightListCell(m.top+i, m.listLine(m.top+i, wide), m.width)
			if m.isBoard() {
				line = m.boardLine(i)
			}
			lines = append(lines, line)
		}
	}
	lines = append(lines, strings.Repeat("─", m.width))
	footer := fmt.Sprintf("%s · %d pending · %d failed · ? help · q quit", m.queryText(), m.data.pending, m.data.failed)
	if m.detailOpen {
		footer = "tab next · shift-tab previous · j/k scroll · h list · esc/q back"
		if !m.detailFocus {
			footer = "j/k select · tab focus · enter close · q quit"
		}
		if m.detailTruncated {
			if m.detailExpanded {
				footer = "m show less · " + footer
			} else {
				footer = "m show more · " + footer
			}
		}
	}
	if m.visual {
		footer += " · visual range"
	}
	if m.marks != nil && m.marks.size() > 0 {
		footer += fmt.Sprintf(" · %d marked", m.marks.size())
	}
	if m.notice != "" {
		footer = "warning: " + m.notice + " · " + footer
	}
	if layout := m.views[m.view].Config.Layout; layout != "" && layout != "list" && layout != "board" {
		footer = layout + " unavailable; list · " + footer
	}
	if m.capture.open {
		return m.captureFormView()
	}
	if m.longEdit.choice != "" {
		return m.longEditView()
	}
	if m.picker.open || m.archiveConfirm {
		return m.editView()
	}
	if m.save.open {
		destination := "user"
		if m.save.project {
			destination = "project"
		}
		footer = "Save view (" + destination + ", tab destination, ctrl-u clear): " + m.save.name
		if m.save.busy {
			footer = "Saving view…"
		}
	}
	if m.editor.open {
		footer = "/ " + m.editor.text[:m.editor.cursor] + "▏" + m.editor.text[m.editor.cursor:]
	}
	if m.queryError != "" {
		footer = "! " + m.queryError + " · " + footer
	} else if m.queryWarning != "" {
		footer = "warning: " + m.queryWarning + " · " + footer
	}
	if m.count > 0 {
		footer += " · " + strconv.Itoa(m.count) + m.prefix
	}
	lines = append(lines, cell(footer, m.width))
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}
func (m Model) listLine(index int, wide bool) string {
	if line, ok := m.queryOverlay(index - m.top); ok {
		return line
	}
	if m.showHelp {
		hints := []string{
			"j/k ↓/↑ · count + move · gg/G/NG · [/]/groups",
			"enter detail · tab focus/cycle · detail j/k scroll · esc back",
			"s status · </> workflow · p priority · a/A assignee · l labels",
			"m milestone · b blocker · P parent · t title · S/D start/done",
			"Criteria: x/space toggle · dd archive · . repeat · u/ctrl-r undo/redo",
			"space mark · V range · ctrl-a mark all · esc clear marks",
			"y + key yank · yb branch name · O open safe URL · ? help",
			"R resync · 1–9 views · gt/gT cycle · q/ctrl-c quit",
		}
		if index < len(hints) {
			return hints[index]
		}
		return ""
	}
	if m.loading {
		if index == 0 {
			return "⠋ Loading…"
		}
		return ""
	}
	if m.failure != "" {
		if index == 0 {
			return "! " + m.failure
		}
		return ""
	}
	if len(m.data.rows) == 0 {
		if index == 0 && (m.applied != m.request || m.prepared != m.serial || m.syncing) {
			return "\u280b Loading\u2026" // not empty yet: a newer result is pending
		}
		if index == 0 {
			return "No matching items."
		}
		return ""
	}
	if index >= len(m.data.rows) {
		return ""
	}
	r := m.data.rows[index]
	marker := "  "
	if r.header != "" {
		if index == m.selected {
			marker = "> "
		}
		return marker + r.header
	}
	if m.isMarked(index, r.row.Ref) {
		marker = "* "
	}
	if index == m.selected {
		if marker == "* " {
			marker = "*>"
		} else {
			marker = "> "
		}
	}
	state := ""
	if r.row.Pending {
		state = "● "
	}
	if r.row.Error != "" {
		state = "! "
	}
	if custom := m.views[m.view].Config.Columns; wide && len(custom) > 0 {
		values := make([]string, 0, len(custom))
		for _, column := range custom {
			switch column {
			case "id":
				values = append(values, cell(r.row.Ref.ID, 12))
			case "priority":
				values = append(values, cell(priority(r.row.Priority), 2))
			case "status":
				values = append(values, cell(r.row.Status, 14))
			case "blockers":
				values = append(values, strconv.Itoa(r.row.OpenBlockers))
			case "children":
				values = append(values, fmt.Sprintf("%d/%d", r.row.ChildrenDone, r.row.ChildrenTotal))
			case "title":
				values = append(values, state+textutil.Line(r.row.Title))
			}
		}
		return marker + strings.Join(values, "  ")
	}
	line := marker + cell(r.row.Ref.ID, 12) + " "
	if wide {
		line += cell(priority(r.row.Priority), 2) + " " + cell(r.row.Status, 14) + " "
		blockers, children := "─", "─"
		if r.row.OpenBlockers > 0 {
			blockers = strconv.Itoa(r.row.OpenBlockers)
		}
		if r.row.ChildrenTotal > 0 {
			children = fmt.Sprintf("%d/%d", r.row.ChildrenDone, r.row.ChildrenTotal)
		}
		line += cell(blockers, 3) + " " + cell(children, 7) + " "
	}
	return line + state + textutil.Line(r.row.Title)
}
func (m Model) detailHeader() string {
	row, ok := m.selectedRow()
	if !ok {
		return "Detail · no item selected"
	}
	parts := []string{row.Ref.ID, row.Title, row.Status, string(row.Priority)}
	if row.Milestone != "" {
		parts = append(parts, "milestone "+row.Milestone)
	}
	return strings.Join(parts, " · ")
}
func (m Model) detailTabs() string {
	tabs := make([]string, len(detailTabNames))
	for i, name := range detailTabNames {
		if detailTab(i) == m.detailTab {
			tabs[i] = "[" + name + "]"
		} else {
			tabs[i] = name
		}
	}
	text := strings.Join(tabs, "  ")
	if ansi.StringWidth(text) > m.detailWidth() {
		return "[" + detailTabNames[m.detailTab] + "] · tab/shift-tab to switch"
	}
	return text
}
func (m Model) detailPaneLine(index int) string {
	if index == 0 {
		return m.detailTabs()
	}
	return m.detailContentLine(index - 1)
}
func (m Model) detailContentLine(index int) string {
	if row, ok := m.selectedRow(); ok && row.Error != "" {
		if index == 0 {
			return "! " + textutil.Line(row.Error)
		}
		if index == 1 && (row.ErrorLocalVersion != "" || row.ErrorRemoteVersion != "") {
			return fmt.Sprintf("Conflict versions: local %s · source %s", row.ErrorLocalVersion, row.ErrorRemoteVersion)
		}
		index--
	}
	line := m.detailScroll + index
	if m.detailTab == tabCriteria && m.criteriaCursor >= 0 && criteriaLineAt(m.detailLines, m.criteriaCursor) == line && line < len(m.detailLines) {
		return "> " + m.detailLines[line]
	}
	if !m.detailIdentity.Valid {
		if index == 0 {
			return "Select an item to view details."
		}
		return ""
	}
	if m.detailError != "" {
		if index == 0 {
			return "! " + m.detailError
		}
		return ""
	}
	if m.detailLoading && len(m.detailLines) == 0 {
		if index == 0 {
			return "⠋ Loading full detail…"
		}
		return ""
	}
	if line >= len(m.detailLines) {
		return ""
	}
	return m.detailLines[line]
}
