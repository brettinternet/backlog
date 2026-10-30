package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

// A slow preparation must not consume confirmation input intended for a newer modal.
func TestActionPreparationCannotOverlayNewModal(t *testing.T) {
	t.Parallel()
	choice := engine.ActionChoice{Name: "confirm-work", Confirm: true}
	m := shellFixture(3)
	m.actionSerial = 1
	// A new query opens while the action's worker is preparing its invocation.
	next, _ := m.Update(tea.KeyPressMsg{Code: '/'})
	m = next.(Model)
	next, cmd := m.Update(actionPreparedMsg{serial: 1, choice: choice})
	m = next.(Model)
	if cmd != nil || m.actionConfirm != nil || !m.editor.open {
		t.Fatal("stale preparation installed a confirmation over query input")
	}
	if strings.Contains(m.View().Content, "Run confirm-work") {
		t.Fatal("stale prompt became visible")
	}
	// Independently arriving modal state also wins, even without a new key.
	m = shellFixture(3)
	m.actionSerial = 2
	m.capture.open = true
	next, cmd = m.Update(actionPreparedMsg{serial: 2, choice: choice})
	m = next.(Model)
	if cmd != nil || m.actionConfirm != nil || !m.capture.open {
		t.Fatal("preparation installed an invisible confirmation over quick-add")
	}
}

// Typing before worker discovery completes must not discard the action catalog,
// and an action menu must keep its target even when live selection changes.
func TestActionDiscoverySurvivesEarlyTyping(t *testing.T) {
	t.Parallel()
	m := shellFixture(3)
	m.opts.Automation = &project.Config{Settings: project.Settings{Actions: []project.ActionConfig{{Name: "work"}}}}
	m.keymap, m.keyError = buildKeymap(nil, m.opts.Automation.Actions...)
	next, _ := m.openConfiguredActions()
	m = next.(Model)
	serial := m.actionCatalogSerial
	next, _ = m.updateLauncher("w", "w")
	m = next.(Model)
	next, filter := m.receiveActionChoices(actionChoicesMsg{serial: serial, choices: []engine.ActionChoice{{Name: "work", Invocation: hooks.ActionInvocation{Event: hooks.Event{Ref: "tasks:BL-1"}}}}})
	m = next.(Model)
	if filter == nil {
		t.Fatal("early typing discarded discovery")
	}
	next, _ = m.Update(filter())
	m = next.(Model)
	if len(m.launcher.rows) != 1 || m.launcher.rows[0].actionRef != "tasks:BL-1" || m.launcher.query.text != "w" {
		t.Fatalf("lost catalog or captured target: %+v", m.launcher)
	}
}

// A reopened menu shows the item's last sessions at once, but never another
// item's: picking one would resume the wrong agent session.
func TestItemChoicesCacheByItem(t *testing.T) {
	t.Parallel()
	m := shellFixture(3)
	m.opts.Automation = &project.Config{}
	m.selected = 1
	open := func() Model {
		next, _ := m.openConfiguredActions()
		return next.(Model)
	}
	m = open()
	if !m.launcher.hidden {
		t.Fatalf("first open drew the menu before its sessions: %+v", m.launcher)
	}
	next, _ := m.receiveActionChoices(actionChoicesMsg{serial: m.actionCatalogSerial, key: m.choicesKey(), resumes: []engine.ResumeChoice{{Command: "resume", Session: model.ItemSession{Agent: "claude"}}}})
	m = next.(Model)
	if m.launcher.hidden || m.launcher.rows[0].resume == nil {
		t.Fatalf("sessions not shown: %+v", m.launcher)
	}
	m.launcher = launcherState{}
	m.selected = 2
	if m = open(); !m.launcher.hidden || slices.ContainsFunc(m.launcher.rows, func(r launcherRow) bool { return r.resume != nil }) {
		t.Fatal("another item's sessions were offered")
	}
	m.launcher = launcherState{}
	m.selected = 1
	if m = open(); m.launcher.hidden || m.launcher.rows[0].resume == nil {
		t.Fatal("reopened menu waited for sessions it had")
	}
}
