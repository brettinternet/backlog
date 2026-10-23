package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
)

func treeItems(count int) ([]model.Item, []engine.RowGroup) {
	items := make([]model.Item, count)
	group := engine.RowGroup{}
	for i := range items {
		item := editItem(fmt.Sprintf("BL-%06d", i+1), "Tree item")
		item.Category = model.Todo
		if i > 0 {
			parent := items[i-1].Ref
			item.Parent = &parent
		}
		items[i] = item
		group.Rows = append(group.Rows, engine.Row{Ref: item.Ref, Title: item.Title, Status: item.Status, Category: item.Category, Version: item.Version})
	}
	return items, []engine.RowGroup{group}
}

// treePress applies the key's one fold command; update leaves out the
// loading-indicator timers that Update would batch with it.
func treePress(m Model, key string) Model {
	next, cmd := m.update(editKeyMessage(key))
	m = next.(Model)
	if cmd != nil {
		next, _ = m.Update(cmd())
		m = next.(Model)
	}
	return m
}

// Catches h collapsing a leaf or skipping its parent, l skipping expansion,
// label-picker interception, and folds losing the stable selected item.
func TestTreeNavigation(t *testing.T) {
	t.Parallel()
	items, groups := treeItems(6)
	m, _ := editFixture(t, items, model.Capabilities{}, Options{NoColor: true})
	m.views[m.view].Config.Layout = "tree"
	m.groups = groups
	next, _ := m.Update(m.prepare()())
	m = next.(Model)
	for depth := 0; depth < 6; depth++ {
		if m.selected != depth {
			t.Fatalf("depth %d: selected %d", depth, m.selected)
		}
		if depth < 5 {
			m = treePress(m, "h")
			if !m.data.rows[m.selected].collapsed || len(m.data.rows) != depth+1 {
				t.Fatalf("collapse depth %d: %+v", depth, m.data)
			}
			m = treePress(m, "l")
			if m.selected != depth || m.data.rows[m.selected].collapsed || len(m.data.rows) != 6 {
				t.Fatalf("expand depth %d", depth)
			}
		}
		m = treePress(m, "l")
		if m.picker.open {
			t.Fatal("l opened label picker")
		}
	}
	for depth := 5; depth > 0; depth-- {
		if depth < 5 {
			m = treePress(m, "h")
		} // collapse the branch before going up
		m = treePress(m, "h")
		if m.selected != depth-1 {
			t.Fatalf("parent at depth %d: %d", depth, m.selected)
		}
	}
	m = treePress(m, "z")
	m = treePress(m, "R")
	if len(m.data.rows) != 6 {
		t.Fatal("unfold all")
	}
	m = treePress(m, "z")
	m = treePress(m, "a")
	if len(m.data.rows) != 1 {
		t.Fatal("toggle root")
	}
	m = treePress(m, "z")
	m = treePress(m, "M")
	if len(m.data.rows) != 1 {
		t.Fatal("fold all")
	}
	m = treePress(m, "l")
	if len(m.data.rows) != 2 {
		t.Fatal("expand one after fold-all")
	}
}

// Catches missing ancestors, counting only matching/direct children, duplicate
// child edges, and source-less parent references becoming roots.
func TestTreeFilteredAncestorsAndProgress(t *testing.T) {
	t.Parallel()
	items, groups := treeItems(4)
	items[2].Category = model.Done
	items[2].Parent.Source = ""
	items[0].Children = []model.Ref{items[1].Ref, items[1].Ref}
	groups[0].Rows = groups[0].Rows[3:]
	p := prepareTreeItems(groups, items, nil, nil, false)
	if len(p.rows) != 4 {
		t.Fatalf("ancestor closure: %d", len(p.rows))
	}
	for i, r := range p.rows {
		if r.depth != i || r.context != (i < 3) {
			t.Fatalf("row %d: %+v", i, r)
		}
		if r.row.ChildrenTotal != 3-i {
			t.Fatalf("total at %d: %d", i, r.row.ChildrenTotal)
		}
	}
	if p.rows[0].row.ChildrenDone != 1 || p.rows[1].row.ChildrenDone != 1 || p.rows[2].row.ChildrenDone != 0 {
		t.Fatal("nested done progress")
	}
	viewModel := New(Options{NoColor: true, HighContrast: true})
	viewModel.views[viewModel.view].Config.Layout = "tree"
	viewModel.data = p
	viewModel.data.widths = measure(p.rows, nil)
	view := viewModel.View().Content
	if !strings.Contains(view, "· Tree item") || !strings.Contains(view, "▣ 1/3") || strings.Contains(view, "unavailable") {
		t.Fatal(view)
	}
	// Context rows must preserve relation data for the selectable detail pane.
	m, _ := editFixture(t, items, model.Capabilities{}, Options{NoColor: true})
	p = prepareTreeItems(groups, items, m.snapshot, nil, false)
	if relations := p.rows[0].row.Relations; len(relations) != 1 || relations[0].Kind != "children" || relations[0].Ref != items[1].Ref {
		t.Fatalf("context relations: %+v", relations)
	}
	if relations := p.rows[1].row.Relations; len(relations) != 2 || relations[0].Kind != "parent" || relations[1].Kind != "children" {
		t.Fatalf("nested context relations: %+v", relations)
	}
	// A removed parent remains a root rather than dropping the matching subtree.
	items = items[1:]
	p = prepareTreeItems(groups, items, nil, nil, false)
	if len(p.rows) != 3 || p.rows[0].parent != -1 {
		t.Fatal("missing parent hid match")
	}
}

// Catches infinite traversal on corrupt parent cycles and recursive stack growth
// on very deep hierarchies. Each node must occur exactly once.
func TestTreeCyclesAndDeepHierarchy(t *testing.T) {
	t.Parallel()
	items, groups := treeItems(50000)
	parent := items[len(items)-1].Ref
	items[0].Parent = &parent
	p := prepareTreeItems(groups, items, nil, nil, false)
	if len(p.rows) != len(items) {
		t.Fatalf("cycle lost nodes: %d", len(p.rows))
	}
	seen := map[model.Ref]bool{}
	for _, r := range p.rows {
		if seen[r.row.Ref] {
			t.Fatal("duplicate node")
		}
		seen[r.row.Ref] = true
	}
	if p.rows[0].row.ChildrenTotal != 49999 {
		t.Fatal("cycle progress double-counted")
	}
	// Deep title indentation must not allocate proportional to depth on View.
	m := New(Options{NoColor: true})
	m.views[m.view].Config.Layout = "tree"
	m.data = p
	m.selected = 49999
	m.top = 49981
	if len(m.View().Content) > m.width*m.height*4 {
		t.Fatal("unbounded viewport")
	}
}

func BenchmarkTreeRender(b *testing.B) {
	for _, count := range []int{1000, 50000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			items, groups := treeItems(count)
			m := New(Options{NoColor: true})
			m.views[m.view].Config.Layout = "tree"
			m.data = prepareTreeItems(groups, items, nil, nil, false)
			m.selected = count - 1
			m.top = count - 19
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = m.View()
			}
		})
	}
}
