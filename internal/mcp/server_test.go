package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

func TestFoundationToolsAndWriteGuards(t *testing.T) {
	home := testkit.Isolate(t)
	root := filepath.Join(home, "repo")
	common := filepath.Join(root, ".git")
	for _, dir := range []string{common, filepath.Join(root, "items"), filepath.Join(home, "cache")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := project.Config{Settings: project.Settings{Project: project.ProjectConfig{ID: "test"}, Sources: []project.SourceConfig{{ID: "tasks", Type: "native", Path: filepath.Join(root, "items"), Prefix: "BL"}}, Statuses: []project.StatusConfig{{Key: "draft", Category: "backlog"}, {Key: "todo", Category: "todo"}, {Key: "done", Category: "done"}}, Ready: project.ReadyConfig{Statuses: []string{"todo"}, SatisfiedBy: []string{"done"}, SkipContainers: true}}, Discovery: project.Discovery{ControlRoot: root, CommonDir: common}, Paths: project.Paths{CacheDir: filepath.Join(home, "cache")}}
	server := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := server.server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		return result
	}
	assertErr := func(result *mcp.CallToolResult, want string) {
		t.Helper()
		if !result.IsError || len(result.Content) == 0 {
			t.Fatalf("expected %s failure: %#v", want, result)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		var response map[string]string
		if err := json.Unmarshal([]byte(text), &response); err != nil || response["reason"] != want {
			t.Fatalf("wanted reason %s; got %s", want, text)
		}
	}
	created := call("create", map[string]any{"title": "Draft", "status": "draft"})
	if created.IsError {
		t.Fatalf("create draft: %#v", created)
	}
	var draft ItemResult
	encoded, _ := json.Marshal(created.StructuredContent)
	if err := json.Unmarshal(encoded, &draft); err != nil {
		t.Fatal(err)
	}
	if draft.Item.Status != "draft" {
		t.Fatalf("draft status: %#v", draft.Item)
	}
	if got := call("next", map[string]any{}); got.IsError {
		t.Fatalf("next: %#v", got)
	} else {
		var result ItemsResult
		b, _ := json.Marshal(got.StructuredContent)
		_ = json.Unmarshal(b, &result)
		if len(result.Items) != 0 {
			t.Fatalf("draft was ready: %#v", result.Items)
		}
	}
	originalSources := server.cfg.Sources
	server.cfg.Sources = append([]project.SourceConfig(nil), originalSources...)
	server.cfg.Sources[0].Type = "unknown"
	assertErr(call("next", map[string]any{}), "rejected")
	server.cfg.Sources = originalSources
	assertErr(call("create", map[string]any{"title": "Bad", "status": "unknown"}), "rejected")
	entries, err := os.ReadDir(filepath.Join(root, "items"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("unknown status wrote item: %v", entries)
	}
	assertErr(call("show", map[string]any{"id": "missing"}), "not-found")
	assertErr(call("query", map[string]any{"query": "badfield:value"}), "usage")
	created = call("create", map[string]any{"title": "Ready"})
	if created.IsError {
		t.Fatalf("create: %#v", created)
	}
	var ready ItemResult
	encoded, _ = json.Marshal(created.StructuredContent)
	if err := json.Unmarshal(encoded, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Item.Version == "" {
		t.Fatal("create omitted version")
	}
	if got := call("next", map[string]any{}); got.IsError {
		t.Fatalf("next: %#v", got)
	}
	q := call("query", map[string]any{"query": "status:todo"})
	if q.IsError {
		t.Fatalf("query: %#v", q)
	}
	if got := call("show", map[string]any{"id": ready.Item.Ref.ID}); got.IsError {
		t.Fatalf("show: %#v", got)
	}
	numericID := strings.TrimPrefix(ready.Item.Ref.ID, "BL-")
	child := call("create", map[string]any{"title": "Child", "parent": numericID, "blocked_by": []string{numericID}, "milestone": "m1", "due": "2030-01-02", "priority": "high", "labels": []string{"agent"}})
	if child.IsError {
		t.Fatalf("create quick-add fields: %#v", child.Content)
	}
	var childResult ItemResult
	encoded, _ = json.Marshal(child.StructuredContent)
	if err := json.Unmarshal(encoded, &childResult); err != nil {
		t.Fatal(err)
	}
	if childResult.Item.Parent == nil || childResult.Item.Parent.ID != ready.Item.Ref.ID || len(childResult.Item.BlockedBy) != 1 || childResult.Item.BlockedBy[0].ID != ready.Item.Ref.ID || childResult.Item.Milestone != "m1" || childResult.Item.Due != "2030-01-02" || childResult.Item.Priority != "high" || len(childResult.Item.Labels) != 1 || childResult.Item.Labels[0] != "agent" {
		t.Fatalf("create quick-add fields: %#v", childResult.Item)
	}
	path := filepath.Join(root, "items", ready.Item.Ref.ID+".md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertErr(call("update", map[string]any{"id": ready.Item.Ref.ID, "version": "stale", "fields": map[string]any{"title": "No write"}}), "conflict")
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("stale update changed item bytes")
	}
	updated := call("update", map[string]any{"id": ready.Item.Ref.ID, "version": ready.Item.Version, "fields": map[string]any{"title": "Updated", "labels+": []string{"mcp"}}})
	if updated.IsError {
		t.Fatalf("update: %#v", updated.Content[0].(*mcp.TextContent).Text)
	}
	updated, _ = session.CallTool(ctx, &mcp.CallToolParams{Name: "update", Arguments: map[string]any{"id": ready.Item.Ref.ID, "fields": map[string]any{"labels-": []string{"mcp"}}}})
	if updated.IsError {
		t.Fatalf("list remove update: %#v", updated)
	}
	var afterList ItemResult
	b, _ := json.Marshal(updated.StructuredContent)
	if err := json.Unmarshal(b, &afterList); err != nil {
		t.Fatal(err)
	}
	for _, label := range afterList.Item.Labels {
		if label == "mcp" {
			t.Fatal("list -= did not remove label")
		}
	}
	// Section replacement must target the requested section and honor the same
	// version guard as field updates, rather than bypassing concurrency checks.
	replaced := call("update", map[string]any{"id": ready.Item.Ref.ID, "version": afterList.Item.Version, "operation": "replace_section", "section": "summary", "text": "Delivered"})
	if replaced.IsError {
		t.Fatalf("replace section: %#v", replaced)
	}
	var afterSection ItemResult
	b, _ = json.Marshal(replaced.StructuredContent)
	if err := json.Unmarshal(b, &afterSection); err != nil || afterSection.Item.Summary != "Delivered" {
		t.Fatalf("section replacement: %#v, %v", afterSection, err)
	}
	before, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertErr(call("update", map[string]any{"id": ready.Item.Ref.ID, "version": afterList.Item.Version, "operation": "replace_section", "section": "summary", "text": "Stale"}), "conflict")
	after, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("stale section replacement changed item bytes")
	}
	appended := call("append", map[string]any{"id": ready.Item.Ref.ID, "kind": "note", "text": "note"})
	if appended.IsError {
		t.Fatalf("append: %#v", appended)
	}
	assertErr(call("append", map[string]any{"id": ready.Item.Ref.ID, "kind": "other", "text": "x"}), "rejected")
	criterion := call("criteria", map[string]any{"id": ready.Item.Ref.ID, "operation": "add", "text": "Acceptance"})
	if criterion.IsError {
		t.Fatalf("criteria add: %#v", criterion)
	}
	evidence := call("criteria", map[string]any{"id": ready.Item.Ref.ID, "operation": "check", "index": 1, "evidence": "reason"})
	if evidence.IsError {
		t.Fatalf("single-line evidence containing r/n: %#v", evidence)
	}
	path = filepath.Join(root, "items", ready.Item.Ref.ID+".md")
	before, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertErr(call("criteria", map[string]any{"id": ready.Item.Ref.ID, "operation": "check", "index": 1, "evidence": "line one\nline two"}), "usage")
	after, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("multiline evidence changed item bytes")
	}
	assertErr(call("criteria", map[string]any{"id": ready.Item.Ref.ID, "operation": "wat"}), "rejected")
	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	foundCreate := false
	for _, tool := range tools.Tools {
		if tool.Name == "create" {
			foundCreate = true
			schema, _ := json.Marshal(tool.InputSchema)
			for _, option := range []string{"status", "description", "criteria", "source", "version"} {
				if !contains(string(schema), option) {
					t.Fatalf("create schema lacks CLI new option %q: %s", option, schema)
				}
			}
		}
	}
	if !foundCreate {
		t.Fatal("create tool missing")
	}
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: InstructionsURI})
	if err != nil {
		t.Fatal(err)
	}
	if len(resource.Contents) != 1 || resource.Contents[0].Text == "" {
		t.Fatalf("instructions resource missing: %#v", resource)
	}
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
