package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cli"
	serverpkg "github.com/brettinternet/backlog/internal/mcp"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWriteCommandOptionParity(t *testing.T) {
	testkit.Isolate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := serverpkg.New(project.Config{})
	st, ct := mcp.NewInMemoryTransports()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "parity-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer func() { cancel(); <-serverDone }()
	listed, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]map[string]any{}
	for _, tool := range listed.Tools {
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(encoded, &schema); err != nil {
			t.Fatal(err)
		}
		props, _ := schema["properties"].(map[string]any)
		schemas[tool.Name] = props
	}
	// Each CLI flag has a named schema destination. These are deliberate aliases
	// for the corresponding CLI operation, not excluded commands.
	mapping := map[string]map[string]string{
		"new":   {"if-version": "create.version", "body": "create.description", "ac": "create.criteria", "source": "create.source", "status": "create.status", "sprint": "create.sprint"},
		"set":   {"if-version": "update.version", "yes": "update.confirm"},
		"start": {"if-version": "update.version"}, "done": {"if-version": "update.version"},
		"archive": {"if-version": "update.version"}, "unarchive": {"if-version": "update.version"},
		"note": {"if-version": "append.version"}, "comment": {"if-version": "append.version", "author": "append.author"},
		"ac":   {"if-version": "criteria.version", "evidence": "criteria.evidence", "dod": "criteria.list"},
		"edit": {"if-version": "update.version", "section": "update.section", "text": "update.text"},
	}
	for command, flags := range cli.WriteOptionNames() {
		destinations, ok := mapping[command]
		if !ok {
			t.Errorf("write command %q has no MCP mapping", command)
			continue
		}
		for _, flag := range flags {
			destination, ok := destinations[flag]
			if !ok {
				t.Errorf("write option %s --%s has no MCP equivalent", command, flag)
				continue
			}
			tool, property, _ := splitDestination(destination)
			if schemas[tool] == nil || schemas[tool][property] == nil {
				t.Errorf("write option %s --%s maps to missing MCP property %s", command, flag, destination)
			}
		}
	}
	for command, fields := range map[string][]string{
		"new": {"type", "priority", "assignees", "labels", "milestone", "due", "parent", "blocked_by"},
		"set": {"fields"}, "start": {"fields"}, "done": {"fields"},
		"archive": {"operation"}, "unarchive": {"operation"},
		"note": {"kind", "text"}, "comment": {"kind", "text"},
		"ac": {"operation", "index", "text"}, "edit": {"operation", "text"},
	} {
		for _, property := range fields {
			if schemas[map[string]string{"new": "create", "set": "update", "start": "update", "done": "update", "archive": "update", "unarchive": "update", "note": "append", "comment": "append", "ac": "criteria", "edit": "update"}[command]][property] == nil {
				t.Errorf("write command %s has no MCP equivalent property %s", command, property)
			}
		}
	}
}

func splitDestination(value string) (string, string, string) {
	for i, r := range value {
		if r == '.' {
			return value[:i], value[i+1:], ""
		}
	}
	return value, "", ""
}

// Catches MCP events diverging from bl events --json, and paging that skips or
// repeats events across cursors, including filtered events past the limit.
func TestEventsMatchCLI(t *testing.T) {
	testkit.Isolate(t)
	root := t.TempDir()
	for _, dir := range []string{".git", "items"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		".git/config":  "[core]\n bare = false\n",
		"backlog.yaml": "version: 1\nsources: [{id: tasks, type: native, path: items, prefix: BL}]\nstatuses: [{key: todo, name: To Do, category: todo}, {key: doing, name: Doing, category: active}, {key: done, name: Done, category: done}]\nworkflow: {start: doing, done: done}\n",
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (int, string) {
		var out, stderr bytes.Buffer
		code := cli.Run(context.Background(), append([]string{"bl", "-C", root, "--json"}, args...), cli.BuildInfo{}, strings.NewReader(""), &out, &stderr)
		return code, out.String()
	}
	for _, args := range [][]string{{"new", "One"}, {"new", "Two"}, {"new", "Three"}, {"start", "1"}, {"done", "1"}, {"start", "3"}} {
		if code, out := run(args...); code != 0 {
			t.Fatalf("%v: %d %s", args, code, out)
		}
	}
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	serverDone := make(chan error, 1)
	go func() { serverDone <- serverpkg.New(cfg).Run(ctx, st) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "events-test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { session.Close(); cancel(); <-serverDone }()
	call := func(args map[string]any) (serverpkg.EventsResult, map[string]string) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "events", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		var page serverpkg.EventsResult
		var failure map[string]string
		if result.IsError {
			_ = json.Unmarshal([]byte(text), &failure)
		} else if err := json.Unmarshal([]byte(text), &page); err != nil {
			t.Fatalf("events shape: %s", text)
		}
		return page, failure
	}
	cliEvents := func(args ...string) json.RawMessage {
		t.Helper()
		code, out := run(append([]string{"events"}, args...)...)
		var response struct {
			Data struct {
				Events json.RawMessage `json:"events"`
			} `json:"data"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &response) != nil {
			t.Fatalf("bl events: %d %s", code, out)
		}
		return response.Data.Events
	}
	for _, q := range []string{"", "changed:status"} {
		want := cliEvents(q)
		var got []json.RawMessage
		pages, since := 0, ""
		for {
			page, failure := call(map[string]any{"since": since, "query": q, "limit": 2})
			if failure != nil {
				t.Fatalf("events %q: %v", q, failure)
			}
			for _, e := range page.Events {
				b, _ := json.Marshal(e)
				got = append(got, b)
			}
			pages++
			since = page.Cursor
			if !page.More {
				break
			}
		}
		joined, _ := json.Marshal(got)
		var a, b any
		_ = json.Unmarshal(joined, &a)
		_ = json.Unmarshal(want, &b)
		if len(got) < 3 || pages < 2 || !reflect.DeepEqual(a, b) {
			t.Fatalf("query %q: %d pages\nMCP %s\nCLI %s", q, pages, joined, want)
		}
		if page, _ := call(map[string]any{"since": since, "query": q}); len(page.Events) != 0 || page.More || page.Cursor != since {
			t.Fatalf("query %q: exhausted cursor %s returned %+v", q, since, page)
		}
	}
	unknown := "01ZZZZZZZZZZZZZZZZZZZZZZZZ"
	if _, failure := call(map[string]any{"since": unknown}); failure["reason"] != "not-found" {
		t.Fatalf("MCP unknown since: %v", failure)
	}
	if code, out := run("events", "--since", unknown); code != 3 || !strings.Contains(out, `"reason":"not-found"`) {
		t.Fatalf("CLI unknown since: %d %s", code, out)
	}
}
