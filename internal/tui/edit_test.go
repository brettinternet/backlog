package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

type editFakeSource struct {
	mu         sync.Mutex
	items      map[string]model.Item
	capability model.Capabilities
	mutations  []model.Mutation
	sequence   int
}

func newEditFakeSource(items []model.Item, caps model.Capabilities) *editFakeSource {
	byID := make(map[string]model.Item, len(items))
	for _, item := range items {
		byID[item.Ref.ID] = item
	}
	return &editFakeSource{items: byID, capability: caps}
}
func (s *editFakeSource) Info() source.Info {
	return source.Info{ID: "tasks", Capabilities: s.capability}
}
func (s *editFakeSource) Load(context.Context, source.Cursor) (source.Delta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]model.Item, 0, len(s.items))
	for _, item := range s.items {
		items = append(items, item)
	}
	return source.Delta{Coverage: source.Complete, Items: items}, nil
}
func (s *editFakeSource) Read(_ context.Context, id string) (model.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return model.Item{}, source.Refuse("not-found", id)
	}
	return item, nil
}
func (s *editFakeSource) Prepare(_ context.Context, mutation model.Mutation) (source.Prepared, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[mutation.Ref.ID]
	if !ok {
		return source.Prepared{}, source.Refuse("not-found", mutation.Ref.ID)
	}
	if mutation.ExpectedVersion != item.Version {
		return source.Prepared{}, source.Refuse("conflict", "fake version changed")
	}
	preview, err := fakeApply(item, mutation)
	if err != nil {
		return source.Prepared{}, err
	}
	return source.Prepared{Item: preview, Inverse: fakeInverse(item, mutation)}, nil
}
func (s *editFakeSource) Apply(_ context.Context, mutation model.Mutation) (model.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[mutation.Ref.ID]
	if !ok {
		return model.Item{}, source.Refuse("not-found", mutation.Ref.ID)
	}
	if mutation.ExpectedVersion != item.Version {
		return model.Item{}, source.Refuse("conflict", "fake version changed")
	}
	next, err := fakeApply(item, mutation)
	if err != nil {
		return model.Item{}, err
	}
	s.sequence++
	next.Version = fmt.Sprintf("v%d", s.sequence+1)
	s.items[mutation.Ref.ID] = next
	s.mutations = append(s.mutations, mutation)
	return next, nil
}
func (s *editFakeSource) applied() []model.Mutation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.mutations)
}
func (s *editFakeSource) external(item model.Item) {
	s.mu.Lock()
	s.items[item.Ref.ID] = item
	s.mu.Unlock()
}
func fakeInverse(item model.Item, mutation model.Mutation) model.Mutation {
	inverse := model.Mutation{Ref: item.Ref}
	switch mutation.Op {
	case model.Set:
		inverse.Op, inverse.Field = model.Set, mutation.Field
		switch mutation.Field {
		case "title":
			inverse.Value = item.Title
		case "status":
			inverse.Value = item.Status
		case "priority":
			inverse.Value = string(item.Priority)
		case "milestone":
			inverse.Value = item.Milestone
		case "parent":
			if item.Parent == nil {
				inverse.Op = model.Unset
			} else {
				inverse.Value = qualifiedRef(*item.Parent, item.Ref.Source)
			}
		}
	case model.Add:
		inverse.Op, inverse.Field, inverse.Value = model.Remove, mutation.Field, mutation.Value
	case model.Remove:
		inverse.Op, inverse.Field, inverse.Value = model.Add, mutation.Field, mutation.Value
	case model.CheckCriterion:
		inverse.Op, inverse.Section, inverse.Index = model.UncheckCriterion, mutation.Section, mutation.Index
	case model.UncheckCriterion:
		inverse.Op, inverse.Section, inverse.Index = model.CheckCriterion, mutation.Section, mutation.Index
	case model.Batch:
		inverse.Op = model.Batch
		for i := len(mutation.Changes) - 1; i >= 0; i-- {
			inverse.Changes = append(inverse.Changes, fakeInverse(item, mutation.Changes[i]))
		}
	case model.Archive:
		inverse.Op = model.Unarchive
	}
	return inverse
}
func fakeApply(item model.Item, mutation model.Mutation) (model.Item, error) {
	item.Labels = slices.Clone(item.Labels)
	item.Assignees = slices.Clone(item.Assignees)
	item.BlockedBy = slices.Clone(item.BlockedBy)
	item.Criteria = slices.Clone(item.Criteria)
	item.DoD = slices.Clone(item.DoD)
	if mutation.Op == model.Batch {
		for _, change := range mutation.Changes {
			var err error
			item, err = fakeApply(item, change)
			if err != nil {
				return model.Item{}, err
			}
		}
		return item, nil
	}
	text, _ := mutation.Value.(string)
	switch mutation.Op {
	case model.Set:
		switch mutation.Field {
		case "title":
			item.Title = text
		case "status":
			item.Status = text
		case "priority":
			item.Priority = model.Priority(text)
		case "milestone":
			item.Milestone = text
		case "parent":
			item.Parent = parseFakeRef(text, item.Ref.Source)
		}
	case model.Unset:
		switch mutation.Field {
		case "parent":
			item.Parent = nil
		case "milestone":
			item.Milestone = ""
		}
	case model.Add, model.Remove:
		var values *[]string
		switch mutation.Field {
		case "labels":
			values = &item.Labels
		case "assignees":
			values = &item.Assignees
		}
		if mutation.Field == "blocked_by" {
			ref := parseFakeRef(text, item.Ref.Source)
			if ref == nil {
				return model.Item{}, source.Refuse("rejected", "invalid reference")
			}
			index := -1
			for i, existing := range item.BlockedBy {
				if existing == *ref {
					index = i
					break
				}
			}
			if mutation.Op == model.Add && index < 0 {
				item.BlockedBy = append(item.BlockedBy, *ref)
			} else if mutation.Op == model.Remove && index >= 0 {
				item.BlockedBy = slices.Delete(item.BlockedBy, index, index+1)
			}
		} else if values != nil {
			index := slices.Index(*values, text)
			if mutation.Op == model.Add && index < 0 {
				*values = append(*values, text)
			} else if mutation.Op == model.Remove && index >= 0 {
				*values = slices.Delete(*values, index, index+1)
			}
		}
	case model.CheckCriterion, model.UncheckCriterion:
		criteria := &item.Criteria
		if mutation.Section == "Definition of done" {
			criteria = &item.DoD
		}
		if mutation.Index < 1 || mutation.Index > len(*criteria) {
			return model.Item{}, source.Refuse("rejected", "criterion index")
		}
		(*criteria)[mutation.Index-1].Checked = mutation.Op == model.CheckCriterion
	case model.Archive, model.Unarchive:
	default:
		return model.Item{}, source.Refuse("rejected", "unsupported fake mutation")
	}
	return item, nil
}
func parseFakeRef(value, sourceID string) *model.Ref {
	if value == "" {
		return nil
	}
	sourceName, id, qualified := strings.Cut(value, ":")
	if qualified {
		return &model.Ref{Source: sourceName, ID: id}
	}
	return &model.Ref{Source: sourceID, ID: value}
}

