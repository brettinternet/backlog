package tui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/charmbracelet/x/ansi"
)

var updateGolden = flag.Bool("update", false, "update TUI golden views")

func shellFixture(count int) Model {
	m := New(Options{NoColor: true, Project: "fixture"})
	for g := 0; g < 3; g++ {
		group := engine.RowGroup{Value: []string{"To Do", "Active", "Done"}[g]}
		for i := 0; i < count; i++ {
			group.Rows = append(group.Rows, engine.Row{Ref: model.Ref{Source: "tasks", ID: fmt.Sprintf("BL-%d", g*count+i+1)}, Title: "修正 🚀 selection", Status: group.Value, Priority: model.High, Pending: i == 1, Error: map[bool]string{true: "conflict"}[i == 2], OpenBlockers: 1, ChildrenDone: 2, ChildrenTotal: 3})
		}
		m.groups = append(m.groups, group)
	}
	m.data = prepareRows(m.groups, nil)
	m.sources = []sourceBadge{{id: "tasks", state: "synced"}}
	return m
}
func send(m Model, keys ...string) Model {
	for _, s := range keys {
		k := tea.KeyPressMsg{}
		switch s {
		case "down":
			k.Code = tea.KeyDown
		case "up":
			k.Code = tea.KeyUp
		case "esc":
			k.Code = tea.KeyEscape
		default:
			if strings.HasPrefix(s, "ctrl+") {
				k.Code = rune(s[len(s)-1])
				k.Mod = tea.ModCtrl
			} else {
				k.Code = []rune(s)[0]
			}
		}
		next, _ := m.Update(k)
		m = next.(Model)
	}
	return m
}
func golden(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(expected) != content {
		t.Fatalf("%s differs; run task cli:test PKG=./internal/tui -- -update\n%s", path, content)
	}
}

// A later source refresh must not hide the warning that executable project
// declarations are still inert pending explicit consent.
func TestTrustBannerSurvivesNavigation(t *testing.T) {
	t.Parallel()
	m := shellFixture(2)
	m.opts.TrustWarning = "Untrusted project config · bl trust --show"
	m.width, m.height = 100, 24
	m = send(m, "j")
	view := m.View().Content
	if !strings.Contains(view, m.opts.TrustWarning) || strings.Contains(view, "✓ tasks") {
		t.Fatal(view)
	}
}

// Catches count prefixes being treated as view switches, incorrect group/page
// offsets, and cursor/fold regressions. Goldens cover the resulting viewport.
func TestShellNavigation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		keys     []string
		selected int
	}{
		{"down", []string{"j", "down"}, 2}, {"up", []string{"j", "j", "k", "up"}, 0},
		{"count", []string{"5", "j"}, 5}, {"first", []string{"G", "g", "g"}, 0},
		{"last", []string{"G"}, 62}, {"numbered", []string{"2", "5", "G"}, 24},
		{"half", []string{"ctrl+d", "ctrl+d", "ctrl+u"}, 10},
		{"full", []string{"ctrl+f", "ctrl+f", "ctrl+b"}, 21},
		{"groups", []string{"]", "]", "["}, 21},
	}
	var transcript strings.Builder
	for _, c := range cases {
		m := send(shellFixture(20), c.keys...)
		if m.selected != c.selected {
			t.Fatalf("%s selected %d, want %d", c.name, m.selected, c.selected)
		}
		fmt.Fprintf(&transcript, "== %s ==\n%s\n", c.name, m.View().Content)
	}
	m := send(shellFixture(20), "]", "z", "a")
	next, _ := m.Update(m.prepare()())
	m = next.(Model)
	if len(m.data.rows) != 43 {
		t.Fatal("fold did not hide rows")
	}
	fmt.Fprintf(&transcript, "== fold ==\n%s\n", m.View().Content)
	// J and K skip headers and the folded group in both directions.
	if jk := send(m, "J"); jk.selected != 23 {
		t.Fatalf("J selected %d", jk.selected)
	} else if jk = send(jk, "K"); jk.selected != 20 {
		t.Fatalf("K selected %d", jk.selected)
	}
	m = send(m, "z", "a")
	next, _ = m.Update(m.prepare()())
	m = next.(Model)
	if len(m.data.rows) != 63 {
		t.Fatal("unfold lost rows")
	}
	fmt.Fprintf(&transcript, "== unfold ==\n%s\n", m.View().Content)
	for n := 1; n <= 9; n++ {
		m = send(shellFixture(2), fmt.Sprint(n))
		next, _ = m.Update(viewDue(m.inputSerial))
		m = next.(Model)
		want := 0
		if n <= len(viewNames) {
			want = n - 1
		}
		if m.view != want {
			t.Fatalf("view %d: %d", n, m.view)
		}
		if n <= len(viewNames) {
			fmt.Fprintf(&transcript, "== view %d ==\n%s\n", n, m.View().Content)
		}
	}
	m = send(shellFixture(2), "g", "t", "g", "t", "g", "T")
	if m.view != 1 {
		t.Fatal("tab cycling")
	}
	// Motion cancels the pending digit timer, even if it is delivered late.
	m = send(shellFixture(20), "5")
	due := viewDue(m.inputSerial)
	m = send(m, "j")
	next, _ = m.Update(due)
	m = next.(Model)
	if m.view != 0 || m.selected != 5 {
		t.Fatal("late timer changed view")
	}
	m = send(m, "?")
	fmt.Fprintf(&transcript, "== help ==\n%s\n", m.View().Content)
	m = send(m, "esc")
	if m.launcher.open {
		t.Fatal("help did not close")
	}
	for _, q := range []string{"q", "ctrl+c"} {
		k := tea.KeyPressMsg{Code: 'q'}
		if q == "ctrl+c" {
			k.Code = 'c'
			k.Mod = tea.ModCtrl
		}
		_, cmd := m.Update(k)
		if cmd == nil {
			t.Fatal("missing quit")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("not quit")
		}
	}
	golden(t, "navigation", transcript.String())
}

