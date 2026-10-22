package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Catches coordinate drift between rendered rows/tabs/headers and mouse
// routing, accidental double opens, and lost split widths during drag.
func TestMouseNavigation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := shellFixture(30)
	m.opts.Now = func() time.Time { return now }
	click := func(x, y int) {
		t.Helper()
		next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		m = next.(Model)
	}
	click(10, 5)
	if m.selected != 2 || m.detailOpen {
		t.Fatal("single click did not select row")
	}
	now = now.Add(200 * time.Millisecond)
	click(10, 5)
	if !m.detailOpen || !m.detailFocus {
		t.Fatal("double click did not open detail")
	}
	m = send(m, "esc")
	next, _ := m.Update(tea.MouseWheelMsg{X: 10, Y: 5, Button: tea.MouseWheelDown})
	m = next.(Model)
	if m.selected != 5 {
		t.Fatal("list wheel did not move three rows")
	}
	next, _ = m.Update(tea.MouseWheelMsg{X: 10, Y: 5, Button: tea.MouseWheelUp})
	m = next.(Model)
	if m.selected != 2 {
		t.Fatal("wheel up")
	}
	click(16, 2)
	if !strings.Contains(m.query, "sort:priority") {
		t.Fatal("header click did not sort")
	}
	// View tabs use terminal-cell positions, matching wide project names.
	m.opts.Project = "修正"
	x := ansi.StringWidth("backlog · 修正 · [Next]  ") + 1
	click(x, 0)
	if m.view != 1 {
		t.Fatal("view tab click missed")
	}
	m = shellFixture(3)
	m.opts.Now = func() time.Time { return now }
	m.width = 160
	m = queryKey(m, "enter")
	split := m.listWidth()
	click(split, 10)
	next, _ = m.Update(tea.MouseMotionMsg{X: 65, Y: 10, Button: tea.MouseLeft})
	m = next.(Model)
	if m.splitWidth != 65 {
		t.Fatal("divider did not drag")
	}
	next, _ = m.Update(tea.MouseReleaseMsg{X: 65, Y: 10, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMotionMsg{X: 70, Y: 10, Button: tea.MouseLeft})
	m = next.(Model)
	if m.splitWidth != 65 {
		t.Fatal("release did not end drag")
	}
	// Detail tab hit-testing, then wheel scroll in the detail region.
	m.width = 240
	click(m.listWidth()+1+ansi.StringWidth("[Overview]  ")+1, 3)
	if m.detailTab != tabDescription {
		t.Fatalf("detail tab: %v", m.detailTab)
	}
	m.detailLines = make([]string, 100)
	next, _ = m.Update(tea.MouseWheelMsg{X: 100, Y: 8, Button: tea.MouseWheelDown})
	m = next.(Model)
	if m.detailScroll != 3 {
		t.Fatal("detail wheel did not scroll")
	}
	m = boardFixture(4)
	m.width = 160
	count, width := m.boardGeometry()
	if count < 2 {
		t.Fatal("fixture needs columns")
	}
	click(width+2, 2)
	if m.boardColumn() != 1 {
		t.Fatal("board header click did not focus empty column")
	}
	click(2*(width+1)+1, 3)
	if m.boardColumn() != 2 {
		t.Fatal("board card hit-testing")
	}
	// A modal input must not allow clicks through to underlying rows or divider.
	m = send(m, ":")
	selected := m.selected
	click(2, 4)
	if m.selected != selected || !m.launcher.open {
		t.Fatal("mouse escaped modal")
	}
}
