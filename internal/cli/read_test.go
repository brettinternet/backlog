package cli_test

import (
	"database/sql"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/testkit"
	"github.com/charmbracelet/x/ansi"
)

var update = flag.Bool("update", false, "update read-command JSON goldens")

func readFixture(t *testing.T) string {
	t.Helper()
	testkit.Isolate(t)
	root := t.TempDir()
	for _, d := range []string{".git", ".config", "items", "other"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n bare = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := "version: 1\nsources: [{id: tasks, type: native, path: items, prefix: BL}]\nstatuses: [{key: todo, name: To Do, category: todo}, {key: done, name: Done, category: done}]\nviews: [{name: Open, query: 'is:open', sort: id, group: status}]\n"
	if err := os.WriteFile(filepath.Join(root, ".config", "backlog.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	for id, body := range map[string]string{
		"BL-012": "---\nid: BL-012\ntitle: Ready\nstatus: todo\npriority: high\n---\n\nBody.\n\n## Acceptance criteria\n\n- [ ] Works\n",
		"BL-13":  "---\nid: BL-13\ntitle: Blocked\nstatus: todo\nblocked_by: [BL-012]\n---\n",
		"BL-14":  "---\nid: BL-14\ntitle: Finished\nstatus: done\n---\n",
	} {
		body = strings.Replace(body, "status:", "created: 2026-10-21T00:00:00Z\nupdated: 2026-10-21T00:00:00Z\nstatus:", 1)
		if err := os.WriteFile(filepath.Join(root, "items", id+".md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Cache failures must not turn a valid read command into an unavailable error.
func TestReadCacheRecovery(t *testing.T) {
	root := readFixture(t)
	code, want, stderr := run("-C", root, "ls", "text:ody", "--json")
	if code != 0 || stderr != "" || !strings.Contains(want, `"id":"BL-012"`) || strings.Contains(want, `"id":"BL-13"`) {
		t.Fatalf("initial query: %d %s %s", code, want, stderr)
	}
	paths, err := filepath.Glob(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "backlog", "projects", "*", "*.sqlite"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("cache databases: %v %v", paths, err)
	}
	for _, kind := range []string{"corrupt", "schema"} {
		if kind == "corrupt" {
			if err := os.WriteFile(paths[0], []byte("not a database"), 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			db, err := sql.Open("sqlite", paths[0])
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec("PRAGMA user_version=999")
			_ = db.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		code, out, stderr := run("-C", root, "ls", "text:ody", "--json")
		if code != 0 || out != want || !strings.Contains(stderr, "warning: rebuilding disposable cache") {
			t.Fatalf("%s: %d %s %s", kind, code, out, stderr)
		}
	}
}

func TestReadIDs(t *testing.T) {
	root := readFixture(t)
	for _, id := range []string{"12", "012", "BL-12", "BL-012", "tasks:BL-12", "tasks:012"} {
		code, out, stderr := run("-C", root, "show", id, "--json")
		if code != 0 || stderr != "" || !strings.Contains(out, `"id":"BL-012"`) {
			t.Fatalf("%s: %d %s %s", id, code, out, stderr)
		}
	}
	archiveDuplicate := filepath.Join(root, "items", "archive", "BL-12.md")
	if err := os.MkdirAll(filepath.Dir(archiveDuplicate), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archiveDuplicate, []byte("---\nid: BL-12\ntitle: Duplicate\nstatus: todo\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := run("-C", root, "show", "12", "--json")
	if code != 2 || stderr != "" || !strings.Contains(out, "active and archived files share it") {
		t.Fatalf("duplicate: %d %s %s", code, out, stderr)
	}
	if err := os.Remove(archiveDuplicate); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = run("-C", root, "show", "12", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"id":"BL-012"`) {
		t.Fatalf("after duplicate removal: %d %s %s", code, out, stderr)
	}
	code, out, stderr = run("-C", root, "show", "999", "--json")
	if code != 3 || stderr != "" || !strings.Contains(out, `"reason":"not-found"`) {
		t.Fatalf("missing: %d %s %s", code, out, stderr)
	}
	configPath := filepath.Join(root, ".config", "backlog.yaml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config = []byte(strings.Replace(string(config), "prefix: BL}]", "prefix: BL}, {id: other, type: native, path: other, prefix: BL}]", 1))
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "items", "BL-012.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other", "BL-012.md"), data, 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = run("-C", root, "show", "12", "--json")
	if code != 2 || stderr != "" || !strings.Contains(out, `"matches":["tasks:BL-012","other:BL-012"]`) {
		t.Fatalf("ambiguous: %d %s %s", code, out, stderr)
	}
	code, out, stderr = run("-C", root, "show", "other:BL-012", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"source":"other"`) {
		t.Fatalf("qualified: %d %s %s", code, out, stderr)
	}
}

func TestReadJSONGolden(t *testing.T) {
	root := readFixture(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"ls", []string{"ls", "is:open,closed", "--sort", "id", "--group", "status"}},
		{"show", []string{"show", "12"}},
		{"next", []string{"next", "--explain"}},
	} {
		code, out, stderr := run(append([]string{"-C", root, "--json"}, tc.args...)...)
		if code != 0 || stderr != "" {
			t.Fatalf("%s: %d %s %s", tc.name, code, out, stderr)
		}
		var envelope struct{ Data json.RawMessage }
		if err := json.Unmarshal([]byte(out), &envelope); err != nil {
			t.Fatal(err)
		}
		var pretty strings.Builder
		var value any
		if err := json.Unmarshal(envelope.Data, &value); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		pretty.Write(encoded)
		pretty.WriteByte('\n')
		path := filepath.Join("testdata", tc.name+".json")
		if *update {
			if err := os.MkdirAll("testdata", 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(pretty.String()), 0600); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != pretty.String() {
			t.Fatalf("%s JSON differs (-update to regenerate):\n%s", tc.name, pretty.String())
		}
	}
	// The exported version must be the codec's actual content hash, not display metadata.
	bytes, err := os.ReadFile(filepath.Join(root, "items", "BL-012.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := format.Parse(bytes)
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := run("-C", root, "show", "12", "--json", "--section", "criteria")
	var envelope struct {
		Data struct {
			Item     struct{ Version string }
			Sections map[string]string
		}
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatal(err)
	}
	if code != 0 || envelope.Data.Item.Version != doc.Item().Version || len(envelope.Data.Sections) != 1 || envelope.Data.Sections["criteria"] != "- [ ] Works" {
		t.Fatalf("version/section: %s", out)
	}
}

func TestReadProjectionAndExplain(t *testing.T) {
	root := readFixture(t)
	for _, args := range [][]string{{"ls", "--view", "Open", "-n", "1"}, {"ls", "id:12", "--sort", "id"}, {"next", "id:12", "-n", "0"}} {
		code, out, stderr := run(append([]string{"-C", root, "--json"}, args...)...)
		if code != 0 || stderr != "" || !strings.Contains(out, `"id":"BL-012"`) || strings.Contains(out, `"id":"BL-13"`) {
			t.Fatalf("projection %v: %d %s %s", args, code, out, stderr)
		}
	}
	// ls defaults to is:open and loads archive/ only for is:archived (query.md).
	if err := os.MkdirAll(filepath.Join(root, "items", "archive"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "items", "archive", "BL-15.md"), []byte("---\nid: BL-15\ntitle: Archived\nstatus: todo\ncreated: 2026-10-21T00:00:00Z\nupdated: 2026-10-21T00:00:00Z\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for args, want := range map[string]string{"ls": "BL-012 BL-13", "ls is:archived": "BL-15", "ls is:closed": "BL-14"} {
		code, out, stderr := run(append([]string{"-C", root, "--json"}, strings.Fields(args)...)...)
		for _, id := range strings.Fields("BL-012 BL-13 BL-14 BL-15") {
			if code != 0 || stderr != "" || strings.Contains(out, `"id":"`+id+`"`) != strings.Contains(want, id) {
				t.Fatalf("%s: %d %s %s", args, code, out, stderr)
			}
		}
	}
	code, out, _ := run("-C", root, "next", "--explain", "--json")
	var envelope struct {
		Data struct {
			Items        []struct{ ID string }
			Explanations []struct {
				Included  bool
				Readiness string
				Reasons   []struct{ Code string }
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatal(err)
	}
	if code != 0 || len(envelope.Data.Items) != 1 || len(envelope.Data.Explanations) != 3 {
		t.Fatalf("explain: %s", out)
	}
	reasons := map[string]bool{}
	for _, e := range envelope.Data.Explanations {
		for _, r := range e.Reasons {
			reasons[r.Code] = true
		}
		if e.Included && e.Readiness != "ready" {
			t.Fatal("included non-ready item")
		}
	}
	for _, reason := range []string{"candidate", "blocker", "status"} {
		if !reasons[reason] {
			t.Fatalf("missing reason %s: %s", reason, out)
		}
	}
	for _, args := range [][]string{{"show"}, {"show", "12", "13"}, {"show", "12", "--section", "bad"}, {"ls", "--view", "Missing"}, {"ls", "--sort", "bad"}, {"ls", "--group", "bad"}, {"ls", "-n", "-1"}, {"next", "-n", "-1"}, {"next", "sort:id"}, {"ls", "status:missing"}} {
		if code, out, stderr := run(append([]string{"-C", root, "--json"}, args...)...); code != 2 || stderr != "" {
			t.Fatalf("usage %v: %d %s %s", args, code, out, stderr)
		}
	}
}

func TestReadTerminalSafety(t *testing.T) {
	root := readFixture(t)
	path := filepath.Join(root, "items", "BL-012.md")
	// YAML escapes produce raw terminal controls in the parsed title.
	raw := "---\nid: BL-012\ntitle: \"\\e[31mRED\\e[0m \\e]8;;https://example.org\\aLINK\\e]8;;\\a\"\nstatus: todo\n---\n\n\x1b[31mBODY\x1b[0m \x1b]0;hidden-title\aSAFE\n"
	raw = strings.Replace(raw, "status:", "created: 2026-10-21T00:00:00Z\nupdated: 2026-10-21T00:00:00Z\nstatus:", 1)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"ls"}, {"show", "12"}, {"show", "12", "--section", "description"}, {"next"}} {
		code, out, stderr := run(append([]string{"-C", root}, args...)...)
		if code != 0 || stderr != "" || strings.ContainsAny(out, "\x1b\a") || strings.Contains(out, "hidden-title") || strings.Contains(out, "https://example.org") {
			t.Fatalf("unsafe output %v: %d %q %q", args, code, out, stderr)
		}
		if strings.HasPrefix(args[0], "show") && !strings.Contains(out, "BODY SAFE") {
			t.Fatalf("lost body: %q", out)
		}
	}
	code, out, _ := run("-C", root, "show", "12", "--json")
	if code != 0 || !strings.Contains(out, `\u001b`) {
		t.Fatalf("JSON was sanitized: %s", out)
	}
	long := strings.Replace(raw, `\e[31mRED\e[0m \e]8;;https://example.org\aLINK\e]8;;\a`, strings.Repeat("界", 100), 1)
	if err := os.WriteFile(path, []byte(long), 0600); err != nil {
		t.Fatal(err)
	}
	_, out, _ = run("-C", root, "ls")
	for _, line := range strings.Split(out, "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("wide table: %q", line)
		}
	}
}
