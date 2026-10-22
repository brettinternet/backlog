//go:build unix

package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/fixtures"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
	"github.com/brettinternet/backlog/internal/tui/ptytest"
)

// This opt-in harness wraps the production model only to expose its cursor and
// completed projection in the PTY. Loading, watching, projection, input and
// rendering use the same implementations as the CLI, not the BL-14 spike List.
type measuredModel struct {
	Model
	done  bool
	ready chan struct{}
}

func (m measuredModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(soakComplete); ok {
		m.done = true
		return m, nil
	}
	next, cmd := m.Model.Update(msg)
	m.Model = next.(Model)
	if m.ready != nil && !m.loading && m.data.items > 0 && m.applied == m.request && m.snapshot.Source("tasks").Coverage == "complete" {
		close(m.ready)
		m.ready = nil
	}
	return m, cmd
}
func (m measuredModel) View() tea.View {
	v := m.Model.View()
	state := "loading"
	if !m.loading && m.snapshot != nil && m.snapshot.Len() > 0 && m.applied == m.request && m.prepared == m.serial && m.snapshot.Source("tasks").Coverage == "complete" {
		state = "ready"
	}
	// Replace the header rather than append a row, preserving viewport size.
	lines := strings.Split(v.Content, "\n")
	query := m.query
	if m.editor.open {
		query = m.editor.text
	}
	content := !m.loading && m.data.items > 0 && m.applied == m.request && m.prepared == m.serial
	lines[0] = fmt.Sprintf("measure %s query=%q cursor=%06d applied=%d soak=%t content=%t", state, query, m.selected, m.prepared, m.done, content)
	v.Content = strings.Join(lines, "\n")
	return v
}

func TestProductionPerformanceChild(t *testing.T) {
	root := os.Getenv("BL_TUI_PERF_ROOT")
	if root == "" {
		return
	}
	// Match interactive CLI startup: standard Go heap growth, with any
	// explicit GOMEMLIMIT inherited by the child runtime unchanged.
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := engine.OpenProjectStore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projector := engine.NewProjector(ctx, cfg)
	defer projector.Close()
	snapshots, unsub := store.Subscribe()
	defer unsub()
	m := New(Options{Context: ctx, Store: store, Projector: projector, Sources: []string{"tasks"}, Project: cfg.Project.ID, Statuses: cfg.Statuses, Read: func(ctx context.Context, ref model.Ref) (model.Item, error) { return engine.ReadItem(ctx, cfg, ref) }})
	m.Bind(snapshots)
	ready := make(chan struct{})
	program := tea.NewProgram(measuredModel{Model: m, ready: ready}, tea.WithFPS(120))
	if durationText := os.Getenv("BL_TUI_PERF_SOAK"); durationText != "" {
		duration, err := time.ParseDuration(durationText)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			<-ready
			// Warmup excludes initial native load and projection from idle counters.
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			<-timer.C
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			g, cpu, start := runtime.NumGoroutine(), cpuSeconds(), time.Now()
			timer.Reset(duration)
			<-timer.C
			runtime.GC()
			runtime.ReadMemStats(&after)
			var usage syscall.Rusage
			_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
			rss := float64(usage.Maxrss) / (1024 * 1024)
			if runtime.GOOS == "linux" {
				rss *= 1024
			}
			result := fmt.Sprintf("duration=%s cpu_seconds=%.6f goroutines=%d->%d heap_bytes=%d->%d peak_RSS_MiB=%.2f", time.Since(start), cpuSeconds()-cpu, g, runtime.NumGoroutine(), before.HeapAlloc, after.HeapAlloc, rss)
			if err := os.WriteFile(os.Getenv("BL_TUI_PERF_RESULT"), []byte(result), 0600); err != nil {
				program.Quit()
				return
			}
			program.Send(soakComplete{})
		}()
	}
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
}

