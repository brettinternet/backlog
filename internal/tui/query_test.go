package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
)

type querySource struct{ items []model.Item }

func (s querySource) Info() source.Info { return source.Info{ID: "tasks"} }
func (s querySource) Load(context.Context, source.Cursor) (source.Delta, error) {
	return source.Delta{Items: s.items, Coverage: source.Complete}, nil
}
func (querySource) Read(context.Context, string) (model.Item, error) {
	return model.Item{}, errors.New("unused")
}
func (querySource) Apply(context.Context, model.Mutation) (model.Item, error) {
	return model.Item{}, errors.New("unused")
}
func queryFixture(t *testing.T, count int) Model {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	items := make([]model.Item, count)
	for i := range items {
		items[i] = model.Item{Ref: model.Ref{Source: "tasks", ID: fmt.Sprintf("BL-%d", i+1)}, Title: fmt.Sprintf("Task %d 修正", i+1), Status: "todo", Category: model.Todo, Priority: model.High, Labels: []string{"two words", "a,b"}, Version: "1"}
	}
	cfg := project.Config{Settings: project.Settings{Statuses: []project.StatusConfig{{Key: "todo", Name: "To Do", Category: "todo"}, {Key: "done", Name: "Done", Category: "done"}}, Ready: project.ReadyConfig{Statuses: []string{"todo"}}, Labels: []string{"two words", "a,b"}}}
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": querySource{items}})
	t.Cleanup(store.Close)
	projector := engine.NewProjector(ctx, cfg)
	t.Cleanup(projector.Close)
	ch, unsub := store.Subscribe()
	defer unsub()
	var snapshot *engine.Snapshot
	for snapshot == nil || snapshot.Len() != count {
		select {
		case snapshot = <-ch:
		case <-ctx.Done():
			t.Fatal("snapshot deadline")
		}
	}
	m := New(Options{Context: ctx, Projector: projector, NoColor: true, Project: "fixture"})
	m.snapshot = snapshot
	m.project()
	return settleQuery(t, m)
}
func settleQuery(t *testing.T, m Model) Model {
	t.Helper()
	for {
		select {
		case p := <-m.opts.Projector.Results():
			if p.Request != m.request {
				continue
			}
			next, _ := m.Update(projectionMsg(p))
			m = next.(Model)
			if p.Error == nil {
				next, _ = m.Update(m.prepare()())
				m = next.(Model)
			}
			return m
		case <-m.opts.Context.Done():
			t.Fatal("projection deadline")
			return m
		}
	}
}
func queryKey(m Model, s string) Model {
	key := tea.KeyPressMsg{}
	switch s {
	case "enter":
		key.Code = tea.KeyEnter
	case "tab":
		key.Code = tea.KeyTab
	case "esc":
		key.Code = tea.KeyEscape
	case "backspace":
		key.Code = tea.KeyBackspace
	case "left":
		key.Code = tea.KeyLeft
	case "down":
		key.Code = tea.KeyDown
	case "ctrl+u":
		key.Code = 'u'
		key.Mod = tea.ModCtrl
	default:
		key.Code = []rune(s)[0]
		key.Text = s
	}
	next, _ := m.Update(key)
	return next.(Model)
}
func queryType(m Model, text string) Model {
	for _, r := range text {
		m = queryKey(m, string(r))
	}
	return m
}

