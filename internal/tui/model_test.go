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
	m.badges = "tasks ✓ synced"
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
	if !strings.Contains(view, m.opts.TrustWarning) || strings.Contains(view, "tasks ✓ synced") {
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
		{"half", []string{"ctrl+d", "ctrl+d", "ctrl+u"}, 9},
		{"full", []string{"ctrl+f", "ctrl+f", "ctrl+b"}, 19},
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
	if m.showHelp {
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

// Catches terminal escape injection, wide-character padding, lost failure
// markers, and secondary columns surviving the narrow breakpoint.
func TestShellRendering(t *testing.T) {
	t.Parallel()
	for _, width := range []int{79, 80, 160} {
		m := shellFixture(3)
		m.groups[0].Rows[0].Title = "safe\x1b[2J\x1b]52;c;bad\a\n修正 🚀"
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
		if strings.Contains(content, "Status") != (width >= 80) {
			t.Fatal("secondary columns")
		}
		golden(t, fmt.Sprintf("width-%d", width), content)
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