func performanceChild(t *testing.T, root, cache, soak, result string) (*ptytest.Session, time.Time) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProductionPerformanceChild$", "-test.timeout=75m")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "BL_TUI_PERF_ROOT="+root, "XDG_CACHE_HOME="+cache, "BL_TUI_PERF_SOAK="+soak, "BL_TUI_PERF_RESULT="+result)
	start := time.Now()
	s, err := ptytest.Start(cmd, 120, 40)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, start
}
func perfReady(t *testing.T, s *ptytest.Session) {
	t.Helper()
	// This is a harness liveness deadline, not a passing latency tolerance.
	// Cold FTS population can take many minutes; report its actual time rather
	// than aborting before the navigation and RSS budgets can be measured.
	if err := s.WaitFor("measure ready", 45*time.Minute); err != nil {
		t.Fatal(err)
	}
}
func perfQuery(t *testing.T, s *ptytest.Session, query string) {
	t.Helper()
	if err := s.WaitFor(fmt.Sprintf("measure ready query=%q", query), 30*time.Second); err != nil {
		t.Fatal(err)
	}
}
func perfFirst(t *testing.T, s *ptytest.Session) {
	t.Helper()
	if err := s.Write("gg"); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor("cursor=000000", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestProductionSoak(t *testing.T) {
	duration := os.Getenv("BL_TUI_PERF_SOAK")
	if duration == "" {
		return
	}
	testkit.Isolate(t)
	root := filepath.Join(t.TempDir(), "project")
	if err := fixtures.Generate(root, 10000, 1); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(t.TempDir(), "result")
	s, _ := performanceChild(t, root, filepath.Join(t.TempDir(), "cache"), duration, result)
	perfReady(t, s)
	d, err := time.ParseDuration(duration)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor("soak=true", d+time.Minute); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	perfFirst(t, s)
	latency, err := s.KeyToOutput("j", "cursor=000001", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("production post-soak key-to-PTY=%s", latency)
}

func TestProductionMeasurements(t *testing.T) {
	if os.Getenv("BL_TUI_MEASURE") != "1" {
		return
	}
	testkit.Isolate(t)
	for _, count := range []int{10000, 50000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "project")
			if err := fixtures.Generate(root, count, 1); err != nil {
				t.Fatal(err)
			}
			cache := filepath.Join(t.TempDir(), "warm-cache")
			// Populate the persistent cache with a separate child, excluding warmup.
			var peakRSS int64
			stop := func(child *ptytest.Session) {
				_ = child.Close()
				peakRSS = max(peakRSS, child.ProcessState().SysUsage().(*syscall.Rusage).Maxrss)
			}
			s, coldStart := performanceChild(t, root, cache, "", "")
			perfReady(t, s)
			t.Logf("%d initial cold load and cache population=%s", count, time.Since(coldStart))
			stop(s)
			// Startup budgets apply at 10k; do not populate twenty redundant 50k
			// cold caches just to report a size without a startup latency threshold.
			if count == 10000 {
				t.Run("startup", func(t *testing.T) {
					for _, condition := range []string{"cold", "warm"} {
						paints, contents, loads := make([]time.Duration, 20), make([]time.Duration, 20), make([]time.Duration, 20)
						for i := range paints {
							sampleCache := cache
							if condition == "cold" {
								sampleCache = filepath.Join(t.TempDir(), "cold-cache")
							}
							child, start := performanceChild(t, root, sampleCache, "", "")
							if err := child.WaitFor("measure ", 5*time.Minute); err != nil {
								t.Fatal(err)
							}
							paints[i] = time.Since(start)
							if err := child.WaitFor("content=true", 45*time.Minute); err != nil {
								t.Fatal(err)
							}
							contents[i] = time.Since(start)
							perfReady(t, child)
							loads[i] = time.Since(start)
							stop(child)
						}
						t.Logf("%d %s n=20 shell p95=%s first-content p95=%s complete-list p95=%s", count, condition, percentile(paints), percentile(contents), percentile(loads))
					}
				})
			}
			t.Run("interaction", func(t *testing.T) {
				s, _ = performanceChild(t, root, cache, "", "")
				perfReady(t, s)
				perfFirst(t, s)
				nav := make([]time.Duration, 100)
				for i := range nav {
					var err error
					nav[i], err = s.KeyToOutput("j", fmt.Sprintf("cursor=%06d", i+1), 5*time.Second)
					if err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("%d navigation key-to-PTY n=100 p95=%s", count, percentile(nav))
				if count == 10000 {
					// Next has fewer than 300 ready rows in this fixture. Hold navigation
					// over the complete backlog, not against the end of that short view.
					if err := s.Write("/\x15is:open,closed sort:id\r"); err != nil {
						t.Fatal(err)
					}
					perfQuery(t, s, "is:open,closed sort:id")
					perfFirst(t, s)
					ticker := time.NewTicker(25 * time.Millisecond)
					start := time.Now()
					for range 200 {
						<-ticker.C
						if err := s.Write("j"); err != nil {
							t.Fatal(err)
						}
					}
					ticker.Stop()
					release := time.Now()
					if err := s.WaitFor("cursor=000200", 5*time.Second); err != nil {
						t.Fatal(err)
					}
					t.Logf("held j 10k 40Hz keys=200 duration=%s release-to-final=%s", release.Sub(start), time.Since(release))
				}
				stop(s)
				// All view avoids readiness excluding the changed fixture item. Filtering
				// uses unique serials and waits for the completed projection, not text echo.
				s, _ = performanceChild(t, root, cache, "", "")
				perfReady(t, s)
				if err := s.Write("4"); err != nil {
					t.Fatal(err)
				}
				if err := s.WaitFor(`query="is:open group:status"`, 5*time.Second); err != nil {
					t.Fatal(err)
				}
				timings := make([]time.Duration, 20)
				for i := range timings {
					prefix := fmt.Sprintf("id:BL-%d", 10+i)
					if err := s.Write("/\x15" + prefix); err != nil {
						t.Fatal(err)
					}
					perfQuery(t, s, prefix)
					s.Reset()
					start := time.Now()
					if err := s.Write("0"); err != nil {
						t.Fatal(err)
					}
					perfQuery(t, s, prefix+"0")
					timings[i] = time.Since(start)
					if err := s.Write("\r"); err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("%d filter single-keystroke-to-PTY n=20 p95=%s", count, percentile(timings))
				if count == 10000 {
					if err := s.Write("/\x15id:BL-1 is:open,closed\r"); err != nil {
						t.Fatal(err)
					}
					perfQuery(t, s, "id:BL-1 is:open,closed")
					filename := filepath.Join(root, "backlog", "BL-1.md")
					data, err := os.ReadFile(filename)
					if err != nil {
						t.Fatal(err)
					}
					doc, err := format.Parse(data)
					if err != nil {
						t.Fatal(err)
					}
					item := doc.Item()
					changes := make([]time.Duration, 20)
					for i := range changes {
						item.Title = fmt.Sprintf("VisibleChange%03d", i)
						data, err = format.Serialize(item)
						if err != nil {
							t.Fatal(err)
						}
						replacement := filepath.Join(root, "backlog", ".measure.tmp")
						if err := os.WriteFile(replacement, data, 0600); err != nil {
							t.Fatal(err)
						}
						s.Reset()
						start := time.Now()
						if err := os.Rename(replacement, filename); err != nil {
							t.Fatal(err)
						}
						if err := s.WaitFor(item.Title, 30*time.Second); err != nil {
							t.Fatal(err)
						}
						changes[i] = time.Since(start)
					}
					t.Logf("10k external atomic-rename-to-visible n=20 p95=%s", percentile(changes))
				}
				stop(s)
				rss := float64(peakRSS) / (1024 * 1024)
				if runtime.GOOS == "linux" {
					rss *= 1024
				}
				t.Logf("%d production child peak_RSS_MiB=%.2f", count, rss)
			})
		})
	}
}
