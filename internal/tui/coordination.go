package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
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

// mineExpiry is when the first of the caller's claims lapses, zero if none.
func (m Model) mineExpiry() time.Time {
	var first time.Time
	for _, at := range m.mineClaims {
		if first.IsZero() || at.Before(first) {
			first = at
		}
	}
	return first
}

// renewCommand renews the caller's claims at half the first one's remaining
// lease, as bl heartbeat -- COMMAND does, so claims last while the TUI runs. A
// run renews its own claim, and a lapsed claim is no longer mine, so neither is.
func (m *Model) renewCommand() tea.Cmd {
	at := m.mineExpiry()
	if at.IsZero() || m.renewing || at.Equal(m.renewTimerAt) {
		return nil
	}
	if _, caller := engine.Coordination(m.opts.Context); caller.Run != "" {
		return nil
	}
	m.renewTimerAt = at
	return tea.Tick(max(at.Sub(m.now())/2, time.Second), func(time.Time) tea.Msg { return claimsRenew(at) })
}
func (m *Model) renew(at time.Time) tea.Cmd {
	if at.Equal(m.renewTimerAt) {
		m.renewTimerAt = time.Time{}
	}
	// The claims changed while the timer ran; schedule for the current ones.
	if !at.Equal(m.mineExpiry()) {
		return m.renewCommand()
	}
	// A timer late past expiry (a sleeping laptop) must not revive a claim.
	now := m.now()
	var refs []model.Ref
	for ref, expires := range m.mineClaims {
		if expires.After(now) {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	m.renewing = true
	ctx := m.opts.Context
	return func() tea.Msg {
		c, caller := engine.Coordination(ctx)
		var failed []string
		for _, ref := range refs {
			if r := engine.Heartbeat(engine.WithCoordination(ctx, c, caller.For(ref))); r.Reason != "" {
				failed = append(failed, ref.ID+": "+r.Message)
			}
		}
		if len(failed) > 0 {
			slices.Sort(failed)
			return claimRenewed{"Claim renewal failed: " + strings.Join(failed, "; ")}
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