var allEditCapabilities = model.Capabilities{
	model.Batch: true, model.Archive: true, model.Unarchive: true, model.Set: true,
	model.Add: true, model.Remove: true, model.Unset: true, model.CheckCriterion: true,
	model.UncheckCriterion: true, model.Restore: true,
}

func editFixture(t *testing.T, items []model.Item, caps model.Capabilities, opts Options) (Model, *editFakeSource) {
	t.Helper()
	base := opts.Context
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, 5*time.Second)
	t.Cleanup(cancel)
	backend := newEditFakeSource(items, caps)
	statuses := []project.StatusConfig{
		{Key: "todo", Name: "To Do", Category: "todo"},
		{Key: "active", Name: "In Progress", Category: "active"},
		{Key: "done", Name: "Done", Category: "done"},
	}
	cfg := project.Config{Settings: project.Settings{Statuses: statuses, Workflow: project.WorkflowConfig{Start: "active", Done: "done"}, Ready: project.ReadyConfig{Statuses: []string{"todo"}, SatisfiedBy: []string{"done"}}}}
	if c, _ := engine.Coordination(ctx); c != nil {
		cfg.Coordination.Worklease = &struct{}{}
	}
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
	t.Cleanup(store.Close)
	waitEdit(t, func() bool { return store.Current().Len() == len(items) })
	opts.Context, opts.Store = ctx, store
	opts.Statuses, opts.Workflow = statuses, cfg.Workflow
	if opts.Project == "" {
		opts.Project = "fixture"
	}
	m := New(opts)
	m.snapshot = store.Current()
	rows := make([]screenRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, screenRow{row: engine.Row{Ref: item.Ref, Version: item.Version, Title: item.Title, Status: item.Status, Priority: item.Priority, Labels: item.Labels, Assignees: item.Assignees, Milestone: item.Milestone, URL: item.URL}})
	}
	slices.SortFunc(rows, func(a, b screenRow) int { return strings.Compare(a.row.Ref.ID, b.row.Ref.ID) })
	m.data.rows = rows
	m.groups = []engine.RowGroup{{Value: "todo"}}
	for _, row := range rows {
		m.groups[0].Rows = append(m.groups[0].Rows, row.row)
	}
	m.data.items = len(rows)
	m.loading = false
	return m, backend
}
func waitEdit(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("timed out waiting for fake source")
}
func editKeyMessage(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space", " ":
		return tea.KeyPressMsg{Code: tea.KeySpace}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	default:
		if strings.HasPrefix(key, "ctrl+") {
			return tea.KeyPressMsg{Code: rune(key[len(key)-1]), Mod: tea.ModCtrl}
		}
		return tea.KeyPressMsg{Code: []rune(key)[0]}
	}
}
func editPress(m Model, key string) Model {
	if !strings.HasPrefix(key, "ctrl+") && !slices.Contains([]string{"enter", "space", " ", "esc", "backspace", "down", "up"}, key) && len([]rune(key)) > 1 {
		for _, character := range key {
			m = editPress(m, string(character))
		}
		return m
	}
	next, cmd := m.Update(editKeyMessage(key))
	m = next.(Model)
	if cmd == nil {
		return m
	}
	msg := cmd()
	if msg == nil {
		return m
	}
	next, follow := m.Update(msg)
	m = next.(Model)
	if follow != nil {
		return editPressMessage(m, follow)
	}
	return m
}
func editPressMessage(m Model, cmd tea.Cmd) Model {
	msg := cmd()
	if msg == nil {
		return m
	}
	next, follow := m.Update(msg)
	m = next.(Model)
	if follow != nil {
		return editPressMessage(m, follow)
	}
	return m
}
func editItem(id, title string) model.Item {
	return model.Item{Ref: model.Ref{Source: "tasks", ID: id}, Title: title, Status: "todo", Category: model.Todo, Priority: model.Medium, Version: "v1"}
}
func itemFrom(t *testing.T, backend *editFakeSource, id string) model.Item {
	t.Helper()
	backend.mu.Lock()
	defer backend.mu.Unlock()
	item, ok := backend.items[id]
	if !ok {
		t.Fatalf("missing fake item %s", id)
	}
	return item
}