// Catches canceled drafts being committed, completion inserting unquoted values,
// and syntax errors blanking the last valid list or losing their byte position.
func TestQueryEditor(t *testing.T) {
	t.Parallel()
	m := queryFixture(t, 12)
	original := m.query
	m = queryKey(m, "/")
	m = queryKey(m, "ctrl+u")
	m = queryType(m, "sta")
	m = settleQuery(t, m)
	m = queryKey(m, "tab")
	if m.editor.text != "status:" {
		t.Fatalf("field completion: %q", m.editor.text)
	}
	m = settleQuery(t, m)
	m = queryType(m, `"To D`)
	m = settleQuery(t, m)
	m = queryKey(m, "tab")
	if m.editor.text != "status:todo" {
		t.Fatalf("named value completion: %q", m.editor.text)
	}
	m = settleQuery(t, m)
	m = queryKey(m, "enter")
	m = settleQuery(t, m)
	if m.editor.open || m.query != "status:todo" || len(m.data.rows) != 12 {
		t.Fatalf("apply: %+v", m.editor)
	}
	m = queryKey(m, "/")
	m = queryKey(m, "ctrl+u")
	m = queryType(m, `label:"two`)
	m = settleQuery(t, m)
	m = queryKey(m, "tab")
	if m.editor.text != `label:"two words"` {
		t.Fatalf("quoted completion: %q", m.editor.text)
	}
	m = settleQuery(t, m)
	m = queryKey(m, "esc")
	m = settleQuery(t, m)
	if m.query != "status:todo" {
		t.Fatal("escape did not restore committed query")
	}
	m = queryKey(m, "/")
	m = queryKey(m, "ctrl+u")
	m = queryType(m, "Task 1")
	m = settleQuery(t, m)
	valid := m.View().Content
	rows := len(m.data.rows)
	m = queryType(m, " lable:tui")
	m = settleQuery(t, m)
	if !strings.Contains(m.queryError, `unknown field "lable" at 7`) || !strings.Contains(m.View().Content, "Task 1") || len(m.data.rows) != rows {
		t.Fatalf("invalid query lost valid list: %s", m.View().Content)
	}
	golden(t, "query-error", m.View().Content)
	m = queryKey(m, "enter")
	if !m.editor.open {
		t.Fatal("invalid query applied")
	}
	m = queryKey(m, "esc")
	m = settleQuery(t, m)
	if m.query != "status:todo" || len(m.data.rows) != 12 || valid == m.View().Content {
		t.Fatal("restore failed")
	}
	m.query = original
	m.project()
	m = settleQuery(t, m)
	// Quick picker chooses a snapshot value and applies it; comma values are quoted.
	m = queryKey(m, "f")
	m = queryKey(m, "l")
	m = settleQuery(t, m)
	m = queryKey(m, "down")
	m = queryKey(m, "enter")
	m = settleQuery(t, m)
	q, err := query.Parse(m.query, query.Options{})
	if err != nil || q.Terms[len(q.Terms)-1].Values[0].Text != "a,b" || m.editor.open {
		t.Fatalf("picker: %s %v", m.query, err)
	}
	// Unknown snapshot values are warnings, not syntax errors.
	m = queryKey(m, "/")
	m = queryKey(m, "ctrl+u")
	m = queryType(m, "status:missing")
	m = settleQuery(t, m)
	if m.queryError != "" || m.queryWarning == "" || len(m.data.rows) != 0 {
		t.Fatal("unknown value must warn and project")
	}
	// A background refresh (claims poll, file change) keeps the dropdown and
	// its highlight; enter inserts the highlighted value instead of failing.
	m = queryKey(m, "ctrl+u")
	m = queryType(m, "group:")
	m = settleQuery(t, m)
	if m.queryError != "" || len(m.editor.candidates) < 2 {
		t.Fatalf("value list: %q %v", m.queryError, m.editor.candidates)
	}
	m = queryKey(m, "down")
	want := "group:" + m.editor.candidates[1].Value
	m.refreshClaims()
	m = settleQuery(t, m)
	m = queryKey(m, "enter")
	if m.editor.open || m.query != want {
		t.Fatalf("enter after refresh: open=%v query=%q error=%q", m.editor.open, m.query, m.queryError)
	}
	// A partial value is guided, not an error; enter takes the first match.
	m = settleQuery(t, m)
	m = queryKey(m, "/")
	m = queryKey(m, "ctrl+u")
	m = queryType(m, "group:la")
	m = settleQuery(t, m)
	if m.queryError != "" {
		t.Fatalf("partial value flashed an error: %q", m.queryError)
	}
	m = queryKey(m, "enter")
	if m.editor.open || m.query != "group:label" {
		t.Fatalf("partial value: %q", m.query)
	}
}

