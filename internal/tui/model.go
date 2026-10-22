// Package tui contains the Bubble Tea shell. The bootstrap has no source I/O,
// timers, or workers; future data loads must run in commands, never Update/View.
package tui

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type Options struct {
	HighContrast bool
	NoColor      bool
}

type Model struct {
	width, height int
	help          help.Model
	title         lipgloss.Style
}

var (
	helpKey = key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help"))
	backKey = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close help"))
	quitKey = key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q/ctrl-c", "quit"))
)

type keyMap struct{}

func (keyMap) ShortHelp() []key.Binding  { return []key.Binding{helpKey, quitKey} }
func (keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{{helpKey, backKey, quitKey}} }

func New(opts Options) Model {
	h := help.New()
	h.SetWidth(80)
	plain := lipgloss.NewStyle()
	title, hint := plain, plain
	if !opts.NoColor {
		title = title.Bold(true)
		hint = hint.Underline(opts.HighContrast)
		if !opts.HighContrast {
			title = title.Foreground(lipgloss.Color("6"))
			hint = hint.Foreground(lipgloss.Color("6"))
		}
	}
	h.Styles = help.Styles{
		Ellipsis: plain, ShortKey: hint, ShortDesc: plain, ShortSeparator: plain,
		FullKey: hint, FullDesc: plain, FullSeparator: plain,
	}
	return Model{width: 80, height: 24, help: h, title: title}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.help.SetWidth(m.width)
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, quitKey):
			return m, tea.Quit
		case key.Matches(msg, helpKey):
			m.help.ShowAll = !m.help.ShowAll
		case key.Matches(msg, backKey):
			m.help.ShowAll = false
		}
	}
	return m, nil
}

func (m Model) View() tea.View {
	body := "No backlog loaded.\nTask storage and views are not implemented yet."
	if m.help.ShowAll {
		body = "Bootstrap shell\nOnly help and quit are available; no task data is read or written."
	}
	content := m.title.Render("backlog · bootstrap") + "\n\n" + body + "\n\n" + m.help.View(keyMap{})
	view := tea.NewView(lipgloss.NewStyle().Width(m.width).MaxWidth(m.width).MaxHeight(m.height).Render(content))
	view.AltScreen = true
	return view
}