// Covers the public key routes with a fake source so a key cannot appear to
// work while producing the wrong source mutation.
func TestEditKeysMutateFakeSource(t *testing.T) {
	cases := []struct {
		name  string
		item  func() model.Item
		keys  []string
		check func(*testing.T, model.Item)
	}{
		{"status picker", editItemForTest, []string{"s", "active", "enter"}, func(t *testing.T, item model.Item) {
			if item.Status != "active" {
				t.Fatalf("status %q", item.Status)
			}
		}},
		{"previous status", func() model.Item { i := editItem("BL-1", "First"); i.Status = "active"; return i }, []string{"<"}, func(t *testing.T, item model.Item) {
			if item.Status != "todo" {
				t.Fatalf("status %q", item.Status)
			}
		}},
		{"next status", editItemForTest, []string{">"}, func(t *testing.T, item model.Item) {
			if item.Status != "active" {
				t.Fatalf("status %q", item.Status)
			}
		}},
		{"priority picker", editItemForTest, []string{"p", "h"}, func(t *testing.T, item model.Item) {
			if item.Priority != model.High {
				t.Fatalf("priority %q", item.Priority)
			}
		}},
		{"assignee picker", editItemForTest, []string{"a", "@amy", "enter"}, func(t *testing.T, item model.Item) {
			if !slices.Contains(item.Assignees, "@amy") {
				t.Fatalf("assignees %v", item.Assignees)
			}
		}},
		{"assign me", editItemForTest, []string{"A"}, func(t *testing.T, item model.Item) {
			if !slices.Contains(item.Assignees, "@me") {
				t.Fatalf("assignees %v", item.Assignees)
			}
		}},
		{"label picker", editItemForTest, []string{"#", "new-jacket", "enter"}, func(t *testing.T, item model.Item) {
			if !slices.Contains(item.Labels, "new-jacket") {
				t.Fatalf("labels %v", item.Labels)
			}
		}},
		{"milestone free text", editItemForTest, []string{"m", "m-jacket", "enter"}, func(t *testing.T, item model.Item) {
			if item.Milestone != "m-jacket" {
				t.Fatalf("milestone %q", item.Milestone)
			}
		}},
		{"add blocker", editItemForTest, []string{"b", "BL-2", "enter"}, func(t *testing.T, item model.Item) {
			if !slices.Contains(item.BlockedBy, model.Ref{Source: "tasks", ID: "BL-2"}) {
				t.Fatalf("blocked by %v", item.BlockedBy)
			}
		}},
		{"remove blocker", func() model.Item {
			i := editItem("BL-1", "First")
			i.BlockedBy = []model.Ref{{Source: "tasks", ID: "BL-2"}}
			return i
		}, []string{"b", "d", "enter"}, func(t *testing.T, item model.Item) {
			if len(item.BlockedBy) != 0 {
				t.Fatalf("blocked by %v", item.BlockedBy)
			}
		}},
		{"parent picker", editItemForTest, []string{"P", "BL-2", "enter"}, func(t *testing.T, item model.Item) {
			if item.Parent == nil || item.Parent.ID != "BL-2" {
				t.Fatalf("parent %v", item.Parent)
			}
		}},
		{"remove current label", func() model.Item {
			i := editItemForTest()
			i.Labels = []string{"configured"}
			return i
		}, []string{"#", "space", "enter"}, func(t *testing.T, item model.Item) {
			if len(item.Labels) != 0 {
				t.Fatalf("labels not removed: %v", item.Labels)
			}
		}},
		{"remove current assignee", func() model.Item {
			i := editItemForTest()
			i.Assignees = []string{"@amy"}
			return i
		}, []string{"a", "space", "enter"}, func(t *testing.T, item model.Item) {
			if len(item.Assignees) != 0 {
				t.Fatalf("assignees not removed: %v", item.Assignees)
			}
		}},
		{"inline title", editItemForTest, []string{"t", "ctrl+u", "Jack likes jackets", "enter"}, func(t *testing.T, item model.Item) {
			if item.Title != "Jack likes jackets" {
				t.Fatalf("title %q", item.Title)
			}
		}},
		{"criteria check", func() model.Item {
			i := editItem("BL-1", "First")
			i.Criteria = []model.Criterion{{Text: "one"}}
			return i
		}, []string{"x"}, func(t *testing.T, item model.Item) {
			if !item.Criteria[0].Checked {
				t.Fatal("criterion did not check")
			}
		}},
		{"criteria uncheck", func() model.Item {
			i := editItem("BL-1", "First")
			i.Criteria = []model.Criterion{{Text: "one", Checked: true}}
			return i
		}, []string{"x"}, func(t *testing.T, item model.Item) {
			if item.Criteria[0].Checked {
				t.Fatal("criterion did not uncheck")
			}
		}},
		{"start", editItemForTest, []string{"S"}, func(t *testing.T, item model.Item) {
			if item.Status != "active" {
				t.Fatalf("status %q", item.Status)
			}
		}},
		{"done", editItemForTest, []string{"D"}, func(t *testing.T, item model.Item) {
			if item.Status != "done" {
				t.Fatalf("status %q", item.Status)
			}
		}},
		{"archive confirm", editItemForTest, []string{"d", "d", "y"}, func(t *testing.T, item model.Item) {
			if item.Title != "First" {
				t.Fatalf("unexpected item mutation: %q", item.Title)
			}
		}},
	}
	for _, test := range cases {
		criteria := test.name == "criteria check" || test.name == "criteria uncheck"
		for _, marked := range []bool{false, true} {
			for _, detail := range []bool{false, true} {
				if criteria && !detail {
					continue // criteria toggle in the Criteria tab
				}
				t.Run(fmt.Sprintf("%s/marked=%t/detail=%t", test.name, marked, detail), func(t *testing.T) {
					items := []model.Item{test.item(), editItem("BL-2", "Second")}
					if marked {
						other := test.item()
						other.Ref.ID = "BL-3"
						items = append(items, other)
					}
					if test.name == "remove blocker" {
						items[0].BlockedBy = []model.Ref{{Source: "tasks", ID: "BL-2"}}
					}
					m, backend := editFixture(t, items, allEditCapabilities, Options{Me: map[string]string{"tasks": "@me"}, Labels: []string{"configured"}, Milestones: []project.Milestone{{Key: "m-1", Name: "First milestone"}}})
					if marked {
						m = editPress(m, "space")
						m = editPress(m, "j")
						m = editPress(m, "j")
						m = editPress(m, "space")
						m = editPress(m, "k")
						m = editPress(m, "k")
					}
					if detail {
						m.detailOpen, m.detailFocus = true, true
						if criteria {
							m.detailTab = tabCriteria
						}
					}
					for _, key := range test.keys {
						m = editPress(m, key)
					}
					item := itemFrom(t, backend, "BL-1")
					test.check(t, item)
					if marked {
						other := itemFrom(t, backend, "BL-3")
						// Detail edits only the item it shows.
						if detail && other.Version != "v1" {
							t.Fatalf("detail edit changed a marked item: %+v", other)
						}
						if !detail {
							test.check(t, other)
						}
					}
					if len(backend.applied()) == 0 {
						t.Fatal("key produced no fake-source mutation")
					}
				})
			}
		}
	}
}

