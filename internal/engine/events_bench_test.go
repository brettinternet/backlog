package engine_test

import (
	"context"
	"path/filepath"
	"testing"

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
	b.ResetTimer()
	for range b.N {
		if _, err := engine.LoadReads(context.Background(), cfg, false); err != nil {
			b.Fatal(err)
		}
	}
}
