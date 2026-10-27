package tui

import (
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/project"
)

func (m Model) isBacklogMD(source string) bool {
	if m.opts.Workspace == nil {
		return false
	}
	cfg, local := m.opts.Workspace.SourceProject(source)
	for _, configured := range cfg.Sources {
		if configured.ID == local {
			return configured.Type == "backlog-md"
		}
	}
	return false
}

func (m Model) hasWorkspace() bool {
	return m.opts.Workspace != nil && m.opts.Workspace.Workspace != nil
}
func (m Model) scopeProject() string {
	if !m.hasWorkspace() || m.opts.WorkspaceScope {
		return ""
	}
	return m.opts.Workspace.Workspace.Project
}

// Routing only touches the selected project's small configuration, never the
// backlog. Worker callbacks route source I/O independently using the same key.
func (m *Model) routeSelectedProject() {
	if !m.hasWorkspace() {
		return
	}
	row, ok := m.selectedRow()
	if !ok {
		return
	}
	m.routeSourceProject(row.Ref.Source)
}
func (m *Model) routeSourceProject(source string) {
	if !m.hasWorkspace() {
		return
	}
	cfg, _ := m.opts.Workspace.SourceProject(source)
	m.applyProject(cfg)
}
func (m *Model) applyProject(cfg project.Config) {
	m.opts.Context = engine.ProjectContext(m.baseContext, cfg)
	m.opts.Statuses, m.opts.Milestones, m.opts.Labels = cfg.Statuses, cfg.Milestones, cfg.Labels
	m.opts.CommentAuthor, m.opts.EditorDir = cfg.Me.Default, cfg.Discovery.ControlRoot
	// Configured actions must run in their own project, not the anchor's context.
	m.opts.Automation = &cfg
}
