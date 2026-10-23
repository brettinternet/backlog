// Package fixtures generates reproducible native projects for performance measurements.
package fixtures

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
)

// Generate writes a new project, refusing an existing destination to avoid
// overwriting real data or leaving stale items from an earlier generation.
func Generate(root string, count int, seed uint64) error {
	if count < 1 {
		return fmt.Errorf("count must be positive")
	}
	if err := os.Mkdir(root, 0755); err != nil {
		return err
	}
	for _, dir := range []string{".git", "backlog"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0755); err != nil {
			return err
		}
	}
	config := "version: 1\nroot: checkout\nsources: [{id: tasks, type: native, path: backlog, prefix: BL}]\nstatuses:\n"
	for _, status := range []string{"backlog", "todo", "active", "done", "canceled"} {
		config += fmt.Sprintf("  - {key: %s, name: %s, category: %s}\n", status, status, status)
	}
	if err := os.WriteFile(filepath.Join(root, "backlog.yaml"), []byte(config), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n bare = false\n"), 0644); err != nil {
		return err
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	clock := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	titles := []string{"Improve task selection", "修正依存関係", "Ship 🚀 dashboard", "Cafe\u0301 rendering"}
	statuses := []string{"backlog", "todo", "todo", "todo", "active", "done", "canceled"}
	priorities := []model.Priority{"", model.Low, model.Medium, model.High, model.Urgent}
	ref := func(n int) model.Ref { return model.Ref{Source: "tasks", ID: fmt.Sprintf("BL-%d", n)} }
	for n := 1; n <= count; n++ {
		item := model.Item{Ref: ref(n), Title: fmt.Sprintf("%s %d", titles[rng.IntN(len(titles))], n), Type: "task", Status: statuses[rng.IntN(len(statuses))], Priority: priorities[rng.IntN(len(priorities))], Created: clock.Add(time.Duration(n) * time.Second), Updated: clock.Add(time.Duration(n) * time.Second)}
		if n%3 != 0 {
			item.Labels = []string{[]string{"backend", "ui", "perf", "bug"}[rng.IntN(4)]}
		}
		if n%4 != 0 {
			item.Assignees = []string{[]string{"alice", "bob", "chen"}[rng.IntN(3)]}
		}
		// Bounded chains and broad fan-out, without cycles or unresolved references.
		if n%16 > 1 {
			item.BlockedBy = []model.Ref{ref(n - 1)}
		}
		if n > 16 && n%7 == 0 {
			item.BlockedBy = append(item.BlockedBy, ref(1))
		}
		if n%5 != 1 && n > 1 {
			parent := ref(n - 1)
			item.Parent = &parent
		}
		size := 0
		switch roll := rng.IntN(100); {
		case roll < 30:
			size = 0
		case roll < 70:
			size = 256
		case roll < 90:
			size = 2048
		case roll < 99:
			size = 8192
		default:
			size = 100 * 1024
		}
		if size > 0 {
			item.Description = strings.Repeat("fixture text ", (size+12)/13)[:size]
		}
		data, err := format.Serialize(item)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(root, "backlog", item.Ref.ID+".md"), data, 0644); err != nil {
			return err
		}
	}
	return nil
}