func editItemForTest() model.Item { return editItem("BL-1", "First") }

func TestEditRepeatUndoRedoAndMarkedTargets(t *testing.T) {
	t.Run("repeat uses current target and version", func(t *testing.T) {
		m, backend := editFixture(t, []model.Item{editItem("BL-1", "First"), editItem("BL-2", "Second")}, allEditCapabilities, Options{})
		m = editPress(m, "t")
		m = editPress(m, "ctrl+u")
		m = editPress(m, "Renamed")
		m = editPress(m, "enter")
		m = editPress(m, "j")
		_ = editPress(m, ".")
		if item := itemFrom(t, backend, "BL-2"); item.Title != "Renamed" {
			t.Fatalf("repeat used stale target: %+v", item)
		}
		if len(backend.applied()) != 2 {
			t.Fatalf("mutations: %+v", backend.applied())
		}
	})
	t.Run("undo and redo", func(t *testing.T) {
		m, backend := editFixture(t, []model.Item{editItem("BL-1", "First")}, allEditCapabilities, Options{})
		m = editPress(m, "t")
		m = editPress(m, "ctrl+u")
		m = editPress(m, "Changed")
		m = editPress(m, "enter")
		m = editPress(m, "u")
		if item := itemFrom(t, backend, "BL-1"); item.Title != "First" {
			t.Fatalf("undo title %q", item.Title)
		}
		_ = editPress(m, "ctrl+r")
		if item := itemFrom(t, backend, "BL-1"); item.Title != "Changed" {
			t.Fatalf("redo title %q", item.Title)
		}
	})
	t.Run("marks receive batch edit", func(t *testing.T) {
		items := []model.Item{editItem("BL-1", "First"), editItem("BL-2", "Second")}
		m, backend := editFixture(t, items, allEditCapabilities, Options{})
		m = editPress(m, "space")
		m = editPress(m, "j")
		m = editPress(m, "space")
		m = editPress(m, ">")
		for _, id := range []string{"BL-1", "BL-2"} {
			if item := itemFrom(t, backend, id); item.Status != "active" {
				t.Fatalf("marked item %s status %q", id, item.Status)
			}
		}
		if !strings.Contains(m.notice, "Edited 2 item(s)") {
			t.Fatalf("batch result: %s", m.notice)
		}
	})
	t.Run("marked version remains the optimistic guard", func(t *testing.T) {
		m, backend := editFixture(t, []model.Item{editItem("BL-1", "First"), editItem("BL-2", "Second")}, allEditCapabilities, Options{})
		m = editPress(m, "space")
		remote := itemFrom(t, backend, "BL-1")
		remote.Title = "External"
		remote.Version = "v9"
		backend.external(remote)
		m.opts.Store.Refresh()
		waitEdit(t, func() bool {
			item, _, ok := m.opts.Store.Current().Get(model.Ref{Source: "tasks", ID: "BL-1"})
			return ok && item.Version == "v9"
		})
		m.data.rows = []screenRow{m.data.rows[1]}
		m.selected = 0
		m = editPress(m, ">")
		if item := itemFrom(t, backend, "BL-1"); item.Status != "todo" || item.Title != "External" {
			t.Fatalf("stale marked edit overwrote source: %+v", item)
		}
		if len(backend.applied()) != 0 || !strings.Contains(m.notice, "conflict") {
			t.Fatalf("stale write outcome: applied=%v notice=%q", backend.applied(), m.notice)
		}
	})
	t.Run("visual range", func(t *testing.T) {
		m, backend := editFixture(t, []model.Item{editItem("BL-1", "First"), editItem("BL-2", "Second")}, allEditCapabilities, Options{})
		m = editPress(m, "V")
		m = editPress(m, "j")
		_ = editPress(m, ">")
		for _, id := range []string{"BL-1", "BL-2"} {
			if item := itemFrom(t, backend, id); item.Status != "active" {
				t.Fatalf("range item %s status %q", id, item.Status)
			}
		}
	})
	t.Run("mark entire view", func(t *testing.T) {
		m, backend := editFixture(t, []model.Item{editItem("BL-1", "First"), editItem("BL-2", "Second")}, allEditCapabilities, Options{})
		m = editPress(m, "ctrl+a")
		_ = editPress(m, ">")
		for _, id := range []string{"BL-1", "BL-2"} {
			if item := itemFrom(t, backend, id); item.Status != "active" {
				t.Fatalf("view item %s status %q", id, item.Status)
			}
		}
	})
}