// Catches a documented second key silently falling through to navigation or an
// edit command, including the former za/assignee collision.
func TestQueryMenuKeys(t *testing.T) {
	t.Parallel()
	cases := []struct{ prefix, key, field, value string }{
		{"f", "s", "picker", "status"}, {"f", "p", "picker", "priority"}, {"f", "a", "picker", "assignee"}, {"f", "l", "picker", "label"}, {"f", "t", "picker", "type"}, {"f", "m", "picker", "milestone"}, {"f", "r", "is", "ready"}, {"f", "d", "is", "open,closed"}, {"f", "c", "clear", ""},
		{"o", "n", "sort", "next"}, {"o", "p", "sort", "priority"}, {"o", "r", "sort", "rank"}, {"o", "u", "sort", "updated"}, {"o", "c", "sort", "created"}, {"o", "d", "sort", "due"}, {"o", "i", "sort", "id"}, {"o", "o", "sort", "-next"},
		{"z", "s", "group", "status"}, {"z", "C", "group", "category"}, {"z", "p", "group", "priority"}, {"z", "A", "group", "assignee"}, {"z", "l", "group", "label"}, {"z", "m", "group", "milestone"}, {"z", "T", "group", "type"}, {"z", "P", "group", "parent"}, {"z", "n", "group", "none"}, {"z", "a", "fold", ""}, {"z", "M", "fold-all", ""}, {"z", "R", "unfold-all", ""},
	}
	for _, tc := range cases {
		t.Run(tc.prefix+tc.key, func(t *testing.T) {
			m := shellFixture(3)
			m = queryKey(m, tc.prefix)
			if !strings.Contains(m.View().Content, tc.key+" ") {
				t.Fatal("missing which-key hint")
			}
			m = queryKey(m, tc.key)
			switch tc.field {
			case "picker":
				if !m.editor.open || !strings.HasSuffix(m.editor.text, tc.value+":") {
					t.Fatal("picker not opened")
				}
			case "clear":
				if m.query != "sort:next" {
					t.Fatalf("clear lost sort: %s", m.query)
				}
			case "fold", "fold-all", "unfold-all":
				next, _ := m.Update(m.prepare()())
				m = next.(Model)
				want := 12
				if tc.field == "fold" {
					want = 9
				}
				if tc.field == "fold-all" {
					want = 3
				}
				if len(m.data.rows) != want {
					t.Fatalf("fold rows: %d", len(m.data.rows))
				}
			default:
				if !strings.Contains(m.query, tc.field+":"+tc.value) {
					t.Fatalf("menu query %q", m.query)
				}
			}
		})
	}
	m := queryKey(shellFixture(3), "F")
	if m.query != "sort:next" {
		t.Fatal("F must clear predicates")
	}
	m = queryKey(shellFixture(3), "z")
	m = queryKey(m, "esc")
	if m.prefix != "" {
		t.Fatal("menu cancel")
	}
	m = shellFixture(3)
	m.query = "sort:priority,-id group:status"
	m = queryKey(m, "o")
	m = queryKey(m, "o")
	if m.query != "group:status sort:-priority,id" {
		t.Fatal("reverse multisort")
	}
	m = queryKey(m, "z")
	m = queryKey(m, "M")
	next, _ := m.Update(m.prepare()())
	m = next.(Model)
	m = queryKey(m, "z")
	m = queryKey(m, "a")
	next, _ = m.Update(m.prepare()())
	m = next.(Model)
	if len(m.data.rows) != 6 {
		t.Fatal("toggle must unfold one group after fold-all")
	}
}

// Catches highlighting of negated/field terms, terminal injection, and Unicode
// byte slicing; golden retains ANSI so style loss is observable.
func TestQueryMatchGolden(t *testing.T) {
	t.Parallel()
	m := queryFixture(t, 3)
	m.theme = newTheme(false, true, nil, nil)
	m = queryKey(m, "/")
	m = queryKey(m, "ctrl+u")
	m = queryType(m, "修正")
	m = settleQuery(t, m)
	m = queryKey(m, "enter")
	m = settleQuery(t, m)
	content := m.View().Content
	if !strings.Contains(content, "\x1b[1;4m修正") {
		t.Fatalf("missing match highlight: %q", content)
	}
	golden(t, "query-highlight", content)
	m.theme = newTheme(true, false, nil, nil)
	if strings.Contains(m.View().Content, "\x1b") {
		t.Fatal("NO_COLOR emitted styling")
	}
}

// Catches worker backpressure freezing keys and obsolete projection/preparation
// results repainting a newer query. Timing is opt-in, never a default gate.
func TestQueryLatest50k(t *testing.T) {
	t.Parallel()
	m := queryFixture(t, 50_000)
	m = queryKey(m, "/")
	m = queryKey(m, "ctrl+u")
	m = queryType(m, "Task")
	stale := m.request
	m = queryType(m, " 49999")
	latest := m.request
	if latest <= stale || m.editor.text != "Task 49999" {
		t.Fatal("input stalled behind projection")
	}
	next, _ := m.Update(projectionMsg(engine.Projection{Request: stale, Groups: shellFixture(1).groups}))
	m = next.(Model)
	if m.request != latest || len(m.data.rows) != 50_000 {
		t.Fatal("stale projection changed rows")
	}
	m = settleQuery(t, m)
	if len(m.data.rows) != 1 || m.data.rows[0].row.Ref.ID != "BL-49999" {
		t.Fatalf("latest query lost: %d rows", len(m.data.rows))
	}
	old := m.prepare()
	m = queryType(m, "0")
	next, _ = m.Update(old())
	m = next.(Model)
	if len(m.data.rows) != 1 {
		t.Fatal("stale preparation changed rows")
	}
	m = settleQuery(t, m)
	if len(m.data.rows) != 0 {
		t.Fatal("final query did not win")
	}
	if os.Getenv("BL_TUI_MEASURE") != "1" {
		return
	}
	samples := make([]time.Duration, 40)
	for i := range samples {
		m = queryKey(m, "ctrl+u")
		start := time.Now()
		m = queryType(m, fmt.Sprintf("Task %d", 49900+i))
		m = settleQuery(t, m)
		_ = m.View()
		samples[i] = time.Since(start)
	}
	slices.Sort(samples)
	t.Logf("50k filter keystroke-to-list n=%d p95=%s budget=80ms", len(samples), samples[(len(samples)*95-1)/100])
}
