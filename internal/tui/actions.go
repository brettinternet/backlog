package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/textutil"
)

type actionChoicesMsg struct {
	serial  uint64
	choices []engine.ActionChoice
	err     error
}
type actionPreparedMsg struct {
	serial uint64
	choice engine.ActionChoice
	err    error
}
type actionFinishedMsg struct {
	message string
	err     error
}

func (m Model) openConfiguredActions() (tea.Model, tea.Cmd) {
	m.launcherSerial++
	m.actionCatalogSerial++
	m.launcher = launcherState{open: true, loading: true}
	return m, m.loadConfiguredActions()
}

func (m Model) loadConfiguredActions() tea.Cmd {
	row, ok := m.selectedRow()
	cfg, ctx, serial := m.opts.Automation, m.opts.Context, m.actionCatalogSerial
	return func() tea.Msg {
		if cfg == nil {
			return actionChoicesMsg{serial: serial, err: fmt.Errorf("no actions configured")}
		}
		if !ok {
			return actionChoicesMsg{serial: serial, err: fmt.Errorf("select an item")}
		}
		choices, err := engine.ActionChoices(engine.WithEventCWD(ctx, m.opts.EditorDir), *cfg, row.Ref.Source+":"+row.Ref.ID)
		return actionChoicesMsg{serial: serial, choices: choices, err: err}
	}
}

func (m Model) receiveActionChoices(msg actionChoicesMsg) (tea.Model, tea.Cmd) {
	if !m.launcher.open || m.launcher.goTo || msg.serial != m.actionCatalogSerial {
		return m, nil
	}
	m.launcher.loading = false
	if msg.err != nil {
		m.launcher.err = boundedLine(msg.err.Error())
		return m, nil
	}
	for _, choice := range msg.choices {
		for _, a := range m.keymap {
			if a.id == "action:"+choice.Name {
				m.launcher.all = append(m.launcher.all, launcherRow{label: textutil.Line(choice.Name), binding: strings.Join(a.bindings, ", "), reason: textutil.Line(choice.Disabled), action: a, actionRef: choice.Invocation.Event.Ref})
				break
			}
		}
	}
	m.launcher.rows = m.launcher.all
	return m, m.filterLauncher()
}

func (m Model) prepareConfiguredAction(name string) (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok || m.opts.Automation == nil {
		m.notice = "Select an item with configured actions."
		return m, nil
	}
	return m.prepareConfiguredActionForID(name, row.Ref.Source+":"+row.Ref.ID)
}

func (m Model) prepareConfiguredActionForID(name, id string) (tea.Model, tea.Cmd) {
	m.actionSerial++
	serial, cfg, ctx, cwd := m.actionSerial, *m.opts.Automation, m.opts.Context, m.opts.EditorDir
	return m, func() tea.Msg {
		choice, err := engine.PrepareAction(engine.WithEventCWD(ctx, cwd), cfg, name, id, true)
		return actionPreparedMsg{serial: serial, choice: choice, err: err}
	}
}

func (m Model) receiveActionPrepared(msg actionPreparedMsg) (tea.Model, tea.Cmd) {
	if msg.serial != m.actionSerial || m.launcher.open || m.capture.open || m.picker.open || m.editor.open || m.save.open || m.archiveConfirm || m.longEdit.path != "" || m.actionConfirm != nil {
		return m, nil
	}
	if msg.err != nil {
		m.notice = "Action: " + boundedLine(msg.err.Error())
		return m, nil
	}
	if msg.choice.Confirm {
		m.actionConfirm = &msg.choice
		return m, nil
	}
	return m, m.runConfiguredAction(msg.choice)
}

func actionMessage(result hooks.ActionResult, err error) tea.Msg {
	if err != nil {
		return actionFinishedMsg{err: err}
	}
	if result.Detached {
		return actionFinishedMsg{message: "Action started."}
	}
	return actionFinishedMsg{message: "Action " + result.Event.Outcome + ". " + boundedLine(result.Message)}
}

func (m Model) runConfiguredAction(choice engine.ActionChoice) tea.Cmd {
	cfg, ctx := *m.opts.Automation, m.opts.Context
	if choice.Detach {
		return func() tea.Msg {
			result, err := engine.ExecuteAction(ctx, cfg, choice)
			return actionMessage(result, err)
		}
	}
	command := &attachedAction{ctx: ctx, cfg: cfg, invocation: choice.Invocation}
	return tea.Exec(command, func(err error) tea.Msg { return actionMessage(command.result, err) })
}

// Bubble Tea releases the terminal around Run and restores it before the callback.
// SetStdin deliberately ignores the terminal: all actions receive JSON stdin.
type attachedAction struct {
	ctx         context.Context
	cfg         project.Config
	invocation  hooks.ActionInvocation
	out, errOut io.Writer
	result      hooks.ActionResult
}

func (a *attachedAction) SetStdin(io.Reader)    {}
func (a *attachedAction) SetStdout(w io.Writer) { a.out = w }
func (a *attachedAction) SetStderr(w io.Writer) { a.errOut = w }
func (a *attachedAction) Run() error {
	var err error
	a.result, err = hooks.RunAction(a.ctx, a.cfg, a.invocation, a.out, a.errOut)
	return err
}