// Catches layout arithmetic indexing past a body of a few rows, which kills
// the program when the terminal shrinks while a view or overlay is open.
func TestShellTinyTerminal(t *testing.T) {
	t.Parallel()
	empty := shellFixture(3)
	empty.groups, empty.data = nil, prepareRows(nil, nil)
	detail := detailFixture(t, 3, nil)
	detail, _ = detailKey(detail, tea.KeyEnter, 0)
	states := map[string]Model{
		"loading": New(Options{NoColor: true}), "empty": empty, "list": shellFixture(3),
		"detail": settleDetail(t, detail), "board": send(shellFixture(3), "B"),
		"tree": send(shellFixture(3), "T"), "help": send(shellFixture(3), "?"),
		"palette": send(shellFixture(3), ":"), "picker": send(shellFixture(3), "down", "s"),
		"capture": send(shellFixture(3), "n"),
	}
	for name, m := range states {
		for height := 1; height <= 6; height++ {
			for _, width := range []int{1, 12, 40} {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("%s %dx%d: %v", name, width, height, r)
						}
					}()
					next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
					next.(Model).View()
				}()
			}
		}
	}
}

// Catches terminal escape injection, wide-character padding, lost failure
// markers, and secondary columns surviving the narrow breakpoint.
func TestShellRendering(t *testing.T) {
	t.Parallel()
	// Secondary columns give way to the title as the terminal narrows.
	for _, tc := range []struct {
		width   int
		updated bool
	}{{50, false}, {80, false}, {160, true}} {
		width := tc.width
		m := shellFixture(3)
		m.groups[0].Rows[0].Title = "safe\x1b[2J\x1b]52;c;bad\a\n修正 🚀"
		// Raw provider statuses label group headers and fill the status column.
		m.groups[0].Value = "To Do\x1b]52;c;bad\a"
		for i := range m.groups[0].Rows {
			m.groups[0].Rows[i].Status = m.groups[0].Value
		}
		m.data = prepareRows(m.groups, nil)
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = next.(Model)
		content := m.View().Content
		if strings.Contains(content, "\x1b") || !strings.Contains(content, "safe") {
			t.Fatal("unsafe output")
		}
		for _, line := range strings.Split(content, "\n") {
			if ansi.StringWidth(line) != width {
				t.Fatalf("%d: row width %d", width, ansi.StringWidth(line))
			}
		}
		if strings.Contains(content, "Updated") != tc.updated {
			t.Fatal("secondary columns")
		}
		golden(t, fmt.Sprintf("width-%d", width), content)
	}
	// Detail's meta line prints the raw priority beside its glyph.
	m := shellFixture(3)
	m.groups[0].Rows[0].Priority = "high\x1b]52;c;bad\a"
	m.data = prepareRows(m.groups, nil)
	m.selected, m.detailOpen = 1, true
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if content := next.(Model).View().Content; strings.Contains(content, "\x1b") || !strings.Contains(content, "high") {
		t.Fatal("unsafe detail meta")
	}
	for _, opts := range []Options{{NoColor: true}, {HighContrast: true}} {
		m := New(opts)
		m.groups = shellFixture(3).groups
		m.data = prepareRows(m.groups, nil)
		content := m.View().Content
		if opts.NoColor && strings.Contains(content, "\x1b") {
			t.Fatal("NO_COLOR styles")
		}
		if strings.Contains(content, "\x1b[2m") || !strings.Contains(content, "▲") || !strings.Contains(content, "●") || !strings.Contains(content, "!") {
			t.Fatal("inaccessible glyphs/styles")
		}
	}
}

