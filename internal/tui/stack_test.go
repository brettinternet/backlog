//go:build unix

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brettinternet/backlog/internal/fixtures"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/testkit"
	"github.com/brettinternet/backlog/internal/tui/ptytest"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

func rows(count int) []Row {
	result := make([]Row, count)
	for i := range result {
		result[i] = Row{ID: fmt.Sprintf("BL-%d", i+1), Title: "Improve task selection 修正 🚀", Body: "# Detail\n\nA **selected** item.\n"}
	}
	return result
}

// This subprocess is the same virtualized core measured in-process, with the
// real Bubble Tea input parser and terminal renderer. Not a production command.
func TestStackChild(t *testing.T) {
	if os.Getenv("BL_TUI_CHILD") != "1" {
		return
	}
	count := 50000
	if os.Getenv("BL_TUI_ROWS") == "10000" {
		count = 10000
	}
	m := List{Rows: rows(count), Width: 120, Height: 40, Render: RenderMarkdown}
	if _, err := tea.NewProgram(m, tea.WithFPS(120)).Run(); err != nil {
		t.Fatal(err)
	}
}

func child(t *testing.T, count int) *ptytest.Session {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStackChild$")
	cmd.Env = append(os.Environ(), "BL_TUI_CHILD=1", fmt.Sprintf("BL_TUI_ROWS=%d", count), "TERM=xterm-256color")
	s, err := ptytest.Start(cmd, 120, 40)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.WaitFor("selection=000000", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	return s
}

// A real PTY regression check catches lost navigation/output and child cleanup,
// without making an absolute latency claim on CI hosts.
func TestListPTY(t *testing.T) {
	testkit.Isolate(t)
	s := child(t, 50000)
	if _, err := s.KeyToOutput("j", "selection=000001", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KeyToOutput("jjjj", "selection=000005", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

// Stale async results must never overwrite the body of the new selection.
func TestListDetailLatestWins(t *testing.T) {
	t.Parallel()
	m := List{Rows: rows(50000), Width: 120, Height: 40, Render: RenderMarkdown}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'j'})
	m = updated.(List)
	if cmd == nil {
		t.Fatal("missing settle command")
	}
	updated, cmd = m.Update(tea.KeyPressMsg{Code: 'j'})
	m = updated.(List)
	if cmd != nil {
		t.Fatal("held navigation spawned another timer")
	}
	updated, _ = m.Update(detailReady{generation: 1, text: "stale"})
	m = updated.(List)
	if m.Detail != "" {
		t.Fatal("accepted stale detail")
	}
	updated, cmd = m.Update(detailDue(m.generation))
	m = updated.(List)
	if cmd == nil {
		t.Fatal("missing async render")
	}
	updated, _ = m.Update(cmd())
	m = updated.(List)
	if !strings.Contains(m.Detail, "selected") {
		t.Fatalf("missing current detail: %q", m.Detail)
	}
}

// A provider title must not execute terminal controls or inject table rows.
func TestListProviderControls(t *testing.T) {
	t.Parallel()
	m := List{Rows: []Row{{ID: "BL-1", Title: "safe\x1b[2J\nforged"}}, Width: 80, Height: 24}
	view := m.View().Content
	if strings.Contains(view, "\x1b") || strings.Contains(view, "safe\nforged") || !strings.Contains(view, "safe forged") {
		t.Fatalf("unsafe row: %q", view)
	}
	rendered, err := RenderMarkdown("safe\x1b[2J body", 80)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "\x1b[2J") || !strings.Contains(rendered, "safe body") {
		t.Fatalf("unsafe body: %q", rendered)
	}
}

func percentile(samples []time.Duration) time.Duration {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[(len(samples)*95-1)/100]
}

// goldmarkTerminal is deliberately a small comparator, not a second production
// renderer: headings/emphasis use Lip Gloss, text wraps to terminal cells.
func inlineText(node ast.Node, source []byte) string {
	var b strings.Builder
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch child := child.(type) {
		case *ast.Text:
			b.Write(child.Segment.Value(source))
		case *ast.String:
			b.Write(child.Value)
		default:
			b.WriteString(inlineText(child, source))
		}
	}
	return b.String()
}

