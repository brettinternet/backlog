package doctest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/project"
	"go.yaml.in/yaml/v3"
)

// Catch a cutover that loses data, restores over new progress, or lets two
// sessions select the same item. Run the published procedure, not a second one.
func TestMigrationRehearsal(t *testing.T) {
	repo := docCLI(t)
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigFile), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, paths.ConfigFile, "me: {default: '@example'}\n")
	root := t.TempDir()
	t.Setenv("BL_REHEARSAL_DIR", root)
	t.Setenv("BL_FAKE_ROOT", filepath.Join(root, "native"))
	t.Setenv("BL_FAKE_STATE", filepath.Join(root, "authority.json"))
	t.Setenv("BL_FAKE_ITEMS", "backlog")
	doc := readFile(t, filepath.Join(repo, "docs/import.md"))
	blocks := shellBlocks(t, doc)
	if len(blocks) != 4 {
		t.Fatalf("expected four rehearsal stages, got %d", len(blocks))
	}
	run(t, repo, "sh", "-eu", "-c", blocks[0])

	var before struct {
		Data struct {
			Metadata struct {
				Fields map[string]any
			}
			Sections map[string]string
			Criteria []struct {
				Text    string
				Checked bool
			}
		}
	}
	decodeFile(t, filepath.Join(root, "before-loop.json"), &before)
	legacy := filepath.Join(root, "legacy/backlog")
	// Compare the source frontmatter to the public native representation,
	// including fields that cannot be recovered just by counting imported files.
	source := readFile(t, filepath.Join(legacy, "tasks/task-1.md"))
	var original map[string]any
	if err := yaml.Unmarshal([]byte(strings.SplitN(source, "---", 3)[1]), &original); err != nil {
		t.Fatal(err)
	}
	for from, to := range map[string]string{
		"title": "title", "priority": "priority", "assignee": "assignees",
		"labels": "labels", "references": "refs", "modified_files": "files", "custom": "custom",
	} {
		want, _ := json.Marshal(original[from])
		got, _ := json.Marshal(before.Data.Metadata.Fields[to])
		if string(want) != string(got) {
			t.Errorf("%s -> %s: %s != %s", from, to, want, got)
		}
	}
	for key, want := range map[string]string{
		"id": "TASK-1", "status": "todo", "milestone": "m-1",
		"created": "2026-01-02T03:04:00Z", "updated": "2026-01-03T04:05:00Z",
	} {
		if before.Data.Metadata.Fields[key] != want {
			t.Errorf("%s: %v != %s", key, before.Data.Metadata.Fields[key], want)
		}
	}
	for section, want := range map[string]string{
		"description": "Synthetic content, not a real project.",
		"plan":        "1. Compare before enabling writers.", "notes": "Keep this note.",
		"summary": "Original summary.", "dod": "- [ ] Review evidence",
	} {
		if before.Data.Sections[section] != want {
			t.Errorf("section %s: %q != %q", section, before.Data.Sections[section], want)
		}
	}
	if len(before.Data.Criteria) != 1 || before.Data.Criteria[0].Checked || before.Data.Criteria[0].Text != "Preserve the synthetic content" {
		t.Fatalf("criteria lost: %+v", before.Data.Criteria)
	}
	if comments := before.Data.Sections["comments"]; !strings.Contains(comments, "2026-01-03T04:05:00Z example") || !strings.Contains(comments, "Retained comment.") {
		t.Fatalf("comments lost: %s", comments)
	}
	var relationship struct {
		Data struct {
			Metadata struct {
				Parent    string
				BlockedBy []string
			}
		}
	}
	decodeFile(t, filepath.Join(root, "relationships.json"), &relationship)
	if relationship.Data.Metadata.Parent != "tasks:TASK-4" || !reflect.DeepEqual(relationship.Data.Metadata.BlockedBy, []string{"tasks:TASK-1"}) {
		t.Fatalf("relationships lost: %+v", relationship)
	}
	var cfg project.Settings
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(root, "native/backlog.yaml"))), &cfg); err != nil {
		t.Fatal(err)
	}
	var oldFields map[string]any
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(legacy, "config.yml"))), &oldFields); err != nil {
		t.Fatal(err)
	}
	for key, got := range map[string]any{"labels": cfg.Labels, "definition_of_done": cfg.Defaults.DOD} {
		wantJSON, _ := json.Marshal(oldFields[key])
		gotJSON, _ := json.Marshal(got)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("config %s: %s != %s", key, gotJSON, wantJSON)
		}
	}
	if len(cfg.Milestones) != 2 || cfg.Milestones[0].Key != "m-0" || !cfg.Milestones[0].Archived || cfg.Milestones[0].Name != "Prior release" || cfg.Milestones[0].Description != "Synthetic history." || cfg.Milestones[1].Key != "m-1" || cfg.Milestones[1].Archived || cfg.Milestones[1].Name != "Foundation" || cfg.Milestones[1].Description != "Synthetic first release." {
		t.Fatalf("milestones lost: %+v", cfg.Milestones)
	}
	archived, err := format.Parse([]byte(readFile(t, filepath.Join(root, "native/backlog/archive/TASK-4.md"))))
	if err != nil || archived.Item().Title != "Historical work" || archived.Item().Milestone != "m-0" {
		t.Fatalf("archive lost: %v", err)
	}

	run(t, root, "sh", "-eu", "-c", blocks[1])
	selected := readFile(t, filepath.Join(root, "selected.json"))
	if !strings.Contains(selected, `"id":"TASK-1"`) || !strings.Contains(selected, `"outcome":"acquired"`) {
		t.Fatalf("single loop selected the wrong item: %s", selected)
	}
	if done := readFile(t, filepath.Join(root, "done.json")); !strings.Contains(done, `"outcome":"released"`) {
		t.Fatalf("done did not release: %s", done)
	}
	run(t, root, "sh", "-eu", "-c", blocks[2])
	var state struct {
		Claims map[string]any
		Calls  []struct{ Op, Session string }
	}
	decodeFile(t, filepath.Join(root, "authority.json"), &state)
	if len(state.Claims) != 0 {
		t.Fatalf("rollback with live claims: %+v", state.Claims)
	}
	acquires := map[string]int{}
	for _, call := range state.Calls {
		if call.Op == "acquire" {
			acquires[call.Session]++
		}
	}
	if acquires["rehearsal-single"] != 1 || acquires["rehearsal-a"]+acquires["rehearsal-b"] != 3 {
		t.Fatalf("expected one single-loop claim and contention between distinct loops: %+v", acquires)
	}
	for _, file := range []string{"a.json", "b.json"} {
		var selected struct {
			Data struct {
				Status string
				Claim  struct{ Outcome string }
			}
		}
		decodeFile(t, filepath.Join(root, file), &selected)
		if selected.Data.Status != "written" || selected.Data.Claim.Outcome != "acquired" {
			t.Fatalf("selection did not claim then start: %+v", selected)
		}
	}
	run(t, root, "sh", "-eu", "-c", blocks[3])
	after := readFile(t, filepath.Join(root, "after-loop.json"))
	if !strings.Contains(after, `"status":"done"`) || !strings.Contains(after, `"checked":true`) || !strings.Contains(after, "Synthetic comparison passed") {
		t.Fatalf("native loop did not retain completion and evidence: %s", after)
	}
	if strings.Contains(readFile(t, filepath.Join(root, "restored/backlog/tasks/task-1.md")), "Synthetic native loop verified") {
		t.Fatal("rollback mixed providers")
	}
	if _, err := os.Stat(filepath.Join(root, "restored/backlog.yaml")); !os.IsNotExist(err) {
		t.Fatalf("unmigrated provider was replaced: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func decodeFile(t *testing.T, path string, value any) {
	t.Helper()
	if err := json.Unmarshal([]byte(readFile(t, path)), value); err != nil {
		t.Fatal(err)
	}
}
