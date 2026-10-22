package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// Row is prepared on a worker. The list never loads or projects source items.
type Row struct{ ID, Title, Body string }

type detailDue uint64
type detailReady struct {
	generation uint64
	text       string
}

// List is the reusable virtualized core for the future list shell. Rows must
// remain immutable. Render runs in a command, never on the update loop.
type List struct {
	Rows          []Row
	Selected      int
	Width, Height int
	Detail        string
	Render        func(string, int) (string, error)
	generation    uint64
	pending       bool
}

func (m List) Init() tea.Cmd { return nil }
func (m List) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Width, m.Height = max(1, msg.Width), max(1, msg.Height)
	case tea.KeyPressMsg:
		previous := m.Selected
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			m.Selected = min(max(0, len(m.Rows)-1), m.Selected+1)
		case "k", "up":
			m.Selected = max(0, m.Selected-1)
		default:
			return m, nil
		}
		if m.Selected == previous {
			return m, nil
		}
		// A single outstanding settle timer coalesces held navigation. The
		// renderer is only launched once the selection has stopped changing.
		m.generation++
		m.Detail = ""
		if !m.pending {
			m.pending = true
			g := m.generation
			return m, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return detailDue(g) })
		}
	case detailDue:
		if uint64(msg) != m.generation {
			g := m.generation
			return m, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return detailDue(g) })
		}
		m.pending = false
		if m.Render != nil && len(m.Rows) > 0 {
			render, body, width, g := m.Render, m.Rows[m.Selected].Body, m.Width, m.generation
			return m, func() tea.Msg {
				text, err := render(body, width)
				if err != nil {
					text = err.Error()
				}
				return detailReady{g, text}
			}
		}
	case detailReady:
		if msg.generation == m.generation {
			m.Detail = msg.text
		}
	}
	return m, nil
}
func (m List) View() tea.View {
	height, width := max(1, m.Height-3), max(1, m.Width)
	first := max(0, m.Selected-height+1)
	var b strings.Builder
	b.WriteString("backlog · virtualized list\n")
	for i := first; i < min(len(m.Rows), first+height); i++ {
		marker := " "
		if i == m.Selected {
			marker = ">"
		}
		b.WriteString(ansi.Truncate(marker+" "+textutil.Line(m.Rows[i].ID)+" "+textutil.Line(m.Rows[i].Title), width, "…"))
		b.WriteByte('\n')
	}
	// The detail body is already rendered; clip it to a bounded viewport.
	if m.Detail != "" {
		b.WriteString(ansi.Truncate(strings.SplitN(m.Detail, "\n", 2)[0], width, "…"))
	}
	fmt.Fprintf(&b, "\nselection=%06d", m.Selected)
	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}