func goldmarkTerminal(body string, width int) (string, error) {
	source := []byte(body)
	doc := goldmark.DefaultParser().Parse(text.NewReader(source))
	var b strings.Builder
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			if n.Type() == ast.TypeBlock {
				b.WriteByte('\n')
			}
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			b.WriteString(lipgloss.NewStyle().Bold(true).Render(inlineText(n, source)))
			return ast.WalkSkipChildren, nil
		case *ast.Emphasis:
			b.WriteString(lipgloss.NewStyle().Bold(n.Level == 2).Italic(n.Level == 1).Render(inlineText(n, source)))
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			b.Write(n.Segment.Value(source))
			if n.SoftLineBreak() || n.HardLineBreak() {
				b.WriteByte('\n')
			}
		case *ast.String:
			b.Write(n.Value)
		case *ast.FencedCodeBlock:
			for i := 0; i < n.Lines().Len(); i++ {
				line := n.Lines().At(i)
				b.Write(line.Value(source))
			}
		}
		return ast.WalkContinue, nil
	})
	return lipgloss.NewStyle().Width(width).Render(b.String()), err
}

func TestStackMeasurements(t *testing.T) {
	if os.Getenv("BL_TUI_MEASURE") != "1" {
		return
	}
	testkit.Isolate(t)
	// Reuse BL-12's deterministic file fixture; all loading/preparation happens
	// before the measurement, exactly as a worker prepares the UI snapshot.
	root := filepath.Join(t.TempDir(), "fixture")
	if err := fixtures.Generate(root, 50000, 42); err != nil {
		t.Fatal(err)
	}
	prepared := make([]Row, 50000)
	for i := range prepared {
		id := fmt.Sprintf("BL-%d", i+1)
		data, err := os.ReadFile(filepath.Join(root, "backlog", id+".md"))
		if err != nil {
			t.Fatal(err)
		}
		item, err := format.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		prepared[i] = Row{ID: id, Title: item.Item().Title, Body: item.Item().Description}
	}
	m := List{Rows: prepared, Width: 120, Height: 40, Render: RenderMarkdown}
	samples := make([]time.Duration, 2000)
	for i := range samples {
		start := time.Now()
		next, _ := m.Update(tea.KeyPressMsg{Code: 'j'})
		m = next.(List)
		_ = m.View()
		samples[i] = time.Since(start)
	}
	t.Logf("50k Update+View n=%d p95=%s", len(samples), percentile(samples))
	for _, size := range []int{1024, 10240, 102400} {
		fragment := "# Heading\n\nA **bold** paragraph with a [link](https://example.com).\n\n- one\n- two\n\n```go\nfmt.Println(42)\n```\n\n"
		body := strings.Repeat(fragment, (size+len(fragment)-1)/len(fragment))[:size]
		for _, renderer := range []struct {
			name   string
			render func(string, int) (string, error)
		}{{"glamour", RenderMarkdown}, {"goldmark-lipgloss", goldmarkTerminal}} {
			timings := make([]time.Duration, 20)
			for i := range timings {
				start := time.Now()
				if _, err := renderer.render(body, 80); err != nil {
					t.Fatal(err)
				}
				timings[i] = time.Since(start)
			}
			t.Logf("markdown %s bytes=%d n=20 p95=%s", renderer.name, size, percentile(timings))
		}
	}
	s := child(t, 50000)
	latencies := make([]time.Duration, 100)
	for i := range latencies {
		d, err := s.KeyToOutput("j", fmt.Sprintf("selection=%06d", i+1), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		latencies[i] = d
	}
	t.Logf("PTY 50k n=100 p95=%s", percentile(latencies))
	_ = s.Close()
	// Fresh 10k child, 200 inputs on a 40 Hz ticker, exactly five seconds.
	s = child(t, 10000)
	s.Reset()
	ticker := time.NewTicker(25 * time.Millisecond)
	start := time.Now()
	for range 200 {
		<-ticker.C
		if err := s.Write("j"); err != nil {
			t.Fatal(err)
		}
	}
	ticker.Stop()
	released := time.Now()
	if err := s.WaitFor("selection=000200", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Logf("held j 10k keys=200 duration=%s release-to-observed-final=%s", released.Sub(start), time.Since(released))
	if err := s.Write("q"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}

func cpuSeconds() float64 {
	var r syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
}

type soakComplete struct{}
type soakModel struct {
	List
	complete bool
}

func (m soakModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(soakComplete); ok {
		m.complete = true
		return m, nil
	}
	next, cmd := m.List.Update(msg)
	m.List = next.(List)
	return m, cmd
}
func (m soakModel) View() tea.View {
	v := m.List.View()
	if m.complete {
		v.Content = strings.Replace(v.Content, "backlog · virtualized list", "SOAK complete", 1)
	}
	return v
}

// The soak is in-process so runtime counters belong to the running Tea program,
// not a parent process. Counters go to an out-of-band file: direct terminal
// writes would invalidate Bubble Tea's tracked cursor/screen state.
func TestStackSoakChild(t *testing.T) {
	if os.Getenv("BL_TUI_SOAK_CHILD") != "1" {
		return
	}
	m := List{Rows: rows(50000), Width: 120, Height: 40, Render: RenderMarkdown}
	duration, err := time.ParseDuration(os.Getenv("BL_TUI_SOAK_DURATION"))
	if err != nil {
		t.Fatal(err)
	}
	program := tea.NewProgram(soakModel{List: m}, tea.WithFPS(120))
	go func() {
		warmup := time.NewTimer(time.Second)
		<-warmup.C // fixed measurement warmup; not a synchronization assertion
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		g := runtime.NumGoroutine()
		cpu := cpuSeconds()
		start := time.Now()
		timer := time.NewTimer(duration)
		<-timer.C
		runtime.GC()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		result := fmt.Sprintf("SOAK duration=%s cpu_seconds=%.6f goroutines=%d->%d heap_bytes=%d->%d", time.Since(start), cpuSeconds()-cpu, g, runtime.NumGoroutine(), before.HeapAlloc, after.HeapAlloc)
		if err := os.WriteFile(os.Getenv("BL_TUI_SOAK_RESULT"), []byte(result), 0600); err != nil {
			program.Quit()
			return
		}
		program.Send(soakComplete{})
	}()
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
}
func TestStackSoak(t *testing.T) {
	if os.Getenv("BL_TUI_SOAK") != "1" {
		return
	}
	durationText := os.Getenv("BL_TUI_SOAK_DURATION")
	if durationText == "" {
		durationText = "10m"
	}
	duration, err := time.ParseDuration(durationText)
	if err != nil || duration <= 0 {
		t.Fatal("invalid soak duration")
	}
	runSoak(t, duration)
}

// Regression: diagnostics must not move the terminal cursor behind Tea's back.
func TestSoakOutputRouting(t *testing.T) { runSoak(t, time.Millisecond) }

func runSoak(t *testing.T, duration time.Duration) {
	t.Helper()
	testkit.Isolate(t)
	resultFile := filepath.Join(t.TempDir(), "soak.txt")
	cmd := exec.Command(os.Args[0], "-test.run=^TestStackSoakChild$", "-test.timeout="+(duration+time.Minute).String())
	cmd.Env = append(os.Environ(), "BL_TUI_SOAK_CHILD=1", "TERM=xterm-256color", "BL_TUI_SOAK_RESULT="+resultFile, "BL_TUI_SOAK_DURATION="+duration.String())
	s, err := ptytest.Start(cmd, 120, 40)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.WaitFor("selection=000000", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor("SOAK complete", duration+time.Minute); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(resultFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(output))
	d, err := s.KeyToOutput("j", "selection=000001", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("post-soak key-to-output=%s", d)
}
