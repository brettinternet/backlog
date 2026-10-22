package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/cli"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

func run(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), append([]string{"bl"}, args...),
		cli.BuildInfo{Version: "test", Commit: "abc1234"}, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
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
				if !ok || info["version"] != "test" || info["commit"] != "abc1234" || info["go"] == "" || envelope.Error != nil {
					t.Fatalf("missing build metadata: %+v", envelope)
				}
			} else if envelope.Data != nil || envelope.Error == nil || envelope.Error.Reason != tc.reason {
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

func TestConfigInvocation(t *testing.T) {
	home := testkit.Isolate(t)
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
		if !strings.HasPrefix(envelope.Data.Paths.StateDir, home+string(filepath.Separator)) {
			t.Fatal("CLI escaped isolated home")
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
	t.Setenv("BL_HIGH_CONTRAST", "bad")
	for _, args := range [][]string{{}, {"--help"}, {"--version"}, {"--json", "version"}, {"help", "version"}} {
		if code, out, stderr := run(args...); code != 0 || out == "" || stderr != "" {
			t.Fatalf("help/version loaded broken config: %d %q %q", code, out, stderr)
		}
	}
}