// Catches a slow fold repainting a view that the user already left.
func TestShellStalePreparation(t *testing.T) {
	t.Parallel()
	m := shellFixture(3)
	cmd := m.prepare()
	m = send(m, "g", "t")
	next, _ := m.Update(cmd())
	m = next.(Model)
	if !m.loading || m.view != 1 {
		t.Fatal("stale fold replaced new view")
	}
}

// Catches a watcher or R refresh unfolding groups or moving the cursor onto a
// different item when rows above it change.
func TestShellLiveProjectionKeepsFoldsAndSelection(t *testing.T) {
	t.Parallel()
	m := send(shellFixture(3), "]", "z", "a")
	next, _ := m.Update(m.prepare()())
	m = send(next.(Model), "]", "j", "j")
	want := m.data.rows[m.selected].row.Ref
	groups := slices.Clone(m.groups)
	groups[0].Rows = groups[0].Rows[1:]
	next, _ = m.Update(projectionMsg(engine.Projection{Request: m.request, Groups: groups}))
	next, _ = next.(Model).Update(next.(Model).prepare()())
	m = next.(Model)
	if len(m.data.rows) != 8 {
		t.Fatalf("rows %d: fold lost on refresh", len(m.data.rows))
	}
	if got := m.data.rows[m.selected].row.Ref; got != want {
		t.Fatalf("selection moved to %v, want %v", got, want)
	}
	// A status edit moves the item into another group. The cursor must follow
	// it, not stay on the row taking its index, where the next edit would land.
	groups = slices.Clone(m.groups)
	for g := range groups {
		groups[g].Rows = slices.DeleteFunc(slices.Clone(groups[g].Rows), func(r engine.Row) bool { return r.Ref == want })
	}
	groups[0].Rows = append(groups[0].Rows, engine.Row{Ref: want, Title: "moved", Status: groups[0].Value})
	next, _ = m.Update(projectionMsg(engine.Projection{Request: m.request, Groups: groups}))
	next, _ = next.(Model).Update(next.(Model).prepare()())
	m = next.(Model)
	if got := m.data.rows[m.selected]; got.row.Ref != want || got.value != groups[0].Value {
		t.Fatalf("selection at %v in %q after %v changed groups", got.row.Ref, got.value, want)
	}
}

func BenchmarkShellNavigation50k(b *testing.B) {
	m := shellFixture(16667)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next, _ := m.Update(tea.KeyPressMsg{Code: 'j'})
		m = next.(Model)
		_ = m.View()
	}
}
func TestShellMeasurement(t *testing.T) {
	if os.Getenv("BL_TUI_MEASURE") != "1" {
		return
	}
	m := shellFixture(16667)
	samples := make([]time.Duration, 2000)
	for i := range samples {
		start := time.Now()
		next, _ := m.Update(tea.KeyPressMsg{Code: 'j'})
		m = next.(Model)
		_ = m.View()
		samples[i] = time.Since(start)
	}
	slices.Sort(samples)
	t.Logf("production shell 50k Update+View n=%d p95=%s", len(samples), samples[(len(samples)*95-1)/100])
}
