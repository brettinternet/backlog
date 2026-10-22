package engine_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/fixtures"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
)

func engineFixture(b *testing.B, n int) (*native.Source, project.Config, string) {
	b.Helper()
	root := filepath.Join(b.TempDir(), "fixture")
	if err := fixtures.Generate(root, n, 42); err != nil {
		b.Fatal(err)
	}
	cfg := storeConfig()
	cfg.Statuses = nil
	statuses := map[string]model.StatusCategory{}
	for _, status := range []model.StatusCategory{model.Backlog, model.Todo, model.Active, model.Done, model.Canceled} {
		statuses[string(status)] = status
		cfg.Statuses = append(cfg.Statuses, project.StatusConfig{Key: string(status), Category: string(status)})
	}
	backend, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: filepath.Join(root, "backlog"), CommonDir: filepath.Join(root, ".git"), Statuses: statuses})
	if err != nil {
		b.Fatal(err)
	}
	return backend, cfg, root
}
func reportP95(b *testing.B, samples []time.Duration) {
	slices.Sort(samples)
	b.ReportMetric(float64(samples[(len(samples)*95-1)/100].Nanoseconds())/1e6, "p95-ms")
}

// BenchmarkEngine uses BL-12's deterministic fixture distribution. Timing is
// reported, never asserted; default tests assert correctness, not host speed.
func BenchmarkEngine(b *testing.B) {
	b.Run("Filter50k", func(b *testing.B) {
		backend, cfg, _ := engineFixture(b, 50_000)
		delta, err := backend.Load(context.Background(), source.Cursor{})
		if err != nil {
			b.Fatal(err)
		}
		s := engine.OpenStore(context.Background(), cfg, source.Registry{"tasks": projectionSource{delta.Items}})
		defer s.Close()
		snapshot := awaitSnapshot(b, s, func(s *engine.Snapshot) bool { return s.Len() == 50_000 })
		p := engine.NewProjector(context.Background(), cfg)
		defer p.Close()
		start := time.Now()
		p.Request(snapshot, "label:ui sort:priority group:status")
		warm := <-p.Results()
		if warm.Error != nil {
			b.Fatal(warm.Error)
		}
		initial := time.Since(start)
		samples := make([]time.Duration, 0, b.N)
		b.ResetTimer()
		for range b.N {
			start := time.Now()
			id := p.Request(snapshot, "label:ui sort:priority group:status")
			result := <-p.Results()
			if result.Request != id || result.Error != nil {
				b.Fatalf("projection: %+v", result)
			}
			samples = append(samples, time.Since(start))
		}
		b.StopTimer()
		b.ReportMetric(float64(initial.Nanoseconds())/1e6, "initial-ms")
		reportP95(b, samples)
	})
	b.Run("External10k", func(b *testing.B) {
		backend, cfg, root := engineFixture(b, 10_000)
		s := engine.OpenStore(context.Background(), cfg, source.Registry{"tasks": backend})
		defer s.Close()
		awaitSnapshot(b, s, func(s *engine.Snapshot) bool { return s.Len() == 10_000 })
		file := filepath.Join(root, "backlog", "BL-1.md")
		newFile := filepath.Join(root, "backlog", "BL-10001.md")
		original, err := os.ReadFile(file)
		if err != nil {
			b.Fatal(err)
		}
		doc, err := format.Parse(original)
		if err != nil {
			b.Fatal(err)
		}
		// Each benchmark iteration measures all four operations end-to-end.
		samples := make([]time.Duration, 0, b.N*4)
		b.ResetTimer()
		for n := range b.N {
			title := fmt.Sprintf("External %d", n)
			next, _, err := doc.Apply(model.Mutation{Op: model.Set, Field: "title", Value: title}, time.Now())
			if err != nil {
				b.Fatal(err)
			}
			start := time.Now()
			temp := filepath.Join(root, "backlog", ".external-edit")
			if err := os.WriteFile(temp, next.Bytes(), 0644); err != nil {
				b.Fatal(err)
			}
			if err := os.Rename(temp, file); err != nil {
				b.Fatal(err)
			}
			awaitSnapshot(b, s, func(s *engine.Snapshot) bool {
				i, _, ok := s.Get(model.Ref{Source: "tasks", ID: "BL-1"})
				return ok && i.Title == title
			})
			samples = append(samples, time.Since(start))
			item := doc.Item()
			item.Ref.ID = "BL-10001"
			item.Title = title
			data, err := format.Serialize(item)
			if err != nil {
				b.Fatal(err)
			}
			start = time.Now()
			if err := os.WriteFile(temp, data, 0644); err != nil {
				b.Fatal(err)
			}
			if err := os.Rename(temp, newFile); err != nil {
				b.Fatal(err)
			}
			awaitSnapshot(b, s, func(s *engine.Snapshot) bool {
				_, _, ok := s.Get(model.Ref{Source: "tasks", ID: "BL-10001"})
				return ok
			})
			samples = append(samples, time.Since(start))
			// Rename a file out of the source and back, without changing its identity.
			outside := filepath.Join(root, "moved.md")
			start = time.Now()
			if err := os.Rename(newFile, outside); err != nil {
				b.Fatal(err)
			}
			awaitSnapshot(b, s, func(s *engine.Snapshot) bool {
				_, _, ok := s.Get(model.Ref{Source: "tasks", ID: "BL-10001"})
				return !ok
			})
			samples = append(samples, time.Since(start))
			if err := os.Rename(outside, newFile); err != nil {
				b.Fatal(err)
			}
			awaitSnapshot(b, s, func(s *engine.Snapshot) bool {
				_, _, ok := s.Get(model.Ref{Source: "tasks", ID: "BL-10001"})
				return ok
			})
			start = time.Now()
			if err := os.Remove(newFile); err != nil {
				b.Fatal(err)
			}
			awaitSnapshot(b, s, func(s *engine.Snapshot) bool {
				_, _, ok := s.Get(model.Ref{Source: "tasks", ID: "BL-10001"})
				return !ok
			})
			samples = append(samples, time.Since(start))
		}
		b.StopTimer()
		for index, name := range []string{"edit", "create", "rename", "delete"} {
			group := make([]time.Duration, 0, b.N)
			for n := index; n < len(samples); n += 4 {
				group = append(group, samples[n])
			}
			slices.Sort(group)
			b.ReportMetric(float64(group[(len(group)*95-1)/100].Nanoseconds())/1e6, name+"-p95-ms")
		}
		reportP95(b, samples)
	})
}
