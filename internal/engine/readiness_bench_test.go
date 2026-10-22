package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

// 50k items, exactly 100k distinct edges. A bounded-depth layered graph models
// shared prerequisites without making fixture setup part of measured time.
// Report each phase as well as the full projection, without latency assertions.
func BenchmarkReadinessRanking50k(b *testing.B) {
	const n = 50000
	items := make([]model.Item, n)
	for i := range items {
		items[i] = model.Item{Ref: model.Ref{Source: "tasks", ID: fmt.Sprintf("BL-%d", i+1)}, Category: model.Todo}
	}
	for i := 25000; i < n; i++ {
		for j := range 4 {
			items[i].BlockedBy = append(items[i].BlockedBy, items[(i-25000+j)%25000].Ref)
		}
	}
	in := Input{Items: items, Config: project.ReadyConfig{Statuses: []string{"todo"}, SatisfiedBy: []string{"done", "canceled"}, SkipContainers: true}}
	var readiness, ranking time.Duration
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		start := time.Now()
		result, deps, reverse := assess(in)
		middle := time.Now()
		result = rank(in, result, deps, reverse)
		end := time.Now()
		readiness += middle.Sub(start)
		ranking += end.Sub(middle)
		if len(result.Items) != n || len(result.Candidates) != 25000 {
			b.Fatal("incomplete projection")
		}
	}
	b.ReportMetric(float64(readiness.Nanoseconds())/float64(b.N), "readiness-ns/op")
	b.ReportMetric(float64(ranking.Nanoseconds())/float64(b.N), "ranking-ns/op")
}
