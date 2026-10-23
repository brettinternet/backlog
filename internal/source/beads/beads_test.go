package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/testkit"
)

// A subprocess fake catches accidental per-row calls, read/write flag mixups,
// stale preconditions and lossy dependency/version decoding at the public seam.
func TestBDHelper(t *testing.T) {
	if os.Getenv("BL_BEADS_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--sandbox") || !strings.Contains(joined, "--json") {
		os.Exit(20)
	}
	command := ""
	for _, a := range args {
		if a == "version" || a == "config" || a == "list" || a == "update" || a == "comment" {
			command = a
			break
		}
	}
	write := command == "update" || command == "comment"
	if strings.Contains(joined, "--readonly") == write {
		os.Exit(21)
	}
	if write && !strings.Contains(joined, "--dolt-auto-commit=on") {
		os.Exit(22)
	}
	log, err := os.OpenFile("calls", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(23)
	}
	fmt.Fprintln(log, command)
	log.Close()
	switch command {
	case "version":
		fmt.Print(`{"version":"1.3.0"}`)
	case "config":
		fmt.Print(`{"value":""}`)
	case "list":
		data, _ := os.ReadFile("rows.json")
		fmt.Print(string(data))
	case "update", "comment":
		data, _ := os.ReadFile("rows.json")
		var rows []map[string]any
		json.Unmarshal(data, &rows)
		fields := 0
		for _, arg := range args {
			if strings.HasPrefix(arg, "--title=") {
				rows[0]["title"] = strings.TrimPrefix(arg, "--title=")
			}
			if strings.HasPrefix(arg, "--set-labels=") {
				rows[0]["labels"] = strings.Split(strings.TrimPrefix(arg, "--set-labels="), ",")
			}
			if strings.HasPrefix(arg, "--title=") || strings.HasPrefix(arg, "--status=") {
				fields++
			}
		}
		// bd 1.3.0 refuses guards unless a field other than labels changes.
		if command == "update" && strings.Contains(joined, "--if-status=") && fields == 0 {
			os.Exit(26)
		}
		if command == "comment" {
			rows[0]["comment_count"] = 1
		}
		data, _ = json.Marshal(rows)
		if os.WriteFile("rows.json", data, 0600) != nil {
			os.Exit(24)
		}
		fmt.Print(`{}`)
	default:
		os.Exit(25)
	}
	os.Exit(0)
}
func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func fake(t *testing.T) (*Source, string) {
	t.Helper()
	testkit.Isolate(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, ".beads", "metadata.json"), `{"dolt_mode":"embedded"}`)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "bd")
	put(t, script, fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestBDHelper -- \"$@\"\n", binary))
	if err = os.Chmod(script, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BL_BEADS_HELPER", "1")
	s, err := New(Options{ID: "issues", Path: root, Binary: script})
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}
func TestBulkMappingAndInvalidCoverage(t *testing.T) {
	s, root := fake(t)
	ctx := context.Background()
	for _, count := range []int{1, 1000} {
		rows := make([]map[string]any, count)
		for i := range rows {
			rows[i] = map[string]any{"id": fmt.Sprintf("bd-%d", i), "title": "Issue", "status": "open", "priority": 2, "dependency_count": 3, "dependencies": []map[string]string{{"issue_id": fmt.Sprintf("bd-%d", i), "depends_on_id": "blocker", "type": "blocks"}, {"issue_id": fmt.Sprintf("bd-%d", i), "depends_on_id": "parent", "type": "parent-child"}, {"issue_id": fmt.Sprintf("bd-%d", i), "depends_on_id": "discovery", "type": "discovered-from"}}}
		}
		raw, _ := json.Marshal(rows)
		put(t, filepath.Join(root, "rows.json"), string(raw))
		put(t, filepath.Join(root, "calls"), "")
		delta, err := s.Load(ctx, source.Cursor{})
		if err != nil {
			t.Fatal(err)
		}
		calls, _ := os.ReadFile(filepath.Join(root, "calls"))
		if string(calls) != "list\n" {
			t.Fatalf("%d items: calls %s", count, calls)
		}
		first := delta.Items[0]
		if len(delta.Items) != count || delta.Coverage != source.Complete || len(first.BlockedBy) != 1 || first.BlockedBy[0].ID != "blocker" || first.Parent == nil || first.Parent.ID != "parent" || len(first.Related) != 1 || first.Priority != model.Medium {
			t.Fatalf("mapping: %+v", delta)
		}
	}
	for _, raw := range []string{`null`, `{}`, `[`, `[{"id":"bd-1","title":"Issue","status":"open","dependency_count":1}]`, `[{"id":"bd-1","title":"Issue","status":"open","dependencies":[{"issue_id":"other","depends_on_id":"x","type":"blocks"}]}]`, `[{"id":"bd-1","title":"Issue","status":"open"},{"id":"bd-1","title":"Duplicate","status":"open"}]`} {
		put(t, filepath.Join(root, "rows.json"), raw)
		if delta, err := s.Load(ctx, source.Cursor{}); err == nil || delta.Coverage == source.Complete {
			t.Fatalf("accepted incomplete JSON %s", raw)
		}
	}
}
func TestVersionCheckedWrites(t *testing.T) {
	s, root := fake(t)
	ctx := context.Background()
	path := filepath.Join(root, "rows.json")
	original := `[{"id":"bd-1","title":"Issue","status":"open","priority":2}]`
	put(t, path, original)
	before, err := s.Read(ctx, "bd-1")
	if err != nil {
		t.Fatal(err)
	}
	mutation := model.Mutation{Ref: before.Ref, ExpectedVersion: before.Version, Op: model.Set, Field: "title", Value: "--status=closed"}
	// A dependency-only change must invalidate a displayed version too.
	put(t, path, `[{"id":"bd-1","title":"Issue","status":"open","priority":2,"dependencies":[{"issue_id":"bd-1","depends_on_id":"bd-2","type":"blocks"}]}]`)
	put(t, filepath.Join(root, "calls"), "")
	_, err = s.Apply(ctx, mutation)
	var refusal *source.Error
	if !errors.As(err, &refusal) || refusal.Reason != "conflict" {
		t.Fatalf("stale write: %v", err)
	}
	calls, _ := os.ReadFile(filepath.Join(root, "calls"))
	if strings.Contains(string(calls), "update") {
		t.Fatal("stale write reached provider")
	}
	put(t, path, original)
	after, err := s.Apply(ctx, mutation)
	if err != nil || after.Title != "--status=closed" || after.Version == before.Version {
		t.Fatalf("update: %+v %v", after, err)
	}
	labeled, err := s.Apply(ctx, model.Mutation{Ref: after.Ref, ExpectedVersion: after.Version, Op: model.Set, Field: "labels", Value: []string{"auth"}})
	if err != nil || len(labeled.Labels) != 1 || labeled.Labels[0] != "auth" {
		t.Fatalf("label-only update: %+v %v", labeled, err)
	}
	after = labeled
	comment := model.Mutation{Ref: after.Ref, ExpectedVersion: after.Version, Op: model.AppendComment, Comment: model.Comment{Body: "a comment"}}
	result, err := s.Apply(ctx, comment)
	if err != nil || result.Version == after.Version {
		t.Fatalf("comment: %+v %v", result, err)
	}
}

