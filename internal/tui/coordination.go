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
type claimsRenew time.Time
type claimRenewed struct{ message string }

func (m Model) claimPoll() tea.Cmd {
	c, _ := engine.Coordination(m.opts.Context)
	if c == nil && !m.hasWorkspace() || m.opts.Projector == nil {
		return nil
	}
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return claimsPoll{} })
}
func (m *Model) refreshClaims() { m.requestProjection(true) }
func (m *Model) expiryCommand() tea.Cmd {
	if m.claimExpiry.Equal(m.claimTimerAt) {
		return nil
	}
	if m.claimExpiry.IsZero() {
		return nil
	}
	at := m.claimExpiry
	m.claimTimerAt = at
	return tea.Tick(max(time.Duration(0), at.Sub(m.now())), func(time.Time) tea.Msg { return claimsExpiry(at) })
}

// renewCommand renews this session's claim at half its remaining lease, as
// bl heartbeat -- COMMAND does, so a claim lasts while the TUI runs. A run
// renews its own claim, and a lapsed claim is no longer mine, so neither is.
func (m *Model) renewCommand() tea.Cmd {
	if m.mineExpiry.IsZero() || m.renewing || m.mineExpiry.Equal(m.renewTimerAt) {
		return nil
	}
	if _, caller := engine.Coordination(m.opts.Context); caller.Run != "" {
		return nil
	}
	at := m.mineExpiry
	m.renewTimerAt = at
	return tea.Tick(max(at.Sub(m.now())/2, time.Second), func(time.Time) tea.Msg { return claimsRenew(at) })
}
func (m *Model) renew(at time.Time) tea.Cmd {
	if at.Equal(m.renewTimerAt) {
		m.renewTimerAt = time.Time{}
	}
	// The claim changed while the timer ran; schedule for the current one.
	if !at.Equal(m.mineExpiry) {
		return m.renewCommand()
	}
	m.renewing = true
	ctx := m.opts.Context
	return func() tea.Msg {
		if r := engine.Heartbeat(ctx); r.Reason != "" {
			return claimRenewed{"Claim renewal failed: " + r.Message}
		}
		return claimRenewed{}
	}
}
func (m Model) claimCommand() tea.Cmd {
	row, ok := m.selectedRow()
	if !ok || m.opts.Read == nil {
		return nil
	}
	c, _ := engine.Coordination(m.opts.Context)
	if c == nil && !m.hasWorkspace() {
		return func() tea.Msg { return claimOperationMsg{"Coordination is not configured."} }
	}
	return func() tea.Msg {
		item, err := m.opts.Read(m.opts.Context, row.Ref)
		if err != nil {
			return claimOperationMsg{err.Error()}
		}
		ctx := m.opts.Context
		if m.hasWorkspace() {
			cfg, local := m.opts.Workspace.SourceProject(item.Ref.Source)
			item.Ref.Source = local
			ctx = engine.ProjectContext(ctx, cfg)
		}
		result := engine.ClaimItem(ctx, item)
		if result.Outcome == "held" {
			result = engine.ReleaseItem(ctx, item)
		}
		return claimOperationMsg{fmt.Sprintf("%s: %s", result.Outcome, result.Message)}
	}
}
