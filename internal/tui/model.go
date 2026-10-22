// Package tui adapts immutable engine projections to a viewport-only UI.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

type Options struct {
	HighContrast, NoColor bool
	Project               string
	Store                 *engine.Store
	Projector             *engine.Projector
	Context               context.Context
	Sources               []string
}

type screenRow struct {
	row    engine.Row
	group  int
	header string
}
type prepared struct {
	rows            []screenRow
	starts          []int
	pending, failed int
}
type snapshotMsg struct {
	snapshot *engine.Snapshot
	badges   string
}
type projectionMsg engine.Projection
type foldedMsg struct {
	serial uint64
	data   prepared
}
type viewDue uint64

// Immutable fold requests avoid copying a backlog-sized map on Update.
type foldState struct {
	group    int
	previous *foldState
}

type Model struct {
	width, height       int
	opts                Options
	title               lipgloss.Style
	showHelp            bool
	selected, top, view int
	count               int
	prefix              string
	serial              uint64
	inputSerial         uint64
	groups              []engine.RowGroup
	folded              *foldState
	data                prepared
	loading             bool
	badges, failure     string
	snapshots           <-chan *engine.Snapshot
	snapshot            *engine.Snapshot
	request             uint64
}

var quitKey = key.NewBinding(key.WithKeys("q", "ctrl+c"))

var viewNames = []string{"Next", "Active", "Mine", "All"}
var viewQueries = []string{"is:ready sort:next group:status", "category:active group:status", "assignee:me group:status", "is:open group:status"}

