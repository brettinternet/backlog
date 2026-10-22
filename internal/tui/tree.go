package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
)

func treeKey(ref model.Ref) string { return ref.Source + "\x00" + ref.ID }

func (m Model) isTree() bool { return m.views[m.view].Config.Layout == "tree" }

// All hierarchy work runs in prepare's command. Each node has one parent;
// malformed cycles are cut deterministically rather than recursed through.
func prepareTree(groups []engine.RowGroup, snapshot *engine.Snapshot, folds *foldState, foldAll bool) prepared {
	items := []model.Item{}
	if snapshot != nil {
		items = snapshot.Items()
	}
	return prepareTreeItems(groups, items, snapshot, folds, foldAll)
}

func prepareTreeItems(groups []engine.RowGroup, items []model.Item, snapshot *engine.Snapshot, folds *foldState, foldAll bool) prepared {
	rows := map[model.Ref]engine.Row{}
	matches := map[model.Ref]bool{}
	order := []model.Ref{}
	for _, group := range groups {
		for _, row := range group.Rows {
			if !matches[row.Ref] {
				order = append(order, row.Ref)
			}
			matches[row.Ref] = true
			rows[row.Ref] = row
		}
	}
	sort.Slice(items, func(i, j int) bool { return treeKey(items[i].Ref) < treeKey(items[j].Ref) })
	parents := map[model.Ref]model.Ref{}
	var blockingBy, childrenBy map[model.Ref][]model.Ref
	if snapshot != nil {
		blockingBy, childrenBy, _ = engine.ReverseRelations(context.Background(), snapshot)
	}
	normalize := func(ref, owner model.Ref) model.Ref {
		if ref.Source == "" {
			ref.Source = owner.Source
		}
		return ref
	}
	for _, item := range items {
		if _, ok := rows[item.Ref]; !ok {
			row := engine.Row{Ref: item.Ref, Version: item.Version, Title: item.Title, Status: item.Status, Category: item.Category, Priority: item.Priority, Type: item.Type, Milestone: item.Milestone, Due: item.Due, URL: item.URL, Assignees: item.Assignees, Labels: item.Labels}
			if snapshot != nil {
				_, edit, _ := snapshot.Get(item.Ref)
				row.Pending, row.Error = edit.Pending, edit.Error
				if edit.Local != nil {
					row.ErrorLocalVersion = edit.Local.Version
				}
				if edit.Remote != nil {
					row.ErrorRemoteVersion = edit.Remote.Version
				}
				row.ReadinessReason = "not available for context ancestor"
				row.Relations = engine.RelationRows(item, snapshot, blockingBy, childrenBy)
				row.RelationVersion = engine.RelationVersion(row.Relations)
			}
			rows[item.Ref] = row
			order = append(order, item.Ref)
		}
		if item.Parent != nil {
			parents[item.Ref] = normalize(*item.Parent, item.Ref)
		}
	}
	// Explicit child edges fill only absent parent fields. Sorted owners make
	// inconsistent provider data deterministic; an explicit parent wins.
	for _, item := range items {
		for _, child := range item.Children {
			child = normalize(child, item.Ref)
			if _, ok := parents[child]; !ok {
				parents[child] = item.Ref
			}
		}
	}
	for child, parent := range parents {
		if _, exists := rows[parent]; !exists {
			delete(parents, child)
		}
	}
	// Resolve cycles with three-color traversal (iterative for deep trees).
	state := map[model.Ref]uint8{}
	for _, ref := range order {
		chain := []model.Ref{}
		current := ref
		for state[current] == 0 {
			state[current] = 1
			chain = append(chain, current)
			parent, ok := parents[current]
			if !ok {
				break
			}
			if state[parent] == 1 {
				delete(parents, current)
				break
			}
			current = parent
		}
		for _, node := range chain {
			state[node] = 2
		}
	}
	children := map[model.Ref][]model.Ref{}
	roots := []model.Ref{}
	for _, ref := range order {
		if parent, ok := parents[ref]; ok {
			children[parent] = append(children[parent], ref)
		} else {
			roots = append(roots, ref)
		}
	}
	// A single preorder supports both descendant aggregation and ancestor closure.
	preorder := make([]model.Ref, 0, len(rows))
	stack := append([]model.Ref{}, roots...)
	for len(stack) > 0 {
		ref := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		preorder = append(preorder, ref)
		stack = append(stack, children[ref]...)
	}
	include := map[model.Ref]bool{}
	total, done := map[model.Ref]int{}, map[model.Ref]int{}
	for i := len(preorder) - 1; i >= 0; i-- {
		ref := preorder[i]
		include[ref] = include[ref] || matches[ref]
		if parent, ok := parents[ref]; ok {
			include[parent] = include[parent] || include[ref]
			total[parent] += 1 + total[ref]
			done[parent] += done[ref]
			if row := rows[ref]; row.Category == model.Done || row.Category == model.Canceled {
				done[parent]++
			}
		}
	}
	collapsed := map[string]bool{}
	for f := folds; f != nil; f = f.previous {
		collapsed[f.group] = !collapsed[f.group]
	}
	type entry struct {
		ref           model.Ref
		depth, parent int
	}
	pending := make([]entry, 0, len(roots))
	for i := len(roots) - 1; i >= 0; i-- {
		pending = append(pending, entry{roots[i], 0, -1})
	}
	p := prepared{}
	for len(pending) > 0 {
		e := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if !include[e.ref] {
			continue
		}
		row := rows[e.ref]
		row.ChildrenDone, row.ChildrenTotal = done[e.ref], total[e.ref]
		key := treeKey(e.ref)
		folded := foldAll != collapsed[key]
		hasChildren := false
		for _, child := range children[e.ref] {
			if include[child] {
				hasChildren = true
				break
			}
		}
		index := len(p.rows)
		p.rows = append(p.rows, screenRow{row: row, value: key, depth: e.depth, parent: e.parent, branch: hasChildren, collapsed: folded, context: !matches[e.ref]})
		p.items++
		if row.Pending {
			p.pending++
		}
		if row.Error != "" {
			p.failed++
		}
		if e.parent < 0 {
			p.starts = append(p.starts, index)
		}
		if hasChildren && !folded {
			kids := children[e.ref]
			for i := len(kids) - 1; i >= 0; i-- {
				pending = append(pending, entry{kids[i], e.depth + 1, index})
			}
		}
	}
	return p
}