// An edit from focused detail can move its item out of the view. Detail must
// keep the item, and keep it current, or the next key edits the neighbor that
// took its place.
func TestDetailHoldsItemThatLeavesView(t *testing.T) {
	m, backend := editFixture(t, []model.Item{editItem("BL-1", "First"), editItem("BL-2", "Second"), editItem("BL-3", "Third")}, allEditCapabilities, Options{})
	projector := engine.NewProjector(m.opts.Context, project.Config{Settings: project.Settings{Statuses: m.opts.Statuses}})
	t.Cleanup(projector.Close)
	m.opts.Projector, m.query = projector, "status:todo sort:id"
	m.project()
	m = settleQuery(t, m)
	m.detailOpen, m.detailFocus = true, true
	sync := func(status string) {
		t.Helper()
		waitEdit(t, func() bool {
			item, _, _ := m.opts.Store.Current().Get(model.Ref{Source: "tasks", ID: "BL-1"})
			return item.Status == status
		})
		next, _ := m.Update(snapshotMsg{snapshot: m.opts.Store.Current()})
		m = settleQuery(t, next.(Model))
		if row, _ := m.selectedRow(); row.Ref.ID != "BL-1" || row.Status != status || len(m.data.rows) != 2 {
			t.Fatalf("detail after its item left the view: %+v, %d rows", row, len(m.data.rows))
		}
	}
	m = editPress(m, ">")
	sync("active")
	m = editPress(m, ">")
	if first, second := itemFrom(t, backend, "BL-1"), itemFrom(t, backend, "BL-2"); first.Status != "done" || second.Status != "todo" {
		t.Fatalf("second edit: BL-1 %q, BL-2 %q", first.Status, second.Status)
	}
	sync("done")
	m = editPress(m, "J")
	if row, _ := m.selectedRow(); row.Ref.ID != "BL-2" || m.detached != nil {
		t.Fatalf("J from a held item: %+v", row)
	}
}