func New(opts Options) Model {
	if opts.Context == nil {
		opts.Context = context.Background()
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
	return Model{width: 80, height: 24, opts: opts, title: style, folded: nil, loading: opts.Store != nil}
}

// Bind subscribes before the program starts. The caller owns unsubscribe and
// closes the engine after Run, never on Bubble Tea's update loop.
func (m *Model) Bind(ch <-chan *engine.Snapshot) { m.snapshots = ch }
func (m Model) Init() tea.Cmd {
	if m.opts.Store == nil {
		return nil
	}
	return tea.Batch(m.waitSnapshot(), m.waitProjection())
}
func (m Model) waitSnapshot() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.opts.Context.Done():
			return nil
		case s := <-m.snapshots:
			badges := make([]string, 0, len(m.opts.Sources))
			for _, id := range m.opts.Sources {
				status := s.Source(id)
				label := "✓ synced"
				if status.Coverage != "complete" {
					label = "~ stale"
				}
				if status.Coverage == "unknown" {
					label = "⠋ syncing"
				}
				if status.Error != "" {
					label = "! error: " + textutil.Line(status.Error)
				}
				badges = append(badges, textutil.Line(id)+" "+label)
			}
			return snapshotMsg{s, strings.Join(badges, " · ")}
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

// Flattening and folding are commands: even a single giant group cannot put
// backlog-sized work on Update. Each result is fenced by the model's serial.
func (m Model) prepare() tea.Cmd {
	groups, folded, serial := m.groups, m.folded, m.serial
	return func() tea.Msg {
		states := map[int]bool{}
		for f := folded; f != nil; f = f.previous {
			states[f.group] = !states[f.group]
		}
		return foldedMsg{serial, prepareRows(groups, states)}
	}
}
func prepareRows(groups []engine.RowGroup, folded map[int]bool) prepared {
	p := prepared{}
	for g, group := range groups {
		p.starts = append(p.starts, len(p.rows))
		glyph := "▾"
		if folded[g] {
			glyph = "▸"
		}
		p.rows = append(p.rows, screenRow{group: g, header: fmt.Sprintf("%s %s (%d)", glyph, textutil.Line(group.Value), len(group.Rows))})
		for _, row := range group.Rows {
			if row.Pending {
				p.pending++
			}
			if row.Error != "" {
				p.failed++
			}
			if !folded[g] {
				p.rows = append(p.rows, screenRow{row: row, group: g})
			}
		}
	}
	return p
}
func (m *Model) project() {
	if m.snapshot != nil && m.opts.Projector != nil {
		m.request = m.opts.Projector.Request(m.snapshot, viewQueries[m.view])
	}
}
func (m *Model) switchView(n int) {
	if n < 0 || n >= len(viewNames) {
		return
	}
	m.serial++
	m.view = n
	m.selected = 0
	m.top = 0
	m.loading = true
	m.project()
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case snapshotMsg:
		m.snapshot, m.badges = msg.snapshot, msg.badges
		m.project()
		return m, m.waitSnapshot()
	case projectionMsg:
		if msg.Request != m.request {
			return m, m.waitProjection()
		}
		if msg.Error != nil {
			m.failure = textutil.Line(msg.Error.Error())
			m.loading = false
			return m, m.waitProjection()
		}
		m.failure = ""
		m.groups = msg.Groups
		m.serial++
		m.folded = nil
		return m, tea.Batch(m.prepare(), m.waitProjection())
	case foldedMsg:
		if msg.serial != m.serial {
			return m, nil
		}
		m.data = msg.data
		m.loading = false
	case viewDue:
		if uint64(msg) == m.inputSerial && m.count > 0 && m.prefix == "" {
			m.switchView(m.count - 1)
			m.count = 0
		}
	case tea.KeyPressMsg:
		s := msg.String()
		if key.Matches(msg, quitKey) {
			return m, tea.Quit
		}
		if s == "?" {
			m.showHelp = !m.showHelp
			return m, nil
		}
		if s == "esc" {
			m.showHelp = false
			m.count = 0
			m.prefix = ""
			m.inputSerial++
			return m, nil
		}
		if len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
			m.count = min(1000000, m.count*10+int(s[0]-'0'))
			m.inputSerial++
			serial := m.inputSerial
			return m, tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg { return viewDue(serial) })
		}
		n := max(1, m.count)
		hadCount := m.count > 0
		m.count = 0
		m.inputSerial++
		prefix := m.prefix
		m.prefix = ""
		page := max(1, m.height-5)
		switch {
		case prefix == "g" && s == "g":
			m.selected = 0
		case prefix == "g" && s == "t":
			m.switchView((m.view + 1) % len(viewNames))
		case prefix == "g" && s == "T":
			m.switchView((m.view + len(viewNames) - 1) % len(viewNames))
		case prefix == "z" && s == "a":
			if len(m.data.rows) > 0 {
				g := m.data.rows[m.selected].group
				m.folded = &foldState{group: g, previous: m.folded}
				m.serial++
				m.selected = m.data.starts[g]
				return m, m.prepare()
			}
		case s == "g" || s == "z":
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
	}
	m.selected = max(0, min(m.selected, len(m.data.rows)-1))
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
	tabs := make([]string, len(viewNames))
	for i, name := range viewNames {
		tabs[i] = name
		if i == m.view {
			tabs[i] = "[" + name + "]"
		}
	}
	header := "backlog · " + textutil.Line(m.opts.Project) + " · " + strings.Join(tabs, "  ")
	lines = append(lines, m.title.Render(cell(header, m.width)), cell(m.badges, m.width))
	wide := m.width >= 80
	columns := "  ID           Title"
	if wide {
		columns = "  ID           P  Status         ⊘   ▣       Title"
	}
	lines = append(lines, cell(columns, m.width))
	page := max(0, m.height-5)
	for i := 0; i < page; i++ {
		line := ""
		index := m.top + i
		if m.showHelp {
			hints := []string{"j/k ↓/↑ · count + move · gg/G/NG", "ctrl-d/u half page · ctrl-f/b full page", "[/] groups · za fold · 1–4 views · gt/gT", "R resync · ? help · esc back · q/ctrl-c quit", "A digit selects a view after 350ms; follow it with a motion for a count.", "Detail, editing, saved views and board are not available yet."}
			if i < len(hints) {
				line = hints[i]
			}
		} else if m.loading {
			if i == 0 {
				line = "⠋ Loading…"
			}
		} else if m.failure != "" {
			if i == 0 {
				line = "! " + m.failure
			}
		} else if len(m.data.rows) == 0 {
			if i == 0 {
				line = "No matching items."
			}
		} else if index < len(m.data.rows) {
			r := m.data.rows[index]
			marker := "  "
			if index == m.selected {
				marker = "> "
			}
			if r.header != "" {
				line = marker + r.header
			} else {
				state := ""
				if r.row.Pending {
					state = "● "
				}
				if r.row.Error != "" {
					state = "! "
				}
				line = marker + cell(r.row.Ref.ID, 12) + " "
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
				line += state + textutil.Line(r.row.Title)
			}
		}
		lines = append(lines, cell(line, m.width))
	}
	lines = append(lines, strings.Repeat("─", m.width))
	footer := fmt.Sprintf("%s · %d pending · %d failed · ? help · q quit", viewQueries[m.view], m.data.pending, m.data.failed)
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
