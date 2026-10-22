package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/textutil"
)

type selection struct {
	Ref    model.Ref `json:"ref"`
	Group  string    `json:"group,omitempty"`
	Header bool      `json:"header,omitempty"`
}
type viewState struct {
	ID        string             `json:"id"`
	Config    project.ViewConfig `json:"config"`
	Selection selection          `json:"selection"`
	folded    *foldState
	foldAll   bool
}

// Session is disposable per-project state, not a source of backlog truth.
type Session struct {
	Version    int         `json:"version"`
	Active     string      `json:"active"`
	Views      []viewState `json:"views"`
	DetailTab  int         `json:"detailTab"`
	DetailOpen bool        `json:"detailOpen"`
	SplitWidth int         `json:"splitWidth"`
}

func LoadSession(cfg project.Config) (*Session, string) {
	data, err := project.ReadUIState(cfg)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ""
	}
	var s Session
	if err == nil {
		err = json.Unmarshal(data, &s)
	}
	if err == nil {
		if s.Version != 1 || len(s.Views) > 1000 || s.DetailTab < 0 || s.DetailTab >= len(detailTabNames) || s.SplitWidth < 0 || s.SplitWidth > 10000 {
			err = fmt.Errorf("invalid UI state")
		}
		seen := map[string]bool{}
		for _, v := range s.Views {
			if v.ID == "" || seen[v.ID] {
				err = fmt.Errorf("invalid view identity")
			}
			seen[v.ID] = true
			if _, e := query.Parse(v.Config.Query, query.Options{}); e != nil {
				err = e
			}
		}
	}
	if err != nil {
		return nil, "UI state ignored: " + textutil.Line(err.Error())
	}
	return &s, ""
}

// SaveSession is called after Program.Run, outside Bubble Tea callbacks.
func (m Model) SaveSession(cfg project.Config) error {
	m.captureView()
	data, err := json.Marshal(Session{Version: 1, Active: m.views[m.view].ID, Views: m.views, DetailTab: int(m.detailTab), DetailOpen: m.detailOpen, SplitWidth: m.splitWidth})
	if err != nil {
		return err
	}
	return project.WriteUIState(cfg, data)
}
func viewQuery(v project.ViewConfig) string {
	text := v.Query
	if v.Sort != "" {
		text = setQueryField(text, "sort", v.Sort)
	}
	if v.Group != "" {
		text = setQueryField(text, "group", v.Group)
	}
	return text
}
func (m *Model) initViews() {
	for i, name := range viewNames {
		m.views = append(m.views, viewState{ID: "builtin:" + name, Config: project.ViewConfig{Name: name, Query: viewQueries[i], Layout: "list"}})
	}
	for _, v := range m.opts.Views {
		v.Query = viewQuery(v.ViewConfig)
		v.Sort = ""
		v.Group = ""
		m.views = append(m.views, viewState{ID: v.ID, Config: v.ViewConfig})
	}
	if s := m.opts.Session; s != nil {
		for i := range m.views {
			for _, saved := range s.Views {
				if saved.ID == m.views[i].ID {
					m.views[i].Config = saved.Config
					m.views[i].Selection = saved.Selection
					break
				}
			}
			if m.views[i].ID == s.Active {
				m.view = i
			}
		}
		m.detailTab = detailTab(s.DetailTab)
		m.detailOpen = s.DetailOpen
		m.splitWidth = s.SplitWidth
	}
	m.query = m.views[m.view].Config.Query
	m.restore = &m.views[m.view].Selection
}
func (m *Model) captureView() {
	v := &m.views[m.view]
	v.Config.Query = m.query
	v.folded = m.folded
	v.foldAll = m.foldAll
	if m.restore != nil {
		v.Selection = *m.restore
		return
	}
	if m.selected >= 0 && m.selected < len(m.data.rows) {
		r := m.data.rows[m.selected]
		v.Selection = selection{Ref: r.row.Ref, Group: r.value, Header: r.header != ""}
	}
}
func (m Model) listWidth() int {
	if m.splitWidth == 0 {
		return (m.width - 3) / 2
	}
	return max(30, min(m.splitWidth, m.width-31))
}

type savePrompt struct {
	open, project, busy bool
	name                string
}
type viewSaved struct {
	view  project.SavedView
	state viewState
	err   error
}

func (m Model) updateSave(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.save.busy {
		return m, nil
	}
	switch key.String() {
	case "esc":
		m.save = savePrompt{}
	case "tab":
		m.save.project = !m.save.project
	case "ctrl+u":
		m.save.name = ""
	case "backspace":
		if len(m.save.name) > 0 {
			_, n := utf8.DecodeLastRuneInString(m.save.name)
			m.save.name = m.save.name[:len(m.save.name)-n]
		}
	case "enter":
		name := strings.TrimSpace(m.save.name)
		if name == "" {
			m.notice = "view name must not be empty"
			return m, nil
		}
		if m.opts.SaveView == nil {
			m.notice = "saving views is unavailable"
			return m, nil
		}
		m.captureView()
		state := m.views[m.view]
		state.Config.Name = name
		state.Config.Sort = ""
		state.Config.Group = ""
		destination, save := m.save.project, m.opts.SaveView
		m.save.busy = true
		return m, func() tea.Msg {
			v, err := save(state.Config, destination)
			return viewSaved{view: v, state: state, err: err}
		}
	default:
		if key.Text != "" && !strings.ContainsAny(key.Text, "\x00\x1b\r\n") && len(m.save.name)+len(key.Text) <= 200 {
			m.save.name += key.Text
		}
	}
	return m, nil
}
func (m *Model) saved(msg viewSaved) {
	m.save = savePrompt{}
	if msg.err != nil {
		m.notice = "save failed: " + textutil.Line(msg.err.Error())
		return
	}
	m.captureView()
	previous := m.views[m.view].ID
	state := msg.state
	state.ID = msg.view.ID
	index := -1
	for i, v := range m.views {
		if v.ID == state.ID {
			index = i
			break
		}
	}
	if index < 0 {
		index = len(m.views)
		m.views = append(m.views, state)
	} else {
		m.views[index] = state
	}
	rank := func(id string) int {
		switch {
		case strings.HasPrefix(id, "builtin:"):
			return 0
		case strings.HasPrefix(id, "project:"):
			return 1
		case strings.HasPrefix(id, "user:"):
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(m.views, func(i, j int) bool { return rank(m.views[i].ID) < rank(m.views[j].ID) })
	for i, v := range m.views {
		if v.ID == previous {
			m.view = i
		}
		if v.ID == state.ID {
			index = i
		}
	}
	m.switchView(index)
	m.notice = "Saved " + textutil.Line(msg.view.Name)
}
