package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brettinternet/backlog/internal/instructions"
	backlogmcp "github.com/brettinternet/backlog/internal/mcp"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

// A broken project configuration must not prevent agents from learning the CLI.
func TestInstructionsTopics(t *testing.T) {
	testkit.Isolate(t)
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigFile), 0700); err != nil {
		t.Fatal(err)
	}
	put(t, paths.ConfigFile, "invalid: [")
	code, out, stderr := run("instructions")
	if code != 0 || stderr != "" || !strings.Contains(out, "# Agent overview") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	for _, topic := range instructions.Names() {
		if !strings.Contains(out, topic) {
			t.Fatalf("topic absent: %s", topic)
		}
		markdown, _ := instructions.Read(topic)
		code, body, stderr := run("instructions", topic)
		if code != 0 || body != markdown || stderr != "" {
			t.Fatalf("%s: %d %s %s", topic, code, body, stderr)
		}
		code, body, stderr = run("instructions", topic, "--json")
		var result struct {
			OK   bool
			Data struct {
				Topic    string
				Topics   []string
				Markdown string
			}
		}
		if err := json.Unmarshal([]byte(body), &result); err != nil || code != 0 || stderr != "" || !result.OK || result.Data.Topic != topic || result.Data.Markdown != markdown || len(result.Data.Topics) != 6 {
			t.Fatalf("%s JSON: %d %s %s", topic, code, body, stderr)
		}
	}
	for _, args := range [][]string{{"instructions", "missing"}, {"instructions", "overview", "extra"}, {"instructions", "../workflow"}} {
		code, out, stderr := run(args...)
		if code != 2 || out != "" {
			t.Fatalf("%v: %d %s %s", args, code, out, stderr)
		}
		for _, topic := range instructions.Names() {
			if !strings.Contains(stderr, topic) {
				t.Fatalf("missing topic in error: %s", stderr)
			}
		}
	}
	code, out, stderr = run("instructions", "missing", "--json")
	var failure struct {
		Error struct {
			Reason  string
			Details struct{ Topics []string }
		}
	}
	if err := json.Unmarshal([]byte(out), &failure); err != nil || code != 2 || stderr != "" || failure.Error.Reason != "usage" || len(failure.Error.Details.Topics) != 6 {
		t.Fatalf("unknown JSON: %d %s %s", code, out, stderr)
	}
}

// Validate every bl invocation in inline code or a shell block against live help.
// Quoted strings are arguments, not flags. Prose flags must exist somewhere in
// the referenced command set too. This catches guidance drifting after renames.
func instructionDrift(markdown string) error {
	snippets := regexp.MustCompile("(?m)(?:`|^)bl ([^`\\n]+)").FindAllStringSubmatch(markdown, -1)
	words := regexp.MustCompile(`"[^"]*"|'[^']*'|[^\s]+`)
	flagPattern := regexp.MustCompile(`(?:^|\s)(--[a-zA-Z][a-zA-Z0-9-]*|-[a-zA-Z]\b)`)
	known := ""
	for _, snippet := range snippets {
		tokens := words.FindAllString(snippet[1], -1)
		if len(tokens) == 0 {
			continue
		}
		command := tokens[0]
		code, help, stderr := run(command, "--help")
		if code != 0 || stderr != "" {
			return fmt.Errorf("unknown command %s: %s", command, stderr)
		}
		consumed := 1
		for strings.Contains(help, "\nCOMMANDS:") && consumed < len(tokens) && !strings.HasPrefix(tokens[consumed], "-") {
			command += " " + tokens[consumed]
			consumed++
			code, help, stderr = run(append(strings.Fields(command), "--help")...)
			if code != 0 || stderr != "" {
				return fmt.Errorf("unknown command %s: %s", command, stderr)
			}
		}
		known += "\n" + help
		for _, token := range tokens[consumed:] {
			if token == "--" {
				break
			}
			if !strings.HasPrefix(token, "-") {
				continue
			}
			name, _, _ := strings.Cut(token, "=")
			if !regexp.MustCompile(`(?:^|[\s,])` + regexp.QuoteMeta(name) + `(?:[\s,=]|$)`).MatchString(help) {
				return fmt.Errorf("unknown flag %s on %s", name, command)
			}
		}
	}
	for _, flag := range flagPattern.FindAllStringSubmatch(markdown, -1) {
		if !strings.Contains(known, flag[1]) {
			return fmt.Errorf("unknown prose flag %s", flag[1])
		}
	}
	return nil
}

