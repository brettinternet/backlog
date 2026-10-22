package engine_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/project"
)

// Measures the production event-aware load; numbers are reported, not gates.
func BenchmarkEventRead(b *testing.B) {
	_, cfg, root := engineFixture(b, 10000)
	cfg.Discovery.ControlRoot = root
	cfg.Discovery.CommonDir = filepath.Join(root, ".git")
	cfg.Project.ID = root
	cfg.Paths.CacheDir = filepath.Join(root, "cache")
	cfg.Paths.StateDir = filepath.Join(root, "state")
	cfg.Sources = []project.SourceConfig{{ID: "tasks", Type: "native", Path: "backlog", Prefix: "BL"}}
	if _, err := engine.LoadReads(context.Background(), cfg, false); err != nil {
		b.Fatal(err)
	}
	for _, events := range []bool{false, true} {
		name := "native-only"
		if events {
			name = "events"
		}
		b.Run(name, func(b *testing.B) {
			options := cfg
			if !events {
				options.Paths.StateDir = ""
			}
			samples := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for range b.N {
				start := time.Now()
				if _, err := engine.LoadReads(context.Background(), options, false); err != nil {
					b.Fatal(err)
				}
				samples = append(samples, time.Since(start))
			}
			b.StopTimer()
			reportP95(b, samples)
		})
	}
}
