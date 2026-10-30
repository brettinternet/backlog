package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/history"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/charmbracelet/x/ansi"
)

func detailFixture(t *testing.T, count int, render func(string, int) (string, error)) Model {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := New(Options{Context: ctx, NoColor: true, Project: "fixture", Render: render,
		Milestones: []project.Milestone{{Key: "m-1", Name: "TUI"}},
		Read: func(_ context.Context, ref model.Ref) (model.Item, error) {
			return model.Item{Ref: ref, Version: "v1", Title: "Detail fixture", Status: "todo", Category: model.Todo, Priority: model.High,
				Milestone: "m-1", Description: "# Description\n\nA **selected** item. 修正 🚀", Criteria: []model.Criterion{{Text: "Keep input responsive", Checked: true, Evidence: "counter"}}, DoD: []model.Criterion{{Text: "CI passes"}}, Plan: "Implement detail", Notes: "Verified asynchronously", Comments: []model.Comment{{Author: "brett", Body: "Looks good"}}}, nil
		}})
	m.width = 120
	group := engine.RowGroup{Value: "todo"}
	for i := 0; i < count; i++ {
		group.Rows = append(group.Rows, engine.Row{Ref: model.Ref{Source: "tasks", ID: fmt.Sprintf("BL-%d", i+1)}, Version: "v1", Title: fmt.Sprintf("Item %d", i+1), Status: "todo", Category: model.Todo, Priority: model.High, Readiness: engine.Ready, ReadinessReason: "all readiness checks pass", CriteriaDone: 1, CriteriaTotal: 2, Comments: 1})
	}
	m.data = prepareGroupedRows([]engine.RowGroup{group}, nil, false, "", nil)
	m.data.widths = measure(m.data.rows, nil)
	return m
}
func detailKey(m Model, code rune, mod tea.KeyMod) (Model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
	return next.(Model), cmd
}

// settleDetail skips the settle timer, not the worker: it submits the current
// request unless a tab change already did, and applies the worker's answer.
func settleDetail(t *testing.T, m Model) Model {
	t.Helper()
	if _, ok := m.selectedRow(); !ok {
		t.Fatal("no selected item")
	}
	if m.detailSubmitted != m.detailGeneration {
		m.submitDetail(false)
	}
	for {
		select {
		case result := <-m.detailWorker.results:
			next, _ := m.Update(detailResultMsg(result))
			m = next.(Model)
			if result.generation != m.detailGeneration {
				continue
			}
		case <-time.After(5 * time.Second):
			t.Fatal("detail never settled")
		}
		break
	}
	if m.detailError != "" {
		t.Fatal(m.detailError)
	}
	return m
}

// A held key must not enqueue one render per row, nor leave the header/cursor behind.
func TestDetailHeldNavigation(t *testing.T) {
	t.Parallel()
	var renders, reads atomic.Int32
	m := detailFixture(t, 10000, func(body string, _ int) (string, error) { renders.Add(1); return body, nil })
	read := m.detailWorker.read
	m.detailWorker.read = func(ctx context.Context, ref model.Ref) (model.Item, error) { reads.Add(1); return read(ctx, ref) }
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m) // opening renders at once
	m, _ = detailKey(m, 'h', 0)
	renders.Store(0)
	reads.Store(0)
	for i := 1; i < 10000; i++ {
		var cmd tea.Cmd
		m, cmd = detailKey(m, 'j', 0)
		// Only the first move starts timers: settling and the loading delay.
		if cmd != nil && i > 1 {
			t.Fatal("held navigation spawned extra settle timers")
		}
		if header := detailHeader(m); !strings.Contains(header, fmt.Sprintf("BL-%d ", i+1)) || !strings.Contains(header, fmt.Sprintf("Item %d", i+1)) {
			t.Fatal("header lagged selection")
		}
		if m.selected >= m.top+m.page() {
			t.Fatal("cursor left viewport")
		}
	}
	if renders.Load() != 0 || reads.Load() != 0 {
		t.Fatal("navigation performed detail work")
	}
	next, _ := m.Update(detailDue(m.detailGeneration))
	m = next.(Model)
	result := m.waitDetail()()
	next, _ = m.Update(result)
	m = next.(Model)
	if renders.Load() != 1 || reads.Load() != 1 || m.detailLoading {
		t.Fatal("settled selection was not rendered exactly once")
	}
}

// A slow reload of the page in view (resize, R) must keep its body, while a
// slow load of another item must not show the previous item's body.
func TestDetailSlowReloadKeepsBody(t *testing.T) {
	t.Parallel()
	m := detailFixture(t, 2, func(body string, _ int) (string, error) { return body, nil })
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	pane := func(m Model) string { return ansi.Strip(strings.Join(m.detailPane(m.detailWidth(), 20), "\n")) }
	slow := func(m Model) Model {
		next, _ := m.Update(indicatorDue{kind: detailLoad, period: m.loads[detailLoad].period})
		return next.(Model)
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 24})
	m = slow(next.(Model))
	view := pane(m)
	if !strings.Contains(view, "selected") || !strings.Contains(view, "Loading…") {
		t.Fatalf("resize reload replaced the body:\n%s", view)
	}
	// The indicator only fills blank cells; the scroll percent and body stay.
	short := func(m Model) string { return ansi.Strip(strings.Join(m.detailPane(m.detailWidth(), 6), "\n")) }
	if unshown := strings.Replace(short(m), "Loading… · ", strings.Repeat(" ", ansi.StringWidth("Loading… · ")), 1); !strings.Contains(unshown, "%") || unshown != short(next.(Model)) {
		t.Fatalf("indicator shifted content:\n%s\nwant:\n%s", short(m), short(next.(Model)))
	}
	m, _ = detailKey(m, 'h', 0)
	m, _ = detailKey(m, 'j', 0)
	if view := pane(m); strings.Contains(view, "selected") || !strings.Contains(view, "Loading…") {
		t.Fatalf("another item kept the previous body:\n%s", view)
	}
}

