package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cli"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func run(args ...string) (int, string, string) {
	return testkit.RunCLI(context.Background(), append([]string{"bl"}, args...),
		func(ctx context.Context, argv []string, out, errOut io.Writer) int {
			return cli.Run(ctx, argv, cli.BuildInfo{Version: "test", Commit: "abc123456789", BuildTime: "2026-10-21T01:29:04Z"}, strings.NewReader(""), out, errOut)
		})
}

func TestMachineOutput(t *testing.T) {
	testkit.Isolate(t)
	for _, tc := range []struct {
		args    []string
		code    int
		reason  string
		command string
	}{
		{[]string{"--json", "version"}, 0, "", "version"},
		{[]string{"--json", "--version"}, 0, "", "version"},
		{[]string{"version", "-j"}, 0, "", "version"},
		{[]string{"--json", "missing"}, 2, "usage", "bl"},
		{[]string{"--unknown", "--json"}, 2, "usage", "bl"},
		{[]string{"--json=garbage", "version"}, 2, "usage", "bl"},
		{[]string{"config", "show", "--unknown", "-j"}, 2, "usage", "config show"},
		{[]string{"version", "extra", "-j"}, 2, "usage", "version"},
		{[]string{"tui", "-j"}, 2, "usage", "tui"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			code, out, stderr := run(tc.args...)
			if code != tc.code || stderr != "" {
				t.Fatalf("exit=%d stderr=%q stdout=%q", code, stderr, out)
			}
			var envelope cli.Envelope
			decoder := json.NewDecoder(strings.NewReader(out))
			if err := decoder.Decode(&envelope); err != nil {
				t.Fatalf("invalid JSON: %v: %q", err, out)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				t.Fatalf("more than one envelope: %q", out)
			}
			if envelope.SchemaVersion != 1 || envelope.Command != tc.command || envelope.OK != (code == 0) {
				t.Fatalf("wrong envelope: %+v", envelope)
			}
			if tc.reason == "" {
				info, ok := envelope.Data.(map[string]any)
				if !ok || info["version"] != "test" || info["commit"] != "abc123456789" || info["build_time"] != "2026-10-21T01:29:04Z" || info["go"] == "" || envelope.Error != nil {
					t.Fatalf("missing build metadata: %+v", envelope)
				}
			} else if envelope.Data != nil || envelope.Error == nil || envelope.Error.Reason != tc.reason || envelope.Error.Details == nil {
				t.Fatalf("wrong error: %+v", envelope)
			}
		})
	}
	code, out, stderr := run("tui")
	if code != 6 || out != "" || !strings.Contains(stderr, "terminal") {
		t.Fatalf("non-terminal tui: %d %q %q", code, out, stderr)
	}
	code, out, stderr = run("--unknown", "--json=false")
	if code != 2 || out != "" || !strings.Contains(stderr, "usage") {
		t.Fatalf("human usage: %d %q %q", code, out, stderr)
	}
}

func TestInvocationDirectory(t *testing.T) {
	testkit.Isolate(t)
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	relative, err := filepath.Rel(original, directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", relative, "--json", "config", "show"},
		{"config", "show", "-C=" + directory, "-j"},
	} {
		code, out, stderr := run(args...)
		var envelope struct{ Data struct{ Directory string } }
		if err := json.Unmarshal([]byte(out), &envelope); err != nil || code != 0 || stderr != "" || envelope.Data.Directory != directory {
			t.Fatalf("directory resolution: %d %q %q: %v", code, out, stderr, err)
		}
	}
	current, err := os.Getwd()
	if err != nil || current != original {
		t.Fatalf("Run changed process cwd: %s: %v", current, err)
	}

	for _, args := range [][]string{
		{"-C", "--json", "version"},
		{"--C", "--json", "version"},
		{"--unknown", "-C", "--json"},
		{"-C=--json", "version"},
		{"version", "--", "--json"},
		{"show", "--section", "--json"}, // subcommand value flags are derived, not listed by hand
		{"missing"},
		{"--unknown"},
	} {
		code, out, stderr := run(args...)
		if code != 2 || out != "" || !strings.Contains(stderr, "usage") {
			t.Fatalf("flag value changed output mode: %v: %d %q %q", args, code, out, stderr)
		}
	}
	code, out, stderr := run("-j", "-C", filepath.Join(directory, "missing"), "version")
	var envelope cli.Envelope
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || code != 2 || stderr != "" || envelope.Error == nil || envelope.Error.Details["flag"] != "C" {
		t.Fatalf("directory error details: %d %q %q: %v", code, out, stderr, err)
	}
}