func (m *Model) toggleTree() tea.Cmd {
	if m.selected < 0 || m.selected >= len(m.data.rows) || !m.data.rows[m.selected].branch {
		return nil
	}
	m.folded = &foldState{group: m.data.rows[m.selected].value, previous: m.folded}
	m.serial++
	return m.prepare()
}
func (m *Model) navigateTree(key string) tea.Cmd {
	if m.selected < 0 || m.selected >= len(m.data.rows) {
		return nil
	}
	row := m.data.rows[m.selected]
	switch key {
	case "h", "left":
		if row.branch && !row.collapsed {
			return m.toggleTree()
		}
		if row.parent >= 0 {
			m.selected = row.parent
		}
	case "l", "right":
		if row.branch {
			if row.collapsed {
				return m.toggleTree()
			}
			m.selected++
		}
	}
	if m.detailOpen {
		return m.scheduleDetail()
	}
	return nil
}
func (m Model) treeTitle(r screenRow) string {
	glyph := "  "
	if r.branch {
		glyph = "▾ "
		if r.collapsed {
			glyph = "▸ "
		}
	}
	// Indentation is capped by viewport width, never hierarchy depth.
	prefix := strings.Repeat(" ", min(r.depth, max(0, m.width/4))*2) + glyph
	if r.context {
		prefix += "· "
	}
	progress := ""
	if r.row.ChildrenTotal > 0 {
		progress = fmt.Sprintf(" ▣ %d/%d", r.row.ChildrenDone, r.row.ChildrenTotal)
	}
	return prefix + r.row.Title + progress
}
func (m Model) treeContext(index int, line string) string {
	if !m.isTree() || index < 0 || index >= len(m.data.rows) || !m.data.rows[index].context || m.opts.NoColor {
		return line
	}
	style := lipgloss.NewStyle()
	if m.opts.HighContrast {
		style = style.Underline(true)
	} else {
		style = style.Faint(true)
	}
	return style.Render(line)
}
