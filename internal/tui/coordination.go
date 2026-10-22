package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
)

type claimsPoll struct{}
type claimsExpiry time.Time
type claimOperationMsg struct{ message string }

func (m Model) claimPoll() tea.Cmd {
	c, _ := engine.Coordination(m.opts.Context)
	if c == nil || m.opts.Projector == nil {
		return nil
	}
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return claimsPoll{} })
}
func (m *Model) refreshClaims() {
	if m.snapshot != nil && m.opts.Projector != nil {
		text := m.query
		if m.editor.open {
			text = m.editor.text
		}
		m.request = m.opts.Projector.RefreshClaims(m.snapshot, text)
	}
}
func (m *Model) expiryCommand() tea.Cmd {
	if m.claimExpiry.Equal(m.claimTimerAt) {
		return nil
	}
	if m.claimExpiry.IsZero() {
		return nil
	}
	now := time.Now()
	if m.opts.Now != nil {
		now = m.opts.Now()
	}
	at := m.claimExpiry
	m.claimTimerAt = at
	return tea.Tick(max(time.Duration(0), at.Sub(now)), func(time.Time) tea.Msg { return claimsExpiry(at) })
}
func (m Model) claimCommand() tea.Cmd {
	row, ok := m.selectedRow()
	if !ok || m.opts.Read == nil {
		return nil
	}
	c, _ := engine.Coordination(m.opts.Context)
	if c == nil {
		return func() tea.Msg { return claimOperationMsg{"Coordination is not configured."} }
	}
	return func() tea.Msg {
		item, err := m.opts.Read(m.opts.Context, row.Ref)
		if err != nil {
			return claimOperationMsg{err.Error()}
		}
		result := engine.ClaimItem(m.opts.Context, item)
		if result.Outcome == "held" {
			result = engine.ReleaseItem(m.opts.Context, item)
		}
		return claimOperationMsg{fmt.Sprintf("%s: %s", result.Outcome, result.Message)}
	}
}