func TestUnsupportedCapabilitiesAndFailureDetails(t *testing.T) {
	m, backend := editFixture(t, []model.Item{editItem("BL-1", "First"), editItem("BL-2", "Second")}, model.Capabilities{}, Options{})
	m = editPress(m, "s")
	if m.picker.open || !strings.Contains(m.notice, "does not support set") || len(backend.applied()) != 0 {
		t.Fatalf("unsupported edit: picker=%t notice=%q mutations=%v", m.picker.open, m.notice, backend.applied())
	}
	m.data.rows[0].row.Pending = true
	m.data.rows[1].row.Error = "conflict: remote changed"
	m.data.rows[1].row.ErrorLocalVersion = "local-v2"
	m.data.rows[1].row.ErrorRemoteVersion = "remote-v3"
	m.selected = 1
	content := m.View().Content
	if !strings.Contains(content, "●") || !strings.Contains(content, "!") {
		t.Fatalf("missing pending/failed glyphs: %s", content)
	}
	m.detailOpen, m.detailFocus = true, true
	detail := m.View().Content
	if !strings.Contains(detail, "conflict: remote changed") || !strings.Contains(detail, "local-v2") || !strings.Contains(detail, "remote-v3") {
		t.Fatalf("failure detail lacks reason or versions: %s", detail)
	}
}

