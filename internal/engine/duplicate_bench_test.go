package engine_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/project"
)

// Measures the duplicate-title check bl new adds at 50k on a warm cache
// (design.md D33: at most 20 ms); numbers are reported, not gates.
func BenchmarkDuplicateTitle50k(b *testing.B) {
	_, cfg, root := engineFixture(b, 50000)
	cfg.Discovery.ControlRoot = root
	cfg.Discovery.CommonDir = filepath.Join(root, ".git")
	cfg.Project.ID = root
	cfg.Paths.CacheDir = filepath.Join(root, "cache")
	cfg.Paths.StateDir = filepath.Join(root, "state")
	cfg.Sources = []project.SourceConfig{{ID: "tasks", Type: "native", Path: "backlog", Prefix: "BL"}}
	reads, err := engine.LoadReads(context.Background(), cfg, false)
	if err != nil {
		b.Fatal(err)
	}
	existing := ""
	for _, item := range reads.Query.Items {
		if item.Category.Open() {
			existing = item.Title
			break
		}
	}
	for _, tc := range []struct{ name, title string }{{"miss", "No item has this title"}, {"match", existing}} {
		b.Run(tc.name, func(b *testing.B) {
			samples := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for range b.N {
				start := time.Now()
				err := engine.DuplicateTitle(context.Background(), cfg, "tasks", tc.title)
				samples = append(samples, time.Since(start))
				if (err != nil) != (tc.name == "match") {
					b.Fatalf("%s: %v", tc.name, err)
				}
			}
			b.StopTimer()
			reportP95(b, samples)
		})
	}
}