func TestInstructionsCommandDrift(t *testing.T) {
	testkit.Isolate(t)
	var all strings.Builder
	for _, topic := range instructions.Names() {
		body, _ := instructions.Read(topic)
		all.WriteString(body)
		all.WriteString("\n")
	}
	if err := instructionDrift(all.String()); err != nil {
		t.Fatal(err)
	}
	// Validate the documented MCP loop against the server's published schema,
	// so renamed tools or arguments cannot silently strand agent recovery.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := backlogmcp.New(project.Config{})
	finished := make(chan error, 1)
	go func() { finished <- server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "instructions-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = session.Close()
		cancel()
		if err := <-finished; err != nil && ctx.Err() == nil {
			t.Error(err)
		}
	}()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(all.String(), "\n") {
		if !strings.HasPrefix(line, `{"tool":`) {
			continue
		}
		var example struct {
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(line), &example); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tool := range tools.Tools {
			if tool.Name != example.Tool {
				continue
			}
			found = true
			encoded, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatal(err)
			}
			for argument := range example.Arguments {
				if _, ok := schema.Properties[argument]; !ok {
					t.Fatalf("unknown documented MCP argument %s on %s", argument, example.Tool)
				}
			}
		}
		if !found {
			t.Fatalf("unknown documented MCP tool %s", example.Tool)
		}
	}
	for _, bad := range []string{"`bl nonexistent`", "bl next --nonexistent", "`bl next --if-version VERSION`", "`bl config nonexistent`"} {
		if err := instructionDrift(bad); err == nil {
			t.Fatalf("drift detector accepted %q", bad)
		}
	}
}

// Exercise the documented loop with returned versions, not invented hashes.
// This catches examples that use the wrong JSON path or stale criterion versions.
func TestInstructionsWorkflow(t *testing.T) {
	root := writeFixture(t)
	code, out, stderr := run("-C", root, "next", "--explain", "--json")
	var next struct {
		Data struct{ Items []struct{ ID string } }
	}
	if err := json.Unmarshal([]byte(out), &next); err != nil || code != 0 || stderr != "" || len(next.Data.Items) == 0 {
		t.Fatalf("next: %d %s %s", code, out, stderr)
	}
	id := next.Data.Items[0].ID
	var shown struct {
		Data struct {
			Item struct {
				Version  string
				Category string
			}
			Criteria []struct {
				Checked  bool
				Evidence string
			}
		}
	}
	read := func() string {
		t.Helper()
		code, out, stderr := run("-C", root, "show", id, "--json")
		if err := json.Unmarshal([]byte(out), &shown); err != nil || code != 0 || stderr != "" || shown.Data.Item.Version == "" {
			t.Fatalf("show: %d %s %s", code, out, stderr)
		}
		return shown.Data.Item.Version
	}
	version := read()
	for _, args := range [][]string{{"start", id}, {"note", id, "Plan: implement and verify."}, {"ac", id, "check", "1", "--evidence", "Required verification command passed."}, {"done", id}} {
		if args[0] == "ac" || args[0] == "done" {
			version = read()
		}
		result := writeOK(t, root, "", append(args, "--if-version", version)...)
		var write struct {
			Data struct {
				Outcomes []struct{ Item struct{ Version string } }
			}
		}
		if err := json.Unmarshal([]byte(result), &write); err != nil || len(write.Data.Outcomes) != 1 || write.Data.Outcomes[0].Item.Version == "" {
			t.Fatalf("write response: %s", result)
		}
		version = write.Data.Outcomes[0].Item.Version
	}
	read()
	if shown.Data.Item.Category != "done" || len(shown.Data.Criteria) == 0 || !shown.Data.Criteria[0].Checked || shown.Data.Criteria[0].Evidence != "Required verification command passed." {
		t.Fatalf("final item: %+v", shown)
	}
}
