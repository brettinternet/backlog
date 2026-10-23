package engine_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

// Real native files catch project/source collisions, status-policy leakage,
// accidental execution of untrusted sources, and writes routed to a sibling.
func TestWorkspaceIsolationAndReadiness(t *testing.T) {
	testkit.Isolate(t)
	base := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	roots := []string{filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "c")}
	statuses := []string{"queued", "finished", "ready"}
	sources := []string{"tasks", "upstream", "tasks"}
	categories := []string{"todo", "done", "todo"}
	for n, root := range roots {
		write(filepath.Join(root, ".git", "config"), "[core]\n bare = false\n")
		config := fmt.Sprintf("version: 1\nproject: {id: project-%d, name: Project%d}\nsources: [{id: %s, type: native, path: items, prefix: BL}]\nstatuses: [{key: %s, name: Custom, category: %s}, {key: complete, name: Complete, category: done}]\n", n, n, sources[n], statuses[n], categories[n])
		if n == 1 {
			config = strings.Replace(config, "prefix: BL}]", "prefix: BL}, {id: inert, type: external, run: [must-not-run]}]", 1)
		}
		write(filepath.Join(root, ".config", "backlog.yaml"), config)
		edge := ""
		if n == 0 {
			edge = "blocked_by: [upstream:BL-1]\n"
		}
		write(filepath.Join(root, "items", "BL-1.md"), fmt.Sprintf("---\nid: BL-1\ntitle: Project%d item\nstatus: %s\ncreated: 2026-10-22T00:00:00Z\nupdated: 2026-10-22T00:00:00Z\n%s---\n", n, statuses[n], edge))
	}
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	write(paths.ConfigFile, fmt.Sprintf("workspaces:\n  - name: all\n    projects: [%q, %q, %q]\ndefaultWorkspace: all\n", roots[0], roots[1], roots[2]))
	cfg, err := project.Resolve(paths, roots[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	local, err := engine.LoadReads(context.Background(), cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if local.Ranking.Items[0].Readiness != engine.Unknown {
		t.Fatalf("project scope: %+v", local.Ranking)
	}
	cfg, err = project.OpenWorkspace(cfg, "all", nil)
	if err != nil {
		t.Fatal(err)
	}
	reads, err := engine.LoadReads(context.Background(), cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(reads.Query.Items) != 3 || len(reads.Warnings) == 0 {
		t.Fatalf("workspace: %+v", reads)
	}
	refs := map[string]model.Ref{}
	for _, i := range reads.Query.Items {
		refs[i.Project] = i.Ref
		if i.Status != statuses[int(i.Project[len(i.Project)-1]-'0')] {
			t.Fatalf("raw status changed: %+v", i)
		}
	}
	for _, e := range reads.Ranking.Items {
		if e.Ref == refs["project-0"] && e.Readiness != engine.Ready {
			t.Fatalf("cross-project blocker: %+v", e)
		}
	}
	// A qualified source/ID collision must not silently pick either project.
	write(filepath.Join(roots[1], "items", "BL-2.md"), "---\nid: BL-2\ntitle: Ambiguous\nstatus: finished\nblocked_by: [tasks:BL-1]\ncreated: 2026-10-22T00:00:00Z\nupdated: 2026-10-22T00:00:00Z\n---\n")
	reads, err = engine.LoadReads(context.Background(), cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range reads.Query.Items {
		if i.Ref.ID == "BL-2" && i.BlockedBy[0].Source != "tasks" {
			t.Fatalf("ambiguous edge guessed: %+v", i.BlockedBy)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := engine.OpenProjectStore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := awaitSnapshot(t, store, func(s *engine.Snapshot) bool { return s.Len() == 4 })
	item, _, ok := snapshot.Get(refs["project-2"])
	if !ok {
		t.Fatal("missing third project")
	}
	requireSuccess(t, store.Mutate(model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "status", Value: "complete"}))
	updated, err := engine.ReadItem(ctx, cfg, item.Ref)
	if err != nil || updated.Status != "complete" || updated.Category != model.Done {
		t.Fatalf("routed status write: %+v %v", updated, err)
	}
	original, err := engine.ReadItem(ctx, cfg, refs["project-0"])
	if err != nil || original.Status != "queued" {
		t.Fatalf("sibling changed: %+v %v", original, err)
	}
	outside, err := project.Resolve(paths, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	outside, err = project.OpenWorkspace(outside, "", nil)
	if err != nil || outside.Workspace == nil || outside.Workspace.Name != "all" {
		t.Fatalf("default workspace: %+v %v", outside.Workspace, err)
	}
}
