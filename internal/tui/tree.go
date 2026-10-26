package tui

import (
	"context"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
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
			row := engine.Row{Ref: item.Ref, Version: item.Version, Title: item.Title, Status: item.Status, Category: item.Category, Priority: item.Priority, Type: item.Type, Milestone: item.Milestone, Due: item.Due, URL: item.URL, Assignees: item.Assignees, Labels: item.Labels, Names: item.Names}
			row.SetCounts(item)
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
		prefix        string // ancestor continuation guide; see extendGuide
		last          bool   // last included sibling
	}
	pending := make([]entry, 0, len(roots))
	for i := len(roots) - 1; i >= 0; i-- {
		pending = append(pending, entry{ref: roots[i], parent: -1})
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
		guide, childPrefix := "", ""
		if e.depth > 0 {
			guide = e.prefix + map[bool]string{false: "T", true: "L"}[e.last]
			childPrefix = extendGuide(e.prefix, e.last)
		}
		p.items++
		p.rows = append(p.rows, screenRow{row: row, value: key, guide: guide, ordinal: p.items, depth: e.depth, parent: e.parent, branch: hasChildren, collapsed: folded, context: !matches[e.ref]})
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
			last := true
			for i := len(kids) - 1; i >= 0; i-- {
				if include[kids[i]] {
					pending = append(pending, entry{ref: kids[i], depth: e.depth + 1, parent: index, prefix: childPrefix, last: last})
					last = false
				}
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

// maxGuide caps stored guide levels, so a deep chain costs O(rows), not
// O(rows x depth). Deeper levels collapse into a leading ellipsis.
const maxGuide = 24

// extendGuide appends one ancestor level: c draws a continuing branch line and
// s leaves space below a last child. A leading < marks trimmed levels.
func extendGuide(prefix string, last bool) string {
	level := "c"
	if last {
		level = "s"
	}
	s := prefix + level
	if len(s) > maxGuide {
		s = "<" + s[len(s)-maxGuide+1:]
	}
	return s
}

// treePrefix draws connectors two cells per level (T and L are the row's own
// branch), capped to a quarter of width, followed by the fold glyph.
func (m Model) treePrefix(r screenRow, width int) string {
	t := m.theme
	guide := r.guide
	if limit := max(1, width/8); len(guide) > limit {
		guide = "<" + guide[len(guide)-limit+1:]
	}
	var b strings.Builder
	for i := 0; i < len(guide); i++ {
		switch guide[i] {
		case '<':
			b.WriteString("… ")
		case 'c':
			b.WriteString("│ ")
		case 's':
			b.WriteString("  ")
		case 'T':
			b.WriteString("├─")
		case 'L':
			b.WriteString("└─")
		}
	}
	lines := t.faint.render(b.String())
	switch {
	case r.branch && r.collapsed:
		lines += t.muted.render("▸")
	case r.branch:
		lines += t.muted.render("▾")
	case r.depth > 0:
		lines += t.faint.render("─")
	default:
		lines += " "
	}
	lines += " "
	if r.context {
		lines += t.muted.render("· ")
	}
	return lines
}