// This catches mismatches between the pinned real CLI and its fake: flags,
// bulk JSON schema, version stability, and durable update/comment behavior.
func TestProviderRoundTrip(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 for isolated bd 1.3.0")
	}
	binary := os.Getenv("BL_TEST_BD_BINARY")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("bd")
		if err != nil {
			t.Fatal(err)
		}
	}
	testkit.Isolate(t)
	root := t.TempDir()
	ctx := context.Background()
	run := func(program string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, program, args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", program, args, err, out)
		}
		return out
	}
	run("git", "init", "-q")
	run("git", "config", "user.email", "test@example.invalid")
	run("git", "config", "user.name", "Test")
	run(binary, "init", "--non-interactive", "--skip-hooks", "--skip-agents", "--prefix", "probe")
	run(binary, "--json", "--sandbox", "create", "Test issue", "--id", "probe-1")
	run(binary, "--json", "--sandbox", "create", "Prerequisite", "--id", "probe-2")
	run(binary, "--json", "--sandbox", "dep", "add", "probe-1", "probe-2")
	s, err := New(Options{ID: "issues", Path: root, Binary: binary})
	if err != nil {
		t.Fatal(err)
	}
	delta, err := s.Load(ctx, source.Cursor{})
	if err != nil || len(delta.Items) != 2 {
		t.Fatalf("load: %+v %v", delta, err)
	}
	before, err := s.Read(ctx, "probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.BlockedBy) != 1 || before.BlockedBy[0].ID != "probe-2" {
		t.Fatalf("bulk dependency mapping: %+v", before)
	}
	updated, err := s.Apply(ctx, model.Mutation{Ref: before.Ref, ExpectedVersion: before.Version, Op: model.Batch, Changes: []model.Mutation{{Op: model.Set, Field: "title", Value: "Updated issue"}, {Op: model.Set, Field: "status", Value: "in_progress"}, {Op: model.Set, Field: "labels", Value: []string{"one", "two"}}, {Op: model.Set, Field: "assignees", Value: []string{"test"}}, {Op: model.Set, Field: "priority", Value: "high"}}})
	if err != nil || updated.Title != "Updated issue" || updated.Status != "in_progress" {
		t.Fatalf("update: %+v %v", updated, err)
	}
	labeled, err := s.Apply(ctx, model.Mutation{Ref: updated.Ref, ExpectedVersion: updated.Version, Op: model.Set, Field: "labels", Value: []string{"three"}})
	if err != nil || len(labeled.Labels) != 1 || labeled.Labels[0] != "three" {
		t.Fatalf("label-only update: %+v %v", labeled, err)
	}
	updated = labeled
	commented, err := s.Apply(ctx, model.Mutation{Ref: updated.Ref, ExpectedVersion: updated.Version, Op: model.AppendComment, Comment: model.Comment{Author: "Round trip author", Body: "Round trip comment"}})
	if err != nil || commented.Version == updated.Version {
		t.Fatalf("comment: %+v %v", commented, err)
	}
	out := run(binary, "--json", "--readonly", "--sandbox", "comments", "probe-1")
	if !strings.Contains(string(out), "Round trip comment") || !strings.Contains(string(out), "Round trip author") {
		t.Fatalf("missing comment: %s", out)
	}
}

func FuzzBulkJSON(f *testing.F) {
	f.Add([]byte(`{"id":"bd-1","title":"Issue","status":"open"}`))
	f.Add([]byte(`{"dependencies":[{"type":"blocks"}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var row issue
		if json.Unmarshal(raw, &row) != nil {
			return
		}
		s := &Source{options: Options{ID: "issues"}}
		_, _ = s.item(row, raw)
	})
}
