package mcp_test

import (
	"context"
	"encoding/json"
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
		"new":   {"if-version": "create.version", "body": "create.description", "ac": "create.criteria", "source": "create.source", "status": "create.status"},
		"set":   {"if-version": "update.version"},
		"start": {"if-version": "update.version"}, "done": {"if-version": "update.version"},
		"archive": {"if-version": "update.version"}, "unarchive": {"if-version": "update.version"},
		"note": {"if-version": "append.version"}, "comment": {"if-version": "append.version", "author": "append.author"},
		"ac":   {"if-version": "criteria.version", "evidence": "criteria.evidence"},
		"edit": {"if-version": "update.version", "section": "update.section"},
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