// Cache keys must distinguish item versions, width and tabs; stale completion cannot repaint a new selection.
func TestDetailCacheAndStaleResults(t *testing.T) {
	t.Parallel()
	var renders, reads int
	m := detailFixture(t, 2, func(body string, _ int) (string, error) { renders++; return body, nil })
	read := m.detailWorker.read
	m.detailWorker.read = func(ctx context.Context, ref model.Ref) (model.Item, error) {
		reads++
		item, err := read(ctx, ref)
		item.Version = "" // follow the row's version
		return item, err
	}
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	stale := detailResultMsg{generation: m.detailGeneration, identity: m.detailIdentity, lines: []string{"STALE"}}
	m, _ = detailKey(m, 'h', 0)
	m, _ = detailKey(m, 'j', 0)
	m = settleDetail(t, m)
	next, _ := m.Update(stale)
	m = next.(Model)
	if strings.Contains(strings.Join(m.detailLines, "\n"), "STALE") {
		t.Fatal("stale result accepted")
	}
	m, _ = detailKey(m, 'k', 0)
	m = settleDetail(t, m)
	if renders != 2 || reads != 2 {
		t.Fatalf("return missed cache: renders=%d reads=%d", renders, reads)
	}
	m.data.rows[0].row.Version = "v2"
	_ = m.scheduleDetail()
	m = settleDetail(t, m)
	if renders != 3 || reads != 3 {
		t.Fatal("version did not invalidate cache")
	}
	next, _ = m.Update(tea.WindowSizeMsg{Width: 130, Height: 24})
	m = next.(Model)
	m = settleDetail(t, m)
	if renders != 4 || reads != 3 {
		t.Fatal("width did not invalidate rendering only")
	}
	m.detailTab = tabComments
	_ = m.scheduleDetail()
	m = settleDetail(t, m)
	if !strings.Contains(strings.Join(m.detailLines, "\n"), "Looks good") || reads != 3 {
		t.Fatal("comments not loaded lazily with cached full detail")
	}
	// An edit that keeps the provider's version is only visible to a reread.
	m.detailFocus = true
	m, _ = detailKey(m, 'R', 0)
	m = settleDetail(t, m)
	if renders != 6 || reads != 4 {
		t.Fatalf("R in detail served the cache: renders=%d reads=%d", renders, reads)
	}
}

