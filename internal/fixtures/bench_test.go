package fixtures_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/fixtures"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
)

// These benchmarks are opt-in; the ordinary test suite never generates large
// trees or executes measurements. task bench builds the actual production CLI.
func BenchmarkCLI(b *testing.B) {
	binary := os.Getenv("BL_BENCH_BINARY")
	if binary == "" {
		b.Skip("use task bench to build the CLI")
	}
	for _, count := range []int{10000, 50000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			base := b.TempDir()
			root := filepath.Join(base, "project")
			if err := fixtures.Generate(root, count, 1); err != nil {
				b.Fatal(err)
			}
			env := []string{}
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key != "HOME" && key != "USERPROFILE" && !strings.HasPrefix(key, "XDG_") && !strings.HasPrefix(key, "BL_") && !strings.HasPrefix(key, "WORKLEASE_") {
					env = append(env, entry)
				}
			}
			env = append(env, "HOME="+base, "USERPROFILE="+base, "XDG_CONFIG_HOME="+filepath.Join(base, "config"), "XDG_CACHE_HOME="+filepath.Join(base, "cache"), "XDG_STATE_HOME="+filepath.Join(base, "state"))
			run := func(args ...string) float64 {
				b.Helper()
				cmd := exec.Command(binary, append([]string{"-C", root}, args...)...)
				cmd.Env = env
				cmd.Stdout = io.Discard
				var stderr strings.Builder
				cmd.Stderr = &stderr
				if err := cmd.Run(); err != nil {
					b.Fatalf("%v: %v %s", args, err, stderr.String())
				}
				usage := cmd.ProcessState.SysUsage().(*syscall.Rusage)
				rss := float64(usage.Maxrss) / (1024 * 1024)
				if runtime.GOOS == "linux" {
					rss *= 1024
				}
				return rss
			}
			for _, op := range []string{"cold-load", "warm-load", "ls", "next", "show", "write"} {
				b.Run(op, func(b *testing.B) {
					opts := native.Options{ID: "tasks", Path: filepath.Join(root, "backlog"), Prefix: "BL", CommonDir: filepath.Join(root, ".git")}
					if op == "warm-load" {
						opts.CacheDir = filepath.Join(base, "load-cache")
						opts.ProjectID = root
					}
					backend, err := native.New(opts)
					if err != nil {
						b.Fatal(err)
					}
					measure := func(n int) float64 {
						switch op {
						case "cold-load", "warm-load":
							delta, err := backend.Load(context.Background(), source.Cursor{})
							if err != nil || delta.Coverage != source.Complete || len(delta.Items) != count {
								b.Fatalf("load: %d items, %v", len(delta.Items), err)
							}
						case "ls":
							return run("ls")
						case "next":
							return run("next")
						case "show":
							return run("show", "BL-1")
						case "write":
							return run("set", "BL-1", fmt.Sprintf("title=Measured title %d", n))
						}
						return 0
					}
					// Warm metadata/page caches and prepare the SQLite cache before sampling.
					measure(-1)
					samples := make([]time.Duration, 0, b.N)
					peakRSS := 0.0
					b.ResetTimer()
					for n := 0; n < b.N; n++ {
						start := time.Now()
						rss := measure(n)
						samples = append(samples, time.Since(start))
						peakRSS = max(peakRSS, rss)
					}
					b.StopTimer()
					slices.Sort(samples)
					b.ReportMetric(float64(samples[(len(samples)*95+99)/100-1].Nanoseconds())/1e6, "p95-ms")
					if peakRSS > 0 {
						b.ReportMetric(peakRSS, "peak-RSS-MiB")
					}
				})
			}
		})
	}
}
