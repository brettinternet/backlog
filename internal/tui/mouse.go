package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

type mouseState struct {
	dragging     bool
	lastRef      model.Ref
	lastClick    time.Time
	lastX, lastY int
}

func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.launcher.open || m.picker.open || m.capture.open || m.longEdit.choice != "" || m.editor.open || m.save.open || m.archiveConfirm || m.actionConfirm != nil {
		return m, nil
	}
	p := msg.Mouse()
	switch msg.(type) {
	case tea.MouseReleaseMsg:
		m.mouse.dragging = false
		return m, nil
	case tea.MouseMotionMsg:
		if m.mouse.dragging && m.detailOpen && m.width >= 120 {
			m.splitWidth = max(30, min(p.X, m.width-31))
			return m, m.scheduleDetail()
		}
		return m, nil
	}
	if p.X < 0 || p.X >= m.width || p.Y < 0 || p.Y >= m.height {
		return m, nil
	}
	split := m.detailOpen && m.width >= 120
	detail := m.detailOpen && (!split || p.X > m.listWidth())
	if _, ok := msg.(tea.MouseWheelMsg); ok {
		delta := 3
		if p.Button == tea.MouseWheelUp {
			delta = -3
		} else if p.Button != tea.MouseWheelDown {
			return m, nil
		}
		if detail {
			m.detailFocus = true
			m.scrollDetail(delta)
			return m, nil
		}
		m.detailFocus = false
		m.goToSelection = nil
		if m.isBoard() {
			key := "j"
			if delta < 0 {
				key = "k"
			}
			m.navigateBoard(key, 3, false, "")
		} else {
			m.selected = max(0, min(m.selected+delta, len(m.data.rows)-1))
			m.keepMouseSelectionVisible()
		}
		if m.detailOpen {
			return m, m.scheduleDetail()
		}
		return m, nil
	}
	if _, ok := msg.(tea.MouseClickMsg); !ok || p.Button != tea.MouseLeft {
		return m, nil
	}
	if p.Y == 0 {
		offset := ansi.StringWidth("backlog · " + textutil.Line(m.opts.Project) + " · ")
		for i, v := range m.views {
			name := textutil.Line(v.Config.Name)
			if i == m.view {
				name = "[" + name + "]"
			}
			end := offset + ansi.StringWidth(name)
			if p.X >= offset && p.X < end {
				m.goToSelection = nil
				m.switchView(i)
				return m, nil
			}
			offset = end + 2
		}
		return m, nil
	}
	if split && p.X == m.listWidth() && p.Y >= 2 && p.Y < m.height-2 {
		m.mouse.dragging = true
		return m, nil
	}
	if detail {
		m.detailFocus = true
		if p.Y == 3 {
			x := p.X
			if split {
				x -= m.listWidth() + 1
			}
			offset := 0
			if !strings.Contains(m.detailTabs(), "tab/shift-tab") {
				for i, name := range detailTabNames {
					if detailTab(i) == m.detailTab {
						name = "[" + name + "]"
					}
					end := offset + ansi.StringWidth(name)
					if x >= offset && x < end {
						m.detailTab = detailTab(i)
						m.detailExpanded = false
						m.criteriaCursor = 0
						return m, m.scheduleDetail()
					}
					offset = end + 2
				}
			}
		}
		return m, nil
	}
	m.detailFocus = false
	m.goToSelection = nil
	if p.Y < 2 || p.Y >= m.height-2 || m.loading {
		return m, nil
	}
	index := -1
	if m.isBoard() {
		count, width := m.boardGeometry()
		column := m.board.left + p.X/(width+1)
		if column >= len(m.data.starts) || column >= m.board.left+count || p.X%(width+1) == width {
			return m, nil
		}
		start, end := m.data.starts[column], m.boardEnd(column)
		position := m.board.positions[m.data.rows[start].value]
		if p.Y == 2 {
			index = min(end-1, start+1+position.cursor)
		} else {
			index = start + 1 + position.top + p.Y - 3
			if index >= end {
				return m, nil
			}
		}
	} else if p.Y == 2 {
		if len(m.views[m.view].Config.Columns) > 0 {
			return m, nil
		}
		field := "title"
		width := m.width
		if split {
			width = m.listWidth()
		}
		if width >= 80 && (!m.detailOpen || split) {
			switch {
			case p.X < 15:
				field = "id"
			case p.X < 18:
				field = "priority"
			case p.X < 33:
				field = "status"
			case p.X < 37:
				field = "blockers"
			case p.X < 45:
				field = "children"
			}
		} else if p.X < 15 {
			field = "id"
		}
		// Only sortable query fields are offered by a clickable header.
		if field == "children" || field == "blockers" {
			return m, nil
		}
		m.query = setQueryField(m.query, "sort", field)
		m.project()
		return m, nil
	} else {
		index = m.top + p.Y - 3
	}
	if index < 0 || index >= len(m.data.rows) {
		return m, nil
	}
	m.selected = index
	m.keepBoardVisible()
	m.keepMouseSelectionVisible()
	row, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	now := time.Now()
	if m.opts.Now != nil {
		now = m.opts.Now()
	}
	double := row.Ref == m.mouse.lastRef && p.X == m.mouse.lastX && p.Y == m.mouse.lastY && now.Sub(m.mouse.lastClick) >= 0 && now.Sub(m.mouse.lastClick) <= 400*time.Millisecond
	m.mouse.lastClick, m.mouse.lastRef, m.mouse.lastX, m.mouse.lastY = now, row.Ref, p.X, p.Y
	if double {
		m.detailOpen = true
		m.detailFocus = true
		m.mouse.lastRef = model.Ref{}
	}
	if m.detailOpen {
		return m, m.scheduleDetail()
	}
	return m, nil
}
func (m *Model) keepMouseSelectionVisible() {
	page := max(1, m.height-5)
	if m.selected < m.top {
		m.top = m.selected
	}
	if m.selected >= m.top+page {
		m.top = m.selected - page + 1
	}
	m.top = max(0, min(m.top, max(0, len(m.data.rows)-page)))
}