// A megabyte read must be capped before rendering, with discoverable expansion, while the UI keeps taking input.
func TestDetailLargeBodyInput(t *testing.T) {
	t.Parallel()
	started := make(chan int, 1)
	release := make(chan struct{})
	var first atomic.Bool
	m := detailFixture(t, 2, func(body string, _ int) (string, error) {
		if first.CompareAndSwap(false, true) {
			started <- len(body)
			<-release
		}
		return body, nil
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	m.detailWorker.read = func(_ context.Context, ref model.Ref) (model.Item, error) {
		return model.Item{Ref: ref, Version: "v1", Description: strings.Repeat("large paragraph\n", 70000)}, nil
	}
	m, _ = detailKey(m, tea.KeyEnter, 0)
	next, _ := m.Update(detailDue(m.detailGeneration))
	m = next.(Model)
	select {
	case n := <-started:
		if n > bodyLimit {
			t.Fatalf("uncapped render input %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("render never started")
	}
	m, _ = detailKey(m, 'j', 0) // scroll input while renderer is held
	m, _ = detailKey(m, 'h', 0)
	m, _ = detailKey(m, 'j', 0)
	if m.selected != 1 || !strings.Contains(detailHeader(m), "BL-2") {
		t.Fatal("input blocked by renderer")
	}
	close(release)
	next, _ = m.Update(m.waitDetail()())
	m = next.(Model)
	if len(m.detailLines) != 0 {
		t.Fatal("old large body replaced new selection")
	}
	m.detailTab = tabOverview
	_ = m.scheduleDetail()
	m = settleDetail(t, m)
	if !m.detailTruncated || !strings.Contains(m.View().Content, "m more") {
		t.Fatal("missing visible expansion control")
	}
	m, _ = detailKey(m, tea.KeyTab, 0) // focus detail without changing tab
	m, _ = detailKey(m, 'm', 0)
	m = settleDetail(t, m)
	if !m.detailExpanded || len(strings.Join(m.detailLines, "\n")) <= bodyLimit {
		t.Fatal("show more did not expand body")
	}
	if len(strings.Join(m.detailLines, "\n")) > renderLimit+100 {
		t.Fatal("expanded output unbounded")
	}
	// The header's label takes a click where it is drawn.
	y := drawnRow(t, m, "m less")
	next, _ = m.Update(tea.MouseClickMsg{X: drawnAt(t, m, y, "m less"), Y: y, Button: tea.MouseLeft})
	m = settleDetail(t, next.(Model))
	if m.detailExpanded {
		t.Fatal("clicking m less did not restore the limit")
	}
}

func TestDetailTabGoldens(t *testing.T) {
	t.Parallel()
	for _, width := range []int{120, 80} {
		m := detailFixture(t, 2, RenderMarkdown)
		m.width = width
		m.data.rows[0].row.Relations = []engine.Relation{{Kind: "blocked by", Ref: model.Ref{Source: "tasks", ID: "BL-2"}, Status: "active", Category: model.Active, Title: "Dependency"}, {Kind: "children", Ref: model.Ref{Source: "tasks", ID: "BL-3"}, Status: "done", Category: model.Done, Title: "Child"}}
		m, _ = detailKey(m, tea.KeyEnter, 0)
		var transcript strings.Builder
		for tab := tabOverview; tab <= tabHistory; tab++ {
			m.detailTab = tab
			_ = m.scheduleDetail()
			m = settleDetail(t, m)
			content := m.View().Content
			if !strings.Contains(content, detailTabNames[tab]) {
				t.Fatalf("active tab %s hidden at %d", detailTabNames[tab], width)
			}
			for _, line := range strings.Split(content, "\n") {
				if ansi.StringWidth(line) != width {
					t.Fatalf("width %d got %d", width, ansi.StringWidth(line))
				}
			}
			fmt.Fprintf(&transcript, "== %s ==\n%s\n", detailTabNames[tab], content)
		}
		golden(t, fmt.Sprintf("detail-%d", width), transcript.String())
	}
	m := detailFixture(t, 1, func(body string, _ int) (string, error) { return body, nil })
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	if !strings.Contains(strings.Join(m.detailLines, "\n"), "m-1 — TUI") {
		t.Fatal("declared milestone name missing")
	}
	m.opts.Milestones = nil
	m.detailWorker.cache = newDetailCache(maxRenderEntries, maxRenderBytes)
	_ = m.scheduleDetail()
	m = settleDetail(t, m)
	body := strings.Join(m.detailLines, "\n")
	if !strings.Contains(body, "Milestone  m-1\n") || strings.Contains(body, "TUI") {
		t.Fatal("undeclared milestone was not raw")
	}
}

type detailSource struct {
	mu    sync.Mutex
	items []model.Item
}

func (*detailSource) Info() source.Info { return source.Info{ID: "tasks"} }
func (s *detailSource) Load(context.Context, source.Cursor) (source.Delta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return source.Delta{Coverage: source.Complete, Items: append([]model.Item(nil), s.items...)}, nil
}
func (s *detailSource) Read(_ context.Context, id string) (model.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, i := range s.items {
		if i.Ref.ID == id {
			return i, nil
		}
	}
	return model.Item{}, fmt.Errorf("missing %s", id)
}
func (*detailSource) Apply(context.Context, model.Mutation) (model.Item, error) {
	return model.Item{}, fmt.Errorf("unused")
}

// Related items often change without bumping the selected item's version.
func TestDetailExternalRelations(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ref := func(id string) model.Ref { return model.Ref{Source: "tasks", ID: id} }
	backend := &detailSource{items: []model.Item{
		{Ref: ref("BL-1"), Version: "v1", Title: "Owner", Status: "todo", Category: model.Todo, BlockedBy: []model.Ref{ref("BL-2")}},
		{Ref: ref("BL-2"), Version: "v1", Title: "Blocker", Status: "active", Category: model.Active},
		{Ref: ref("BL-3"), Version: "v1", Title: "Child", Status: "todo", Category: model.Todo, Parent: func() *model.Ref { v := ref("BL-1"); return &v }()},
	}}
	cfg := project.Config{Settings: project.Settings{Ready: project.ReadyConfig{Statuses: []string{"todo"}, SatisfiedBy: []string{"done"}}, Statuses: []project.StatusConfig{{Key: "todo", Category: "todo"}, {Key: "active", Category: "active"}, {Key: "done", Category: "done"}}}}
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
	defer store.Close()
	projector := engine.NewProjector(ctx, cfg)
	defer projector.Close()
	ch, unsub := store.Subscribe()
	defer unsub()
	projectRow := func(want string) engine.Row {
		t.Helper()
		for {
			select {
			case <-ctx.Done():
				t.Fatal("external refresh timed out")
			case snapshot := <-ch:
				projector.Request(snapshot, "sort:id")
				select {
				case <-ctx.Done():
					t.Fatal("projection timed out")
				case p := <-projector.Results():
					if p.Error != nil {
						t.Fatal(p.Error)
					}
					for _, g := range p.Groups {
						for _, row := range g.Rows {
							if row.Ref == ref("BL-1") {
								for _, relation := range row.Relations {
									if relation.Ref == ref("BL-2") && relation.Status == want {
										return row
									}
								}
							}
						}
					}
				}
			}
		}
	}
	row := projectRow("active")
	m := New(Options{Context: ctx, NoColor: true, Read: func(ctx context.Context, ref model.Ref) (model.Item, error) { return backend.Read(ctx, ref.ID) }, Render: func(body string, _ int) (string, error) { return body, nil }})
	m.data.rows = []screenRow{{row: row}}
	m.detailTab = tabRelations
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	before := strings.Join(m.detailLines, "\n")
	if !strings.Contains(before, "BL-2  Blocker  active") || !strings.Contains(before, "BL-3  Child  todo") {
		t.Fatal(before)
	}
	backend.mu.Lock()
	backend.items[1].Status = "done"
	backend.items[1].Category = model.Done
	backend.items[1].Version = "v2"
	backend.items[2].Status = "done"
	backend.items[2].Category = model.Done
	backend.items[2].Version = "v2"
	backend.mu.Unlock()
	store.Refresh()
	updated := projectRow("done")
	if row.Version != updated.Version || row.RelationVersion == updated.RelationVersion {
		t.Fatal("related version not tracked independently")
	}
	next, _ := m.Update(foldedMsg{serial: m.serial, data: prepared{rows: []screenRow{{row: updated}}}})
	m = next.(Model)
	m = settleDetail(t, m)
	after := strings.Join(m.detailLines, "\n")
	if !strings.Contains(after, "BL-2  Blocker  done") || !strings.Contains(after, "BL-3  Child  done") {
		t.Fatal(after)
	}
}

// Following a relation must open the target with its own inbound relations,
// leave the list alone, refuse a target outside the snapshot, and unwind.
func TestDetailFollowRelations(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ref := func(id string) model.Ref { return model.Ref{Source: "tasks", ID: id} }
	parent := ref("BL-2")
	backend := &detailSource{items: []model.Item{
		{Ref: ref("BL-1"), Version: "v1", Title: "Owner", Status: "todo", Category: model.Todo, BlockedBy: []model.Ref{ref("BL-2")}, Related: []model.Ref{ref("BL-9")}},
		{Ref: ref("BL-2"), Version: "v1", Title: "Blocker", Status: "done", Category: model.Done},
		{Ref: ref("BL-3"), Version: "v1", Title: "Child", Status: "done", Category: model.Done, Parent: &parent},
	}}
	cfg := project.Config{Settings: project.Settings{Ready: project.ReadyConfig{Statuses: []string{"todo"}, SatisfiedBy: []string{"done"}}, Statuses: []project.StatusConfig{{Key: "todo", Category: "todo"}, {Key: "done", Category: "done"}}}}
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
	defer store.Close()
	projector := engine.NewProjector(ctx, cfg)
	defer projector.Close()
	ch, unsub := store.Subscribe()
	defer unsub()
	m := New(Options{Context: ctx, NoColor: true, Read: func(ctx context.Context, ref model.Ref) (model.Item, error) { return backend.Read(ctx, ref.ID) }, Render: func(body string, _ int) (string, error) { return body, nil }})
	m.width = 120
	for m.catalog == nil {
		select {
		case <-ctx.Done():
			t.Fatal("projection timed out")
		case snapshot := <-ch:
			if snapshot.Len() < 3 {
				continue
			}
			projector.Request(snapshot, "is:open")
			p := <-projector.Results()
			m.snapshot, m.catalog = snapshot, p.Catalog
			m.data = prepareGroupedRows(p.Groups, nil, false, "", nil)
		}
	}
	if len(m.data.rows) != 1 || m.data.rows[0].row.Ref != ref("BL-1") {
		t.Fatalf("list: %+v", m.data.rows)
	}
	m.detailTab = tabRelations
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	cursorAt := func(id string) {
		t.Helper()
		for i, target := range m.detailTargets {
			if target.Ref == ref(id) {
				for m.entryCursor < i {
					m, _ = detailKey(m, 'j', 0)
				}
				for m.entryCursor > i {
					m, _ = detailKey(m, 'k', 0)
				}
				return
			}
		}
		t.Fatalf("no entry for %s in %+v", id, m.detailTargets)
	}
	shown := func(id string) {
		t.Helper()
		if row, ok := m.selectedRow(); !ok || row.Ref != ref(id) || m.detailIdentity.Ref != ref(id) || !strings.Contains(detailHeader(m), id) {
			t.Fatalf("detail shows %+v, want %s", m.detailIdentity.Ref, id)
		}
		if m.selected != 0 || m.view != 0 || m.detailTab != tabRelations {
			t.Fatalf("list or view changed: selected %d view %d tab %d", m.selected, m.view, m.detailTab)
		}
	}

	cursorAt("BL-9")
	m, _ = detailKey(m, tea.KeyEnter, 0)
	if m.detached != nil || !m.detailOpen || m.notice != "BL-9 can't open: it isn't in tasks." {
		t.Fatalf("missing target: notice %q", m.notice)
	}
	if !strings.Contains(strings.Join(m.detailLines, "\n"), "? BL-9") {
		t.Fatal("missing target is not marked")
	}
	cursorAt("BL-2")
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	shown("BL-2")
	cursorAt("BL-3") // an inbound parent link, so the catalog prepared BL-2's reverse relations
	if !strings.Contains(strings.Join(m.detailLines, "\n"), "BL-1  Owner") {
		t.Fatal("followed item lost its inbound blocking relation")
	}

	// A double-click follows, as it opens a list row.
	y := m.detailBodyTop() + 1 + m.detailEntries[m.entryCursor] - m.detailScroll
	for range 2 {
		next, _ := m.Update(tea.MouseClickMsg{X: m.width - 10, Y: y, Button: tea.MouseLeft})
		m = next.(Model)
	}
	m = settleDetail(t, m)
	shown("BL-3")

	for _, want := range []string{"BL-2", "BL-1"} {
		m, _ = detailKey(m, tea.KeyBackspace, 0)
		m = settleDetail(t, m)
		shown(want)
	}
	if m.detached != nil || m.detailTargets[m.entryCursor].Ref != ref("BL-2") {
		t.Fatal("back did not end at the original selection and entry")
	}
	m, _ = detailKey(m, tea.KeyBackspace, 0)
	if m.notice != "No previous item." {
		t.Fatalf("back past the start: %q", m.notice)
	}
}

// detailHeader is the plain text of the detail pane's ID, status and title rows.
func detailHeader(m Model) string {
	return ansi.Strip(strings.Join(m.detailPane(m.detailWidth(), 2), "\n"))
}

// Catches list keys leaking into narrow detail as item keys (f s opening the
// status picker), n creating an item during a search, and esc closing detail
// before clearing the search.
func TestDetailListKeysAndFind(t *testing.T) {
	t.Parallel()
	m := shellFixture(3)
	m.width, m.height = 120, 30
	m.detailOpen, m.detailFocus = true, true
	m = queryKey(queryKey(m, "f"), "d")
	if !strings.Contains(m.query, "is:open,closed") || !m.detailFocus {
		t.Fatalf("split detail filter: %q", m.query)
	}
	m.width = 100
	m = queryKey(queryKey(m, "f"), "s")
	if m.notice != "Disabled: "+focusList || m.editor.open {
		t.Fatalf("narrow detail must refuse list keys: %q", m.notice)
	}

	m.detailLines = []string{"alpha", "\x1b[1mBeta\x1b[22m one", "gamma", "beta two"}
	m = queryType(m, "/beta")
	m = queryKey(m, "enter")
	if m.find.open || len(m.find.lines) != 2 || m.find.current != 0 {
		t.Fatalf("find: %+v", m.find)
	}
	m = queryKey(m, "n")
	m = queryKey(m, "n")
	if m.find.current != 0 || m.picker.open || m.capture.open {
		t.Fatalf("n must wrap through matches, not create: %+v", m.find)
	}
	m.theme = newTheme(false, false, nil, nil)
	if got, want := m.highlightFind(m.detailLines[1]), "\x1b[1m\x1b[7mBeta\x1b[27m\x1b[22m one"; got != want {
		t.Fatalf("highlight %q, want %q", got, want)
	}
	m = queryKey(m, "esc")
	if m.find.text != "" || !m.detailOpen {
		t.Fatal("first esc clears the search")
	}
	if m = queryKey(m, "esc"); m.detailOpen {
		t.Fatal("second esc closes detail")
	}
}

// Catches a History tab that needs renderer changes for a new group kind,
// hides a failed provider, opens away from the newest entry, can't expand a
// group in place, moves the cursor off the group when the newest-200 window
// shifts, panics expanding after the item is closed and reopened, leaves a
// cut-off summary unreadable, or hides the resume key on a resumable session.
func TestHistoryTabOpensAtNewestAndExpandsGroups(t *testing.T) {
	t.Parallel()
	m := detailFixture(t, 1, RenderMarkdown)
	at := func(minute int) time.Time { return time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC) }
	m.detailWorker.history = func(ctx context.Context, _ model.Item, _ []changes.Change) history.Result {
		return history.Compose(ctx,
			history.Provider{Name: "events", Load: func(context.Context) ([]history.Entry, error) {
				var entries []history.Entry
				for i := range 250 {
					entries = append(entries, history.Entry{ID: fmt.Sprintf("event:%d", i), Time: at(i), Kind: "item.updated", Summary: fmt.Sprintf("edit %d", i)})
				}
				entries = append(entries, history.Entry{ID: "event:long", Time: at(296), Kind: "item.updated", Summary: strings.Repeat("word ", 40) + "END"})
				return entries, nil
			}},
			history.Provider{Name: "ci", Load: func(context.Context) ([]history.Entry, error) {
				return []history.Entry{
					{ID: "ci:run", Time: at(300), Kind: "ci.run", Span: &history.Span{Start: at(300), State: "running"}, Summary: "build 7"},
					{ID: "ci:run:1", Time: at(301), Kind: "ci.step", Group: "ci:run", Summary: "lint passed"},
				}, nil
			}},
			history.Provider{Name: "claims", Load: func(context.Context) ([]history.Entry, error) { return nil, errors.New("worklease not found") }},
			history.Provider{Name: "sessions", Load: func(context.Context) ([]history.Entry, error) {
				return []history.Entry{{ID: "session:s1", Time: at(295), Kind: "session", Session: "s1", Span: &history.Span{Start: at(295), State: "open"}, Resumable: true, Summary: "claude"}}, nil
			}},
		)
	}
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	_ = m.switchDetailTab(tabHistory)
	m = settleDetail(t, m)
	if !strings.Contains(m.detailLines[0], "claims: unavailable (worklease not found)") {
		t.Fatalf("first line %q", m.detailLines[0])
	}
	content := m.View().Content
	if m.detailIDs[m.entryCursor] != "ci:run" || !strings.Contains(content, "build 7") || strings.Contains(content, "edit 0") || strings.Contains(content, "lint passed") {
		t.Fatalf("cursor %d of %v\n%s", m.entryCursor, m.detailIDs, content)
	}
	// ! scopes the actions menu's sessions to a session row, and only there.
	actions := func(m Model) string {
		next, _ := m.Update(tea.KeyPressMsg{Code: '!', Text: "!"})
		return next.(Model).actionSession
	}
	if got := actions(m); got != "" {
		t.Fatalf("! on %s scoped to session %q", m.detailIDs[m.entryCursor], got)
	}
	onSession := m
	onSession.entryCursor = slices.Index(m.detailIDs, "session:s1")
	if got := actions(onSession); got != "s1" {
		t.Fatalf("! on the session row scoped to %q", got)
	}
	if strings.Contains(content, "! resume") || !strings.Contains(onSession.View().Content, "! resume") {
		t.Fatal("resume key not shown only on the selected session row")
	}
	long := m
	long.entryCursor = slices.Index(m.detailIDs, "event:long")
	if strings.Contains(long.View().Content, "END") {
		t.Fatal("long summary fit its row; the test needs a longer one")
	}
	long, _ = detailKey(long, tea.KeyEnter, 0)
	long = settleDetail(t, long)
	if content := long.View().Content; !strings.Contains(content, "END") {
		t.Fatalf("expanded long summary still cut off\n%s", content)
	}
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	if content := m.View().Content; !m.detailOpen || !strings.Contains(content, "▾") || !strings.Contains(content, "lint passed") {
		t.Fatalf("group did not expand\n%s", content)
	}
	if m.detailIDs[m.entryCursor] != "ci:run" {
		t.Fatalf("cursor moved to %s after expanding", m.detailIDs[m.entryCursor])
	}
	// Clicks act on the entry drawn under the pointer: its row selects it,
	// its marker folds it, and the selected row's hint runs.
	click := func(text, at string) {
		t.Helper()
		y := drawnRow(t, m, text)
		next, _ := m.Update(tea.MouseClickMsg{X: drawnAt(t, m, y, at), Y: y, Button: tea.MouseLeft})
		m = next.(Model)
	}
	click("lint passed", "lint passed")
	if m.detailIDs[m.entryCursor] != "ci:run:1" {
		t.Fatalf("click selected %s", m.detailIDs[m.entryCursor])
	}
	click("build 7", "▾")
	m = settleDetail(t, m)
	if strings.Contains(m.View().Content, "lint passed") || m.detailIDs[m.entryCursor] != "ci:run" {
		t.Fatalf("marker click did not fold the group under it\n%s", m.View().Content)
	}
	click("claude", "claude")
	click("! resume", "! resume")
	if !m.launcher.open || m.actionSession != "s1" {
		t.Fatalf("hint click opened %v scoped to %q", m.launcher.open, m.actionSession)
	}
	m = send(m, "esc")
	// Reopening the same item after close must not toggle into a nil map.
	m.closeDetail()
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	_ = m.switchDetailTab(tabHistory)
	m = settleDetail(t, m)
	m.entryCursor = slices.Index(m.detailIDs, "ci:run")
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	if !m.historyOpen["ci:run"] {
		t.Fatal("group did not expand after reopen")
	}
}

// TestHistoryMeasurement times a warm History open on the loop: the tab switch
// and its View, then applying the worker's cached page and its View. The
// history is BL-1's in the 100k-event bench fixture: 1,000 events across 100
// sessions, plus 20 commits.
func TestHistoryMeasurement(t *testing.T) {
	if os.Getenv("BL_TUI_MEASURE") != "1" {
		return
	}
	m := detailFixture(t, 1, RenderMarkdown)
	at := func(n int) time.Time { return time.Date(2026, 1, 1, 0, 0, n, 0, time.UTC) }
	m.detailWorker.history = func(ctx context.Context, _ model.Item, _ []changes.Change) history.Result {
		return history.Compose(ctx,
			history.Provider{Name: "events", Load: func(context.Context) ([]history.Entry, error) {
				var entries []history.Entry
				for n := range 1000 {
					entries = append(entries, history.Entry{ID: fmt.Sprintf("event:%d", n), Time: at(n), Kind: "item.updated", Session: fmt.Sprintf("s%d", n%100), Summary: "title changed"})
				}
				return entries, nil
			}},
			history.Provider{Name: "sessions", Load: func(context.Context) ([]history.Entry, error) {
				var entries []history.Entry
				for n := range 100 {
					entries = append(entries, history.Entry{ID: fmt.Sprintf("session:s%d", n), Time: at(n), Kind: "session", Session: fmt.Sprintf("s%d", n), Span: &history.Span{Start: at(n), State: "open"}, Summary: "claude · active · open · resumable"})
				}
				return entries, nil
			}},
			history.Provider{Name: "git", Load: func(context.Context) ([]history.Entry, error) {
				var entries []history.Entry
				for n := range 20 {
					entries = append(entries, history.Entry{ID: fmt.Sprintf("git:%d", n), Time: at(n * 50), Kind: "commit", Actor: "Bench", Summary: fmt.Sprintf("edit BL-1 %d", n)})
				}
				return entries, nil
			}},
		)
	}
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	_ = m.switchDetailTab(tabRelations)
	m = settleDetail(t, m)
	samples := make([]time.Duration, 0, 200)
	for range 200 {
		start := time.Now()
		_ = m.switchDetailTab(tabHistory)
		_ = m.View()
		loop := time.Since(start)
		result := <-m.detailWorker.results
		start = time.Now()
		next, _ := m.Update(detailResultMsg(result))
		m = next.(Model)
		_ = m.View()
		samples = append(samples, loop+time.Since(start))
		if m.detailTab != tabHistory || len(m.detailIDs) != 120 {
			t.Fatalf("tab %d entries %d", m.detailTab, len(m.detailIDs))
		}
		_ = m.switchDetailTab(tabRelations)
		m = settleDetail(t, m)
	}
	slices.Sort(samples)
	t.Logf("warm History open Update+View n=%d p95=%s", len(samples), samples[(len(samples)*95-1)/100])
}

// Catches enter on a Changes entry not opening its change view, ! losing the
// change there, backspace not returning to the entry it opened from, and held
// j in a large Files tab doing per-key work beyond the §11 navigation budget,
// whose in-process time BL_TUI_MEASURE=1 logs.
func TestChangeView(t *testing.T) {
	t.Parallel()
	m := detailFixture(t, 1, RenderMarkdown)
	other := changes.Change{ID: "branch:bl-1-old", Stage: changes.StageBranch, State: changes.StateOpen, Branch: "bl-1-old"}
	change := changes.Change{ID: "branch:bl-1-fix", Stage: changes.StageWorktree, State: changes.StateOpen, Branch: "bl-1-fix", Worktree: "/tmp/wt"}
	m.data.rows[len(m.data.rows)-1].row.Changes = []changes.Change{other, change}
	files := make([]changes.File, 5000)
	for i := range files {
		files[i] = changes.File{Path: fmt.Sprintf("internal/pkg%d/file.go", i), Status: changes.FileModified, Additions: i, Deletions: 1}
	}
	m.detailWorker.changeDetail = func(_ context.Context, c changes.Change, _ bool) (changes.Detail, error) {
		if c.ID != change.ID {
			return changes.Detail{}, errors.New("read another change")
		}
		return changes.Detail{Files: files, Commits: []changes.Commit{{SHA: "abcdef1234", Subject: "fix it"}}}, nil
	}
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	_ = m.switchDetailTab(tabChanges)
	m = settleDetail(t, m)
	m, _ = detailKey(m, 'j', 0)
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	if content := m.View().Content; m.change == nil || m.detailTab != tabChangeOverview || !strings.Contains(content, "Commits") || !strings.Contains(content, "5000") {
		t.Fatalf("change view not open on tab %d\n%s", m.detailTab, content)
	}
	if next, _ := m.Update(tea.KeyPressMsg{Code: '!', Text: "!"}); next.(Model).actionChange == nil || next.(Model).actionChange.Branch != "bl-1-fix" {
		t.Fatal("! in the change view lost its change")
	}
	m, _ = detailKey(m, tea.KeyTab, 0)
	m = settleDetail(t, m)
	samples := make([]time.Duration, 0, len(files))
	for range len(files) - 1 {
		start := time.Now()
		m, _ = detailKey(m, 'j', 0)
		_ = m.View()
		samples = append(samples, time.Since(start))
	}
	if m.detailTab != tabChangeFiles || m.entryCursor != len(files)-1 || !strings.Contains(m.View().Content, "pkg4999") {
		t.Fatalf("Files cursor %d on tab %d", m.entryCursor, m.detailTab)
	}
	if os.Getenv("BL_TUI_MEASURE") == "1" {
		slices.Sort(samples)
		t.Logf("held j in 5,000 files Update+View p95=%s max=%s", samples[(len(samples)*95-1)/100], samples[len(samples)-1])
	}
	m, _ = detailKey(m, tea.KeyBackspace, 0)
	m = settleDetail(t, m)
	if m.change != nil || m.detailTab != tabChanges || m.entryCursor != 1 {
		t.Fatalf("backspace left tab %d cursor %d", m.detailTab, m.entryCursor)
	}
	// esc from Files must not carry the Files cursor into Changes.
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	m, _ = detailKey(m, tea.KeyTab, 0)
	m = settleDetail(t, m)
	m, _ = detailKey(m, 'G', 0)
	m, _ = detailKey(m, tea.KeyEscape, 0)
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	if c, ok := m.selectedChange(); m.detailTab != tabChanges || !ok || c.ID != change.ID {
		t.Fatalf("reopened on tab %d cursor %d", m.detailTab, m.entryCursor)
	}
	// A change whose files can't be read still shows its own facts.
	m, _ = detailKey(m, 'k', 0)
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	if content := m.View().Content; !strings.Contains(content, "bl-1-old") || !strings.Contains(content, "read another change") {
		t.Fatalf("failed read hid the change\n%s", content)
	}
}

// Catches the Diff tab losing its unified and split layouts, [ ] and [c ]c
// landing off the file or hunk, m not drawing a large file, enter on a History
// commit not opening its diff or backspace not returning to that entry, and
// held j over a 10,000-line diff beyond the §11 navigation budget, measured
// with BL_TUI_MEASURE=1.
func TestChangeDiff(t *testing.T) {
	t.Parallel()
	m := detailFixture(t, 1, RenderMarkdown)
	change := changes.Change{ID: "branch:bl-1-fix", Stage: changes.StageBranch, State: changes.StateOpen, Branch: "bl-1-fix", Head: "abc"}
	m.data.rows[len(m.data.rows)-1].row.Changes = []changes.Change{change}
	hunk := func(start, n int) string {
		var b strings.Builder
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@ func f() {\n", start, n, start, n)
		for i := range n {
			fmt.Fprintf(&b, "-\tx := %d\n+\tx := %d + 1\n", i, i)
		}
		return b.String()
	}
	file := func(path, patch string) changes.File {
		return changes.File{Path: path, Status: changes.FileModified, Additions: strings.Count(patch, "\n+"), Deletions: strings.Count(patch, "\n-"), Patch: patch}
	}
	branch := []changes.File{file("a.go", hunk(1, 30)+hunk(80, 30)), file("b.go", hunk(1, 30)), file("big.go", hunk(1, 1500)), file("c.go", hunk(1, 30))}
	var commit []changes.File
	for i := range 100 {
		commit = append(commit, file(fmt.Sprintf("pkg/f%d.go", i), hunk(1, 100)))
	}
	m.detailWorker.changeDetail = func(_ context.Context, c changes.Change, patch bool) (changes.Detail, error) {
		files := branch
		if c.ID == "commit:abc1234" {
			files = commit
		}
		if !patch {
			files = nil
		}
		return changes.Detail{Files: files, Patched: patch}, nil
	}
	at := func(n int) time.Time { return time.Date(2026, 1, 1, 0, 0, n, 0, time.UTC) }
	m.detailWorker.history = func(context.Context, model.Item, []changes.Change) history.Result {
		return history.Result{Entries: []history.Entry{
			{ID: "git:abc1234", Time: at(1), Kind: "commit", Summary: "edit BL-1", Provider: "git"},
			{ID: "event:1", Time: at(2), Kind: "item.updated", Summary: "title changed", Provider: "events"},
		}}
	}
	line := func(m Model, text string) string {
		for _, l := range m.detailLines {
			if strings.Contains(l, text) {
				return l
			}
		}
		t.Fatalf("no line with %q", text)
		return ""
	}
	m.width = 100
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	_ = m.switchDetailTab(tabChanges)
	m = settleDetail(t, m)
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	m, _ = detailKey(m, tea.KeyTab, 0)
	m, _ = detailKey(m, tea.KeyTab, 0)
	m = settleDetail(t, m)
	if m.detailTab != tabChangeDiff || len(m.detailFiles) != 4 || len(m.detailHunks) != 4 {
		t.Fatalf("Diff tab %d files %v hunks %v", m.detailTab, m.detailFiles, m.detailHunks)
	}
	if l := line(m, "x := 7 + 1"); strings.Contains(l, "│") || !strings.Contains(l, "8 + ") {
		t.Fatalf("unified line %q", l)
	}
	steps := []struct {
		keys string
		want func() int
	}{
		{"]", func() int { return m.detailFiles[1] }},
		{"]", func() int { return m.detailFiles[2] }},
		{"[", func() int { return m.detailFiles[1] }},
		{"[c", func() int { return m.detailHunks[1] }},
		{"[c", func() int { return m.detailHunks[0] }},
		{"]c", func() int { return m.detailHunks[1] }},
	}
	for _, step := range steps {
		for _, r := range step.keys {
			m, _ = detailKey(m, r, 0)
		}
		if m.detailScroll != step.want() {
			t.Fatalf("%s scrolled to %d, want %d", step.keys, m.detailScroll, step.want())
		}
	}
	line(m, "KiB diff")
	m, _ = detailKey(m, 'm', 0)
	m = settleDetail(t, m)
	line(m, "x := 1499 + 1")
	next, _ := m.Update(tea.WindowSizeMsg{Width: 400, Height: 24})
	m = settleDetail(t, next.(Model))
	if m.detailWidth()-2*detailPad < splitDiffWidth {
		t.Fatalf("detail width %d too narrow to split", m.detailWidth())
	}
	if l := line(m, "x := 7 + 1"); !strings.Contains(l, "│") || !strings.Contains(l, "x := 7 ") {
		t.Fatalf("split line %q", l)
	}
	m, _ = detailKey(m, tea.KeyBackspace, 0)
	m = settleDetail(t, m)
	_ = m.switchDetailTab(tabHistory)
	m = settleDetail(t, m)
	m.entryCursor = slices.Index(m.detailIDs, "git:abc1234")
	m, _ = detailKey(m, tea.KeyEnter, 0)
	m = settleDetail(t, m)
	if m.change == nil || m.change.ID != "commit:abc1234" || m.detailTab != tabChangeDiff || len(m.detailLines) < 10000 {
		t.Fatalf("History enter opened %v on tab %d with %d lines", m.change, m.detailTab, len(m.detailLines))
	}
	samples := make([]time.Duration, 0, 2000)
	for range 2000 {
		start := time.Now()
		m, _ = detailKey(m, 'j', 0)
		_ = m.View()
		samples = append(samples, time.Since(start))
	}
	if m.detailScroll != 2000 {
		t.Fatalf("held j scrolled to %d", m.detailScroll)
	}
	if os.Getenv("BL_TUI_MEASURE") == "1" {
		slices.Sort(samples)
		t.Logf("held j in a %d-line diff Update+View p95=%s max=%s", len(m.detailLines), samples[(len(samples)*95-1)/100], samples[len(samples)-1])
	}
	m, _ = detailKey(m, tea.KeyBackspace, 0)
	m = settleDetail(t, m)
	if m.change != nil || m.detailTab != tabHistory || m.detailIDs[m.entryCursor] != "git:abc1234" {
		t.Fatalf("backspace left tab %d cursor %d", m.detailTab, m.entryCursor)
	}
}
