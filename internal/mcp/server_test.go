package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/itemjson"
	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

func TestOAuthDiagnosticsUseStableMCPFailureShape(t *testing.T) {
	codes := []string{
		oauth.CodeAuthenticationRequired, oauth.CodeProviderUnavailable, oauth.CodeTokenExpired,
		oauth.CodeGrantRevoked, oauth.CodeInsufficientScope, oauth.CodeSSORequired,
		oauth.CodeRateLimited, oauth.CodeProviderDown, oauth.CodePrincipalMismatch,
		oauth.CodeNotOptedIn, oauth.CodeStorageUnavailable, oauth.CodeInteractionRequired,
	}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			diagnostic := oauth.Failure(code, time.Time{})
			result, _, err := failed[any](ErrorReason(diagnostic), engine.ErrorMessage(diagnostic))
			if err != nil || result == nil || !result.IsError || len(result.Content) != 1 {
				t.Fatalf("MCP diagnostic result: %#v %v", result, err)
			}
			var response map[string]string
			text := result.Content[0].(*mcp.TextContent).Text
			if json.Unmarshal([]byte(text), &response) != nil || response["reason"] != code || response["message"] == "" {
				t.Fatalf("MCP diagnostic shape: %s", text)
			}
		})
	}
}

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
	// show must use the bl show --json names, which agents pass back verbatim.
	show := func(id string) (itemjson.Detail, map[string]any) {
		t.Helper()
		got := call("show", map[string]any{"id": id})
		if got.IsError {
			t.Fatalf("show: %#v", got)
		}
		b, _ := json.Marshal(got.StructuredContent)
		var detail itemjson.Detail
		var raw map[string]any
		if json.Unmarshal(b, &detail) != nil || json.Unmarshal(b, &raw) != nil {
			t.Fatalf("show shape: %s", b)
		}
		return detail, raw
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
	numericID := strings.TrimPrefix(ready.Item.ID, "BL-")
	child := call("create", map[string]any{"title": "Child", "parent": numericID, "blocked_by": []string{numericID}, "milestone": "m1", "due": "2030-01-02", "priority": "high", "labels": []string{"agent"}})
	if child.IsError {
		t.Fatalf("create quick-add fields: %#v", child.Content)
	}
	var childResult ItemResult
	encoded, _ = json.Marshal(child.StructuredContent)
	if err := json.Unmarshal(encoded, &childResult); err != nil {
		t.Fatal(err)
	}
	childDetail, raw := show(childResult.Item.ID)
	target := "tasks:" + ready.Item.ID
	meta := childDetail.Metadata
	if meta["parent"] != target || len(meta["blockedBy"].([]any)) != 1 || meta["blockedBy"].([]any)[0] != target || meta["milestone"] != "m1" || meta["due"] != "2030-01-02" || childDetail.Item.Priority != "high" || len(meta["labels"].([]any)) != 1 || meta["labels"].([]any)[0] != "agent" {
		t.Fatalf("create quick-add fields: %#v", childDetail)
	}
	if item, _ := raw["item"].(map[string]any); item["version"] != childResult.Item.Version || item["id"] != childResult.Item.ID {
		t.Fatalf("show item names: %#v", raw)
	}
	// A newline would split one criterion into Markdown the parser reads back differently.
	assertErr(call("create", map[string]any{"title": "Bad criterion", "criteria": []string{"one\n- [ ] two"}}), "usage")
	// MCP resolves the target with bl new's rules, so an unknown source is a usage error.
	assertErr(call("create", map[string]any{"title": "Nowhere", "source": "nope"}), "usage")
	path := filepath.Join(root, "items", ready.Item.ID+".md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertErr(call("update", map[string]any{"id": ready.Item.ID, "version": "stale", "fields": map[string]any{"title": "No write"}}), "conflict")
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("stale update changed item bytes")
	}
	updated := call("update", map[string]any{"id": ready.Item.ID, "version": ready.Item.Version, "fields": map[string]any{"title": "Updated", "labels+": []string{"mcp"}}})
	if updated.IsError {
		t.Fatalf("update: %#v", updated.Content[0].(*mcp.TextContent).Text)
	}
	updated, _ = session.CallTool(ctx, &mcp.CallToolParams{Name: "update", Arguments: map[string]any{"id": ready.Item.ID, "fields": map[string]any{"labels-": []string{"mcp"}}}})
	if updated.IsError {
		t.Fatalf("list remove update: %#v", updated)
	}
	var afterList ItemResult
	b, _ := json.Marshal(updated.StructuredContent)
	if err := json.Unmarshal(b, &afterList); err != nil {
		t.Fatal(err)
	}
	listed, _ := show(ready.Item.ID)
	if labels, _ := listed.Metadata["labels"].([]any); slices.Contains(labels, any("mcp")) {
		t.Fatal("list -= did not remove label")
	}
	// Section replacement must target the requested section and honor the same
	// version guard as field updates, rather than bypassing concurrency checks.
	replaced := call("update", map[string]any{"id": ready.Item.ID, "version": afterList.Item.Version, "operation": "replace_section", "section": "summary", "text": "Delivered"})
	if replaced.IsError {
		t.Fatalf("replace section: %#v", replaced)
	}
	var afterSection ItemResult
	b, _ = json.Marshal(replaced.StructuredContent)
	if err := json.Unmarshal(b, &afterSection); err != nil {
		t.Fatal(err)
	}
	if sectioned, _ := show(ready.Item.ID); sectioned.Sections["summary"] != "Delivered" {
		t.Fatalf("section replacement: %#v", sectioned.Sections)
	}
	before, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertErr(call("update", map[string]any{"id": ready.Item.ID, "version": afterList.Item.Version, "operation": "replace_section", "section": "summary", "text": "Stale"}), "conflict")
	after, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("stale section replacement changed item bytes")
	}
	appended := call("append", map[string]any{"id": ready.Item.ID, "kind": "note", "text": "note"})
	if appended.IsError {
		t.Fatalf("append: %#v", appended)
	}
	assertErr(call("append", map[string]any{"id": ready.Item.ID, "kind": "other", "text": "x"}), "rejected")
	criterion := call("criteria", map[string]any{"id": ready.Item.ID, "operation": "add", "text": "Acceptance"})
	if criterion.IsError {
		t.Fatalf("criteria add: %#v", criterion)
	}
	evidence := call("criteria", map[string]any{"id": ready.Item.ID, "operation": "check", "index": 1, "evidence": "reason"})
	if evidence.IsError {
		t.Fatalf("single-line evidence containing r/n: %#v", evidence)
	}
	path = filepath.Join(root, "items", ready.Item.ID+".md")
	before, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertErr(call("criteria", map[string]any{"id": ready.Item.ID, "operation": "check", "index": 1, "evidence": "line one\nline two"}), "usage")
	after, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("multiline evidence changed item bytes")
	}
	assertErr(call("criteria", map[string]any{"id": ready.Item.ID, "operation": "wat"}), "rejected")
	// Writes without a version must not conflict with each other just because
	// a client sent them without waiting for responses.
	var wg sync.WaitGroup
	results := make([]*mcp.CallToolResult, 8)
	for i := range results {
		req := &mcp.CallToolParams{Name: "update", Arguments: map[string]any{"id": ready.Item.ID, "fields": map[string]any{"title": fmt.Sprint("Concurrent ", i)}}}
		if i%2 == 1 {
			req = &mcp.CallToolParams{Name: "criteria", Arguments: map[string]any{"id": ready.Item.ID, "operation": "uncheck", "index": 1}}
		}
		wg.Go(func() { results[i], _ = session.CallTool(ctx, req) })
	}
	wg.Wait()
	for _, result := range results {
		if result == nil || result.IsError {
			t.Fatalf("concurrent implicit-version write: %#v", result)
		}
	}
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