func TestConfigInvocation(t *testing.T) {
	home := testkit.Isolate(t)
	t.Chdir(home)
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte("ui: {highContrast: true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := func(want bool, args ...string) {
		t.Helper()
		code, out, stderr := run(append([]string{"config", "show", "--json"}, args...)...)
		var envelope struct{ Data project.Config }
		if err := json.Unmarshal([]byte(out), &envelope); err != nil || code != 0 || stderr != "" {
			t.Fatalf("config output: %d %q %q: %v", code, out, stderr, err)
		}
		if envelope.Data.UI.HighContrast != want || envelope.Data.Paths != paths {
			t.Fatalf("wrong effective settings: %+v", envelope.Data)
		}
		for _, path := range []string{envelope.Data.Paths.ConfigFile, envelope.Data.Paths.StateDir, envelope.Data.Paths.CacheDir} {
			if !strings.HasPrefix(path, home+string(filepath.Separator)) {
				t.Fatalf("CLI escaped isolated home: %s", path)
			}
		}
	}
	check(true)
	t.Setenv("BL_HIGH_CONTRAST", "false")
	check(false)
	check(true, "--high-contrast")
	t.Setenv("BL_HIGH_CONTRAST", "bad")
	code, out, stderr := run("--json", "config", "show")
	var envError cli.Envelope
	if err := json.Unmarshal([]byte(out), &envError); err != nil || code != 5 || stderr != "" || envError.Error == nil || envError.Error.Reason != "invalid-config" || !strings.Contains(envError.Error.Message, "BL_HIGH_CONTRAST") {
		t.Fatalf("invalid environment: %d %q %q: %v", code, out, stderr, err)
	}
	check(false, "--high-contrast=false")

	if err := os.WriteFile(paths.ConfigFile, []byte("ui: [broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BL_HIGH_CONTRAST", "")
	code, out, stderr = run("--json", "config", "show")
	var envelope cli.Envelope
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || code != 5 || stderr != "" || envelope.Error == nil || envelope.Error.Reason != "invalid-config" || !strings.Contains(envelope.Error.Message, paths.ConfigFile) {
		t.Fatalf("invalid config: %d %q %q: %v", code, out, stderr, err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte("ui:\n  highContast: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = run("--json", "config", "show")
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || code != 5 || stderr != "" || envelope.Error == nil || envelope.Error.Reason != "invalid-config" || !strings.Contains(envelope.Error.Message, paths.ConfigFile) || !strings.Contains(envelope.Error.Message, "line 2") || !strings.Contains(envelope.Error.Message, "ui.highContast") {
		t.Fatalf("config error location: %d %q %q: %v", code, out, stderr, err)
	}
	t.Setenv("BL_HIGH_CONTRAST", "bad")
	for _, args := range [][]string{{}, {"--help"}, {"--version"}, {"--json", "version"}, {"help", "version"}} {
		if code, out, stderr := run(args...); code != 0 || out == "" || stderr != "" {
			t.Fatalf("help/version loaded broken config: %d %q %q", code, out, stderr)
		}
	}
}

func TestOAuthCommandsRequireOptInAndInteraction(t *testing.T) {
	t.Chdir(testkit.Isolate(t))
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte("accounts:\n  fake:\n    - name: work\n      user: alice\n      oauth: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := run("--json", "auth", "login", "fake", "work")
	var envelope cli.Envelope
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || code != 6 || stderr != "" || envelope.Error == nil || envelope.Error.Reason != "interactive-login-required" {
		t.Fatalf("unattended login: %d %q %q: %v", code, out, stderr, err)
	}
	if strings.Contains(out, "canary") {
		t.Fatalf("OAuth error leaked config data: %s", out)
	}
	code, out, stderr = run("--json", "auth", "logout", "fake", "work")
	envelope = cli.Envelope{}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || code != 0 || stderr != "" || envelope.Error != nil {
		t.Fatalf("empty logout: %d %q %q: %v", code, out, stderr, err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte("accounts:\n  fake:\n    - name: work\n      user: alice\n      credential: [helper, canary-token]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = run("--json", "auth", "logout", "fake", "work")
	envelope = cli.Envelope{}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || code != 0 || stderr != "" || envelope.Error != nil || strings.Contains(out, "canary-token") {
		t.Fatalf("logout after switching to helper credentials: %d %q %q: %v", code, out, stderr, err)
	}
}

func TestMCPUsesRunStreams(t *testing.T) {
	home := testkit.Isolate(t)
	root := filepath.Join(home, "repo")
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
	if err := os.WriteFile(filepath.Join(root, "backlog.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	var diagnostics strings.Builder
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"bl", "-C", root, "mcp"}, cli.BuildInfo{}, serverConn, serverConn, &diagnostics)
	}()
	client := sdk.NewClient(&sdk.Implementation{Name: "run-stream-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &sdk.IOTransport{Reader: clientConn, Writer: clientConn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := session.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil || len(listed.Tools) != 12 {
		t.Fatalf("list tools: count=%d err=%v", len(listed.Tools), err)
	}
	called, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "next", Arguments: map[string]any{}})
	if err != nil || called.IsError {
		t.Fatalf("call next: %#v %v", called, err)
	}
	_ = session.Close()
	_ = clientConn.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("cli.Run exit=%d", code)
		}
	case <-ctx.Done():
		t.Fatal("cli.Run did not stop after stream closed")
	}
}
