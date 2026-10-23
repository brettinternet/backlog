package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

func prepareBoard(m Model) Model {
	next, _ := m.Update(m.prepare()())
	return next.(Model)
}
func boardFixture(count int) Model {
	m := shellFixture(count)
	m.view = 4
	m.query = m.views[4].Config.Query
	m.groups = append(m.groups[:1], append([]engine.RowGroup{{Value: "Empty"}}, m.groups[1:]...)...)
	m.opts.Statuses = []project.StatusConfig{{Key: "To Do"}, {Key: "Empty"}, {Key: "Active"}, {Key: "Done"}}
	return prepareBoard(m)
}

// Catches crossing column boundaries vertically, skipping empty columns, shared
// scroll offsets, and losing a column's cursor while horizontally offscreen.
func TestBoardNavigation(t *testing.T) {
	t.Parallel()
	m := boardFixture(30)
	m = send(m, "2", "5", "j")
	want, _ := m.selectedRow()
	if want.Ref.ID != "BL-26" {
		t.Fatal(want)
	}
	m = send(m, "l")
	if _, ok := m.selectedRow(); ok || m.boardColumn() != 1 {
		t.Fatal("empty column cannot be focused")
	}
	m = send(m, "j", "G", "k")
	if m.boardColumn() != 1 {
		t.Fatal("vertical motion left empty column")
	}
	m = send(m, "l", "j", "l")
	if m.board.left != 2 {
		t.Fatalf("horizontal scroll: %d", m.board.left)
	}
	m = send(m, "h", "h", "h")
	if got, _ := m.selectedRow(); got.Ref != want.Ref {
		t.Fatalf("column cursor lost: %+v", got)
	}
	if m.board.positions["To Do"].top == 0 || m.board.positions["Active"].top != 0 {
		t.Fatal("columns share scroll")
	}
	m = send(m, "G", "j")
	if got, _ := m.selectedRow(); got.Ref.ID != "BL-30" {
		t.Fatal(got)
	}
	for _, width := range []int{80, 160} {
		m = boardFixture(3)
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 12})
		m = next.(Model)
		golden(t, fmt.Sprintf("board-%d", width), m.View().Content)
	}
	// Help replaces the board; the column header must not repeat its first action.
	if help := send(m, "?").View().Content; strings.Count(help, "Move down") != 1 {
		t.Fatalf("help header duplicated:\n%s", help)
	}
}

type boardWriteSource struct {
	*editFakeSource
	entered, release chan struct{}
	fail             bool
	once             sync.Once
}

func (s *boardWriteSource) Apply(ctx context.Context, mutation model.Mutation) (model.Item, error) {
	s.once.Do(func() {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	})
	if ctx.Err() != nil {
		return model.Item{}, ctx.Err()
	}
	if s.fail {
		return model.Item{}, source.Refuse("offline", "write failed")
	}
	return s.editFakeSource.Apply(ctx, mutation)
}

// Catches optimistic moves selecting the old row index, rollback losing the
// card, and treating a non-status group as the status workflow.
func TestBoardStatusMoves(t *testing.T) {
	t.Parallel()
	for _, group := range []string{"status", "priority"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%t", group, fail), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				item := editItem("BL-1", "Move me")
				other := editItem("BL-2", "Stay here")
				other.Priority = model.Low
				statuses := []project.StatusConfig{{Key: "todo", Category: "todo"}, {Key: "active", Category: "active"}, {Key: "done", Category: "done"}}
				cfg := project.Config{Settings: project.Settings{Statuses: statuses}}
				backend := &boardWriteSource{editFakeSource: newEditFakeSource([]model.Item{item, other}, allEditCapabilities), entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
				store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
				defer store.Close()
				waitEdit(t, func() bool { return store.Current().Len() == 2 })
				projector := engine.NewProjector(ctx, cfg)
				defer projector.Close()
				m := New(Options{NoColor: true, Store: store, Context: ctx, Statuses: statuses})
				m.view = 4
				m.query = "is:open group:" + group
				projectRows := func() {
					projector.Request(store.Current(), m.query)
					p := <-projector.Results()
					if p.Error != nil {
						t.Fatal(p.Error)
					}
					m.groups = p.Groups
					m.ungrouped = p.Ungrouped
					m = prepareBoard(m)
				}
				projectRows()
				if group == "priority" {
					m = send(m, "j", "k", "l")
					if row, ok := m.selectedRow(); !ok || row.Ref != other.Ref {
						t.Fatalf("non-status column navigation: %+v", row)
					}
					m = send(m, "h")
					if m.picker.open {
						t.Fatal("l opened labels instead of navigating")
					}
				}
				next, cmd := m.Update(editKeyMessage(">"))
				m = next.(Model)
				result := make(chan tea.Msg, 1)
				go func() { result <- cmd() }()
				<-backend.entered
				projectRows()
				row, ok := m.selectedRow()
				if !ok || row.Ref != item.Ref || !row.Pending || row.Status != "active" {
					t.Fatalf("optimistic move: %+v", row)
				}
				close(backend.release)
				next, _ = m.Update(<-result)
				m = next.(Model)
				projectRows()
				row, ok = m.selectedRow()
				want := "active"
				if fail {
					want = "todo"
				}
				if !ok || row.Ref != item.Ref || row.Pending || row.Status != want || (row.Error != "") != fail {
					t.Fatalf("settled move: %+v", row)
				}
				if !fail {
					m = editPress(m, "<")
					projectRows()
					row, _ = m.selectedRow()
					if row.Status != "todo" || row.Ref != item.Ref {
						t.Fatal(row)
					}
				}
			})
		}
	}
}

// Catches quick-add falling back to the project default in an empty column;
// non-status grouping must not supply an invalid status.
func TestBoardCaptureStatus(t *testing.T) {
	t.Parallel()
	caps := model.Capabilities{model.Create: true}
	m, _ := editFixture(t, []model.Item{editItem("BL-1", "First")}, caps, Options{CreateSource: "tasks", CreateStatus: "todo"})
	m.view = 4
	m.query = "is:open group:status"
	m = prepareBoard(m)
	m = send(m, "l", "n")
	m.capture.line = newLine("New card")
	m.expandCapture(false)
	item, err := captureItem(m.capture.fields, "tasks")
	if err != nil || item.Status != "active" {
		t.Fatalf("column default: %+v %v", item, err)
	}
	m.query = "is:open group:priority"
	m.capture.line = newLine("Another")
	m.expandCapture(false)
	item, err = captureItem(m.capture.fields, "tasks")
	if err != nil || item.Status != "todo" {
		t.Fatalf("non-status default: %+v %v", item, err)
	}
}

func BenchmarkBoardRender(b *testing.B) {
	for _, items := range []int{100, 50000} {
		b.Run(fmt.Sprint(items), func(b *testing.B) {
			m := boardFixture(items / 3)
			for i := 0; i < items%3; i++ {
				m.groups[0].Rows = append(m.groups[0].Rows, engine.Row{Ref: model.Ref{Source: "tasks", ID: fmt.Sprintf("EXTRA-%d", i)}, Status: "To Do", Title: "Extra card"})
			}
			m = prepareBoard(m)
			m.width = 160
			m.keepBoardVisible()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				v := m.View()
				if !strings.Contains(v.Content, "To Do") {
					b.Fatal("missing board")
				}
			}
		})
	}
}
