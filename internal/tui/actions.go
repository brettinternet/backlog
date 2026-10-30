package tui

import (
	"cmp"
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
	resumes []engine.ResumeChoice
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

// openConfiguredActions opens the ! menu: the item's configured actions and
// agent sessions, then the built-in actions likely to apply. A History session
// row narrows it to that session.
func (m Model) openConfiguredActions() (tea.Model, tea.Cmd) {
	m.keyPrefix = ""
	m.count = 0
	m.inputSerial++
	m.launcherSerial++
	var rows []launcherRow
	if m.actionSession == "" {
		rows = m.itemMenuRows()
	}
	m.launcher = launcherState{open: true, item: true, rows: rows, all: rows}
	if _, ok := m.selectedRow(); !ok {
		m.launcher.err = "select an item"
		return m, nil
	}
	if m.opts.Automation == nil {
		return m, nil
	}
	m.actionCatalogSerial++
	m.launcher.loading = true
	return m, m.loadConfiguredActions()
}

func (m Model) loadConfiguredActions() tea.Cmd {
	row, ok := m.selectedRow()
	cfg, ctx, serial, session := m.opts.Automation, m.opts.Context, m.actionCatalogSerial, m.actionSession
	if m.hasWorkspace() {
		_, row.Ref.Source = m.opts.Workspace.SourceProject(row.Ref.Source)
	}
	return func() tea.Msg {
		if cfg == nil {
			return actionChoicesMsg{serial: serial, err: fmt.Errorf("no actions configured")}
		}
		if !ok {
			return actionChoicesMsg{serial: serial, err: fmt.Errorf("select an item")}
		}
		ctx, id := engine.WithEventCWD(ctx, m.opts.EditorDir), row.Ref.Source+":"+row.Ref.ID
		var choices []engine.ActionChoice
		var err error
		if len(cfg.Actions) > 0 {
			choices, err = engine.ActionChoices(ctx, *cfg, id)
		}
		var resumes []engine.ResumeChoice
		var resumeErr error
		if session != "" {
			resumes, resumeErr = engine.SessionChoices(ctx, *cfg, id, session)
		} else {
			resumes, resumeErr = engine.ResumeChoices(ctx, *cfg, id, "")
		}
		return actionChoicesMsg{serial: serial, choices: choices, resumes: resumes, err: cmp.Or(err, resumeErr)}
	}
}

func (m Model) receiveActionChoices(msg actionChoicesMsg) (tea.Model, tea.Cmd) {
	if !m.launcher.open || m.launcher.goTo || msg.serial != m.actionCatalogSerial {
		return m, nil
	}
	m.launcher.loading = false
	if msg.err != nil {
		m.launcher.err = boundedLine(msg.err.Error())
	}
	var found []launcherRow
	for _, choice := range msg.choices {
		for _, a := range m.keymap {
			if a.id == "action:"+choice.Name {
				found = append(found, launcherRow{label: textutil.Line(choice.Name), keywords: strings.Join(a.bindings, " "), reason: textutil.Line(choice.Disabled), action: a, actionRef: choice.Invocation.Event.Ref, group: "Actions"})
				break
			}
		}
	}
	for _, choice := range msg.resumes {
		label := strings.ToUpper(choice.Command[:1]) + choice.Command[1:] + " " + cmp.Or(choice.Session.Agent, "agent") + " · " + age(choice.Session.LastSeen, m.now())
		found = append(found, launcherRow{label: textutil.Line(label), reason: textutil.Line(choice.Disabled), resume: &choice, group: "Sessions"})
	}
	// Menus lead with what only this item has; help keeps them as its last
	// sections.
	if m.launcher.help {
		m.launcher.all = append(m.launcher.all, found...)
	} else {
		m.launcher.all = append(found, m.launcher.all...)
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
	if m.hasWorkspace() {
		_, row.Ref.Source = m.opts.Workspace.SourceProject(row.Ref.Source)
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
	if msg.serial != m.actionSerial || m.launcher.open || m.capture.open || m.picker.open || m.editor.open || m.save.open || m.find.open || m.archiveConfirm || m.longEdit.path != "" || m.actionConfirm != nil {
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

// runResume reopens a recorded agent session: in the background when its
// agent detaches, otherwise attached to the terminal like $EDITOR.
func (m Model) runResume(choice engine.ResumeChoice) tea.Cmd {
	cfg, ctx := *m.opts.Automation, m.opts.Context
	if choice.Detach {
		return func() tea.Msg {
			result, err := hooks.StartAction(cfg, choice.Invocation)
			return resumeMessage(choice, result, err)
		}
	}
	command := &attachedAction{ctx: ctx, cfg: cfg, invocation: choice.Invocation, agent: true}
	return tea.Exec(command, func(err error) tea.Msg { return resumeMessage(choice, command.result, err) })
}

// resumeMessage reports a failed agent command's exit status and stderr tail.
func resumeMessage(choice engine.ResumeChoice, result hooks.ActionResult, err error) tea.Msg {
	label := strings.ToUpper(choice.Command[:1]) + choice.Command[1:]
	switch {
	case err != nil:
		return actionFinishedMsg{err: err}
	case result.Detached:
		return actionFinishedMsg{message: label + " started."}
	case result.Event.Outcome == "ok":
		return actionFinishedMsg{message: label + " finished."}
	}
	message := label + " " + result.Event.Outcome
	if code := result.Event.ExitCode; code != nil && *code > 0 {
		message += fmt.Sprintf(" (exit %d)", *code)
	}
	return actionFinishedMsg{message: message + ". " + boundedLine(cmp.Or(result.Event.Message, result.Message))}
}

// Bubble Tea releases the terminal around Run and restores it before the callback.
// SetStdin ignores the terminal for actions, which receive JSON stdin; an
// agent session gets the terminal.
type attachedAction struct {
	ctx         context.Context
	cfg         project.Config
	invocation  hooks.ActionInvocation
	agent       bool
	in          io.Reader
	out, errOut io.Writer
	result      hooks.ActionResult
}

func (a *attachedAction) SetStdin(r io.Reader)  { a.in = r }
func (a *attachedAction) SetStdout(w io.Writer) { a.out = w }
func (a *attachedAction) SetStderr(w io.Writer) { a.errOut = w }
func (a *attachedAction) Run() error {
	var err error
	if a.agent {
		a.result, err = hooks.RunAgent(a.ctx, a.cfg, a.invocation, a.in, a.out, a.errOut)
		return err
	}
	a.result, err = hooks.RunAction(a.ctx, a.cfg, a.invocation, a.out, a.errOut)
	return err
}
