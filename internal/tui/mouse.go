package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/model"
)

type mouseState struct {
	dragging     bool
	lastRef      model.Ref
	lastClick    time.Time
	lastX, lastY int
}

// Mouse routing reuses the renderer's geometry (tab spans, column layout,
// board columns), so hit-testing cannot drift from what is drawn.
func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.modalOpen() {
		return m, nil
	}
	p := msg.Mouse()
	switch msg.(type) {
	case tea.MouseReleaseMsg:
		m.mouse.dragging = false
		return m, nil
	case tea.MouseMotionMsg:
		if m.mouse.dragging && m.split() {
			m.splitWidth = max(30, min(p.X, m.width-31))
			return m, m.scheduleDetail()
		}
		return m, nil
	}
	if p.X < 0 || p.X >= m.width || p.Y < 0 || p.Y >= m.height {
		return m, nil
	}
	split := m.split()
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
			m.keepSelectionVisible()
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
		_, spans, _ := m.tabs()
		for i, s := range spans {
			if p.X >= s.start && p.X < s.end {
				m.goToSelection = nil
				m.switchView(i)
				return m, nil
			}
		}
		return m, nil
	}
	if p.Y == m.height-1 {
		return m, nil
	}
	if split && p.X == m.listWidth() {
		m.mouse.dragging = true
		return m, nil
	}
	if detail {
		m.detailFocus = true
		if p.Y == 3 { // meta, title, then the tab bar
			x, width := p.X-detailPad, m.width
			if split {
				x, width = x-m.listWidth()-1, m.detailWidth()
			}
			_, spans := m.detailTabBar(max(1, width-2*detailPad))
			for i, s := range spans {
				if x >= s.start && x < s.end {
					m.detailTab = detailTab(i)
					m.detailExpanded = false
					m.criteriaCursor = 0
					return m, m.scheduleDetail()
				}
			}
		}
		return m, nil
	}
	m.detailFocus = false
	m.goToSelection = nil
	if m.loading {
		return m, nil
	}
	width := m.width
	if split {
		width = m.listWidth()
	}
	index := -1
	switch {
	case m.isBoard():
		count, columnWidth := m.boardGeometry()
		stride := columnWidth + boardGap
		column := m.board.left + p.X/stride
		if column >= len(m.data.starts) || column >= m.board.left+count || p.X%stride >= columnWidth {
			return m, nil
		}
		start, end := m.data.starts[column], m.boardEnd(column)
		position := m.board.positions[m.data.rows[start].value]
		if p.Y == 1 {
			index = min(end-1, start+1+position.cursor)
		} else {
			index = start + 1 + position.top + (p.Y-2)/cardHeight
			if index >= end {
				return m, nil
			}
		}
	case p.Y == 1:
		if c, ok := m.layout(width).columnAt(p.X); ok && c.field != "" {
			// A second click on the sorted column reverses it.
			value := c.field
			if len(m.shown.Sort) > 0 && m.shown.Sort[0].Field == c.field && !m.shown.Sort[0].Desc {
				value = "-" + c.field
			}
			m.query = setQueryField(m.query, "sort", value)
			m.serial++
			m.project()
		}
		return m, nil
	default:
		index = m.top + p.Y - bodyTop
	}
	if index < 0 || index >= len(m.data.rows) {
		return m, nil
	}
	m.selected = index
	m.keepBoardVisible()
	m.keepSelectionVisible()
	row, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	now := m.now()
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