func TestLabelAndMilestonePickerCatalogs(t *testing.T) {
	items := []model.Item{editItem("BL-1", "First"), editItem("BL-2", "Second")}
	items[0].Labels = []string{"used-current"}
	items[1].Labels = []string{"used-other"}
	items[0].Milestone = "m-1"
	items[1].Milestone = "m-2"
	m, _ := editFixture(t, items, allEditCapabilities, Options{
		Labels: []string{"configured-b", "configured-a"},
		Milestones: []project.Milestone{
			{Key: "m-1", Name: "Current milestone"},
			{Key: "m-3", Name: "Upcoming"},
			{Key: "m-archived", Name: "Archived", Archived: true},
		},
	})
	m = editPress(m, "#")
	if len(m.picker.choices) < 4 || m.picker.choices[0].value != "configured-b" || m.picker.choices[1].value != "configured-a" {
		t.Fatalf("configured labels do not lead the picker: %+v", m.picker.choices)
	}
	m = editPress(m, "esc")
	m = editPress(m, "m")
	keys := make([]string, 0, len(m.picker.choices))
	for _, choice := range m.picker.choices {
		keys = append(keys, choice.value)
	}
	if !slices.Contains(keys, "m-1") || !slices.Contains(keys, "m-3") || slices.Contains(keys, "m-archived") || !slices.Contains(keys, "m-2") {
		t.Fatalf("milestone names/archive/in-use values: kind=%s title=%s choices=%+v", m.picker.kind, m.picker.title, m.picker.choices)
	}
	for _, choice := range m.picker.choices {
		if choice.value == "m-1" && !strings.Contains(choice.label, "Current milestone") {
			t.Fatalf("milestone name missing: %+v", choice)
		}
	}
}

func TestYankOSC52AndSafeOpen(t *testing.T) {
	item := editItem("BL-12", "Debounce detail rendering")
	item.URL = "https://example.test/items/12"
	m, _ := editFixture(t, []model.Item{item}, allEditCapabilities, Options{})
	next, _ := m.Update(editKeyMessage("y"))
	m = next.(Model)
	next, cmd := m.Update(editKeyMessage("b"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("yb did not emit an OSC 52 command")
	}
	message := cmd()
	raw, ok := message.(tea.RawMsg)
	sequence, isString := raw.Msg.(string)
	if !ok || !isString || !strings.HasPrefix(sequence, "\x1b]52;c;") || !strings.HasSuffix(sequence, "\a") {
		t.Fatalf("yank message %#v", message)
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(sequence, "\x1b]52;c;"), "\a")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != "bl-12-debounce-detail-rendering" {
		t.Fatalf("OSC 52 payload %q: %v", decoded, err)
	}
	opened := ""
	m.opts.OpenURL = func(_ context.Context, value string) error { opened = value; return nil }
	m = editPress(m, "O")
	if opened != item.URL {
		t.Fatalf("opened URL %q", opened)
	}
	for _, value := range []string{"javascript:alert(1)", "https://" + url.UserPassword("fixture-user", "fixture-password").String() + "@example.test/", "https://example.test/\x1b[2J", "https://example.test/" + strings.Repeat("x", 1024)} {
		if _, err := safeURL(value); err == nil {
			t.Fatalf("unsafe URL accepted: %q", value)
		}
	}
	if value, err := safeURL("HTTPS://example.test/items/12"); err != nil || value != "https://example.test/items/12" {
		t.Fatalf("uppercase HTTPS rejected: %q %v", value, err)
	}
}
