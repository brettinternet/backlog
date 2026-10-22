package mcp

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brettinternet/backlog/internal/testkit"
)

func TestStdioSubprocessInitializeListCall(t *testing.T) {
	testkit.Isolate(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n bare = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".config"), 0700); err != nil {
		t.Fatal(err)
	}
	config := "version: 1\nproject: {id: test}\nsources: [{id: tasks, type: native, path: items, prefix: BL}]\nstatuses: [{key: todo, name: To Do, category: todo}, {key: done, name: Done, category: done}]\nready: {statuses: [todo], satisfiedBy: [done]}\n"
	if err := os.WriteFile(filepath.Join(root, ".config", "backlog.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "bl")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/bl")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build bl: %v\n%s", err, output)
	}
	command := exec.Command(binary, "-C", root, "mcp")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	client := sdk.NewClient(&sdk.Implementation{Name: "stdio-test", Version: "test"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &sdk.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("connect: %v; stderr: %s", err, stderr.String())
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 10 {
		t.Fatalf("got %d tools", len(listed.Tools))
	}
	called, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "next", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if called.IsError {
		t.Fatalf("next returned error: %#v", called)
	}
}
