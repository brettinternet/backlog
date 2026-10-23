package cli_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/cli"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
	"github.com/brettinternet/backlog/internal/tui"
)

// The CLI and TUI must dispatch the same tokenized template through creation,
// not merely agree on a parser return value.
func TestCLIAndTUIQuickAddParity(t *testing.T) {
	root := writeFixture(t)
	line := "Parity !high @agent #fresh :feature ^99 <12 <404 <other:7 ~m-2"
	// Existing numeric 12 must retain its imported padding; missing 99 and
	// 404 use the configured prefix, and the qualified ref stays cross-source.
	put(t, filepath.Join(root, "backlog.yaml"), "version: 1\nsources: [{id: tasks, type: native, path: items, prefix: BL}, {id: other, type: native, path: other, prefix: EXT}]\nstatuses: [{key: todo, name: To Do, category: todo}, {key: doing, name: Doing, category: active}, {key: done, name: Done, category: done}]\nworkflow: {start: doing, done: done}\ndefaults: {type: task, dod: [Run checks]}\n")
	writeOK(t, root, "", "new", "--source", "other", "Other fixture")
	// Missing blockers are stored as written, with a warning each.
	if code, out, stderr := writeRun(root, "", append([]string{"new", "--source", "tasks"}, strings.Fields(line)...)...); code != 0 || strings.Count(stderr, "bl: warning: blocker ") != 2 {
		t.Fatalf("quick-add: %d %s %s", code, out, stderr)
	}
	dir := filepath.Join(root, "items")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	backend, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: dir, CommonDir: filepath.Join(root, ".git"), Statuses: map[string]model.StatusCategory{"todo": model.Todo, "doing": model.Active, "done": model.Done}})
	if err != nil {
		t.Fatal(err)
	}
	statuses := []project.StatusConfig{{Key: "todo", Category: "todo"}, {Key: "doing", Category: "active"}, {Key: "done", Category: "done"}}
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
	defer store.Close()
	m := tui.New(tui.Options{Context: ctx, Store: store, CreateSource: "tasks", Statuses: statuses, ResolveRef: func(ctx context.Context, source string, ref model.Ref) (model.Ref, error) {
		return cli.ResolveRef(ctx, cfg, source, ref)
	}, DefaultType: "task", DefaultDoD: []string{"Run checks"}, ValidateItem: func(item model.Item) error { return engine.ValidateItem(cfg, item) }})
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'n'})
	m = updated.(tui.Model)
	for _, r := range line {
		updated, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(tui.Model)
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("TUI quick-add returned no creation command: %s", updated.(tui.Model).View().Content)
	}
	cmd()
	var cliItem, tuiItem model.Item
	for _, id := range []string{"BL-13", "BL-14"} {
		data := []byte(get(t, filepath.Join(dir, id+".md")))
		doc, err := format.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		if id == "BL-13" {
			cliItem = doc.Item()
		} else {
			tuiItem = doc.Item()
		}
	}
	fields := []string{"title", "status", "type", "priority", "assignees", "labels", "parent", "blocked_by", "milestone", "dod"}
	// Compare field projections, excluding the allocated ID and timestamps.
	for _, field := range fields {
		left, right := fieldValue(cliItem, field), fieldValue(tuiItem, field)
		if left != right {
			t.Fatalf("%s: CLI %s vs TUI %s", field, left, right)
		}
	}
}
func fieldValue(item model.Item, field string) string {
	var v any
	switch field {
	case "title":
		v = item.Title
	case "status":
		v = item.Status
	case "type":
		v = item.Type
	case "priority":
		v = item.Priority
	case "assignees":
		v = item.Assignees
	case "labels":
		v = item.Labels
	case "parent":
		v = item.Parent
	case "blocked_by":
		v = item.BlockedBy
	case "milestone":
		v = item.Milestone
	case "dod":
		v = item.DoD
	}
	b, _ := json.Marshal(v)
	return string(b)
}
