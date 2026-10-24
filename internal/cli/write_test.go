package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/cli"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

func writeFixture(t *testing.T) string {
	t.Helper()
	testkit.Isolate(t)
	root := t.TempDir()
	for _, dir := range []string{".git", ".config", "items"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	put(t, filepath.Join(root, ".git/config"), "[core]\n bare = false\n")
	put(t, filepath.Join(root, "backlog.yaml"), "version: 1\nsources: [{id: tasks, type: native, path: items, prefix: BL}]\nstatuses: [{key: todo, name: To Do, category: todo}, {key: doing, name: Doing, category: active}, {key: done, name: Done, category: done}]\nworkflow: {start: doing, done: done}\ndefaults: {type: task, dod: [Run checks]}\n")
	input := get(t, "testdata/write-input.md")
	put(t, filepath.Join(root, "items/BL-012.md"), input)
	return root
}
func put(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func get(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func writeRun(root, stdin string, args ...string) (int, string, string) {
	var out, stderr bytes.Buffer
	code := cli.Run(context.Background(), append([]string{"bl", "-C", root, "--json"}, args...), cli.BuildInfo{}, strings.NewReader(stdin), &out, &stderr)
	return code, out.String(), stderr.String()
}
func writeOK(t *testing.T, root, stdin string, args ...string) string {
	t.Helper()
	code, out, stderr := writeRun(root, stdin, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("%v: %d %s %s", args, code, out, stderr)
	}
	if !strings.Contains(out, `"version":"`) {
		t.Fatalf("write lacks version: %s", out)
	}
	return out
}

// += and -= split comma lists like =, so related+=A,B adds two IDs rather than
// one invalid reference reported by frontmatter line.
func TestSetCommaListEntries(t *testing.T) {
	root := writeFixture(t)
	path := filepath.Join(root, "items/BL-012.md")
	writeOK(t, root, "", "set", "12", "related+=7,8", "labels+=x,y", "labels-=bug,keep")
	if text := get(t, path); !strings.Contains(text, "labels: [x, y]") || !strings.Contains(text, "related: [BL-7, BL-8]") {
		t.Fatalf("comma entries not applied:\n%s", text)
	}
	writeOK(t, root, "", "set", "12", "related-=7,8")
	if strings.Contains(get(t, path), "related") {
		t.Fatalf("related-= kept entries:\n%s", get(t, path))
	}
	for _, value := range []string{"not an id", ""} {
		code, out, _ := writeRun(root, "", "set", "12", "related+=7,"+value)
		if code != 5 || !strings.Contains(out, `related value \"`+value+`\"`) || strings.Contains(out, "line") {
			t.Fatalf("invalid reference %q: %d %s", value, code, out)
		}
	}
}

// Auto-commit must stay off by default, require trust for project-enabled Git
// hooks, and commit a multi-item command exactly once through the real CLI.
func TestAutoCommitCLI(t *testing.T) {
	root := writeFixture(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	git := func(args ...string) string {
		t.Helper()
		data, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, data)
		}
		return string(data)
	}
	git("init", "--initial-branch=main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	put(t, filepath.Join(root, "items/BL-13.md"), strings.ReplaceAll(get(t, filepath.Join(root, "items/BL-012.md")), "BL-012", "BL-13"))
	git("add", ".")
	git("commit", "-m", "initial")
	head := git("rev-parse", "HEAD")
	writeOK(t, root, "", "set", "12", "title=Default off")
	if git("rev-parse", "HEAD") != head {
		t.Fatal("autoCommit defaulted on")
	}
	git("add", "items/BL-012.md")
	git("commit", "-m", "baseline")
	configPath := filepath.Join(root, "backlog.yaml")
	put(t, configPath, get(t, configPath)+"git: {autoCommit: true}\n")
	git("add", "backlog.yaml")
	git("commit", "-m", "enable")
	head = git("rev-parse", "HEAD")
	code, out, warnings := writeRun(root, "", "set", "12", "title=Untrusted write")
	if code != 0 || !strings.Contains(warnings, "untrusted") || git("rev-parse", "HEAD") != head {
		t.Fatalf("trust gate: %d %s %s", code, out, warnings)
	}
	git("add", "items/BL-012.md")
	git("commit", "-m", "baseline")
	if code, out, warnings := writeRun(root, "", "trust"); code != 0 {
		t.Fatalf("trust: %d %s %s", code, out, warnings)
	}
	count := git("rev-list", "--count", "HEAD")
	writeOK(t, root, "", "set", "12", "13", "priority=high")
	if got := git("show", "--format=", "--name-only", "HEAD"); got != "items/BL-012.md\nitems/BL-13.md\n" {
		t.Fatalf("wrong paths: %s", got)
	}
	if git("rev-list", "--count", "HEAD~1") != count {
		t.Fatal("multi-item command made multiple commits")
	}
	if !strings.HasPrefix(git("log", "-1", "--format=%s"), "docs(backlog): set BL-13 ") {
		t.Fatal("wrong message")
	}
}

func fileVersion(t *testing.T, root string) string {
	t.Helper()
	d, err := format.Parse([]byte(get(t, filepath.Join(root, "items/BL-012.md"))))
	if err != nil {
		t.Fatal(err)
	}
	return d.Version()
}

// These goldens detect incidental rewrites of quoting, unknown fields, comments,
// and unrelated sections at the actual command boundary, not a mocked codec.
func TestWriteGolden(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		stdin  string
		file   string
		editor string
	}{
		{"new", []string{"new", "New item !h #fresh @agent :feature <12 ^99", "--body", "-", "--ac", "New criterion, with a comma"}, "Body from stdin.\n", "items/BL-13.md", ""},
		{"set", []string{"set", "12", "title=Changed", "priority=high", "label+=new", "label-=bug", "related+=12"}, "", "", ""},
		{"start", []string{"start", "12"}, "", "", ""},
		{"done", []string{"done", "12"}, "", "", ""},
		{"note", []string{"note", "12", "-"}, "Note from stdin.\n", "", ""},
		{"comment", []string{"comment", "12", "-", "--author", "@agent"}, "Comment from stdin.\n", "", ""},
		{"ac-check", []string{"ac", "12", "check", "1", "--evidence", "task test passed"}, "", "", ""},
		{"ac-replace", []string{"ac", "12", "check", "2", "--evidence", "new proof"}, "", "", ""},
		{"ac-uncheck", []string{"ac", "12", "uncheck", "2"}, "", "", ""},
		{"ac-add", []string{"ac", "12", "add", "Third criterion"}, "", "", ""},
		{"ac-rm", []string{"ac", "12", "rm", "1"}, "", "", ""},
		{"edit", []string{"edit", "12"}, "", "", "sed \"s/Original'/Edited'/\" \"$1\" > \"$1.next\"\nmv \"$1.next\" \"$1\"\n"},
		{"edit-section", []string{"edit", "12", "--section", "plan"}, "", "", "printf 'New plan.\\n' > \"$1\"\n"},
		{"edit-criteria", []string{"edit", "12", "--section", "criteria"}, "", "", "printf '\\n- [ ] Added in editor.\\n' >> \"$1\"\n"},
		{"archive", []string{"archive", "12"}, "", "items/archive/BL-012.md", ""},
		{"unarchive", []string{"unarchive", "12"}, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeFixture(t)
			if tc.editor != "" {
				path := filepath.Join(root, "editor")
				put(t, path, "#!/bin/sh\nset -eu\n"+tc.editor)
				if err := os.Chmod(path, 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("EDITOR", path)
			}
			if tc.name == "unarchive" {
				writeOK(t, root, "", "archive", "12")
			}
			beforeEvents := readEvents(t, root)
			writeOK(t, root, tc.stdin, tc.args...)
			events := readEvents(t, root)[len(beforeEvents):]
			var mutations []hooks.Event
			for _, event := range events {
				if event.Type != "item.readiness" {
					mutations = append(mutations, event)
				}
			}
			if len(mutations) != 1 {
				t.Fatalf("expected one mutation event: %+v", events)
			}
			event := mutations[0]
			if event.Origin != "cli" || event.Depth != 0 || event.Cause != nil {
				t.Fatalf("wrong event context: %+v", event)
			}
			field := map[string]string{"new": "title", "set": "title", "start": "status", "done": "status", "note": "notes", "comment": "comments", "ac-check": "criteria", "ac-replace": "criteria", "ac-uncheck": "criteria", "ac-add": "criteria", "ac-rm": "criteria", "edit": "title", "edit-section": "plan", "edit-criteria": "criteria", "archive": "archived", "unarchive": "archived"}[tc.name]
			if _, ok := event.Changes[field]; !ok {
				t.Fatalf("event changes omit %s: %+v", field, event)
			}
			path := tc.file
			if path == "" {
				path = "items/BL-012.md"
			}
			result := get(t, filepath.Join(root, path))
			if tc.name == "archive" {
				if _, err := os.Stat(filepath.Join(root, "items/BL-012.md")); !os.IsNotExist(err) {
					t.Fatalf("archive did not move file: %v", err)
				}
			}
			if tc.name == "unarchive" {
				if _, err := os.Stat(filepath.Join(root, "items/archive/BL-012.md")); !os.IsNotExist(err) {
					t.Fatalf("unarchive did not move file: %v", err)
				}
			}
			// Only generated timestamps are normalized; every other byte is compared.
			result = regexp.MustCompile(`(?m)^updated: .*`).ReplaceAllString(result, "updated: <TIME>")
			if tc.name == "new" {
				result = regexp.MustCompile(`(?m)^created: .*`).ReplaceAllString(result, "created: <TIME>")
			}
			result = regexp.MustCompile(`(?m)^### \S+ @agent$`).ReplaceAllString(result, "### <TIME> @agent")
			golden := filepath.Join("testdata", "write-"+tc.name+".md")
			if *update {
				put(t, golden, result)
			}
			if want := get(t, golden); want != result {
				t.Fatalf("%s differs (-update to regenerate):\n%s", golden, result)
			}
		})
	}
}

// A veto and a timeout must both fail with exit 5 before item bytes or events change.
func TestBeforeHookVetoAndTimeoutDoNotWrite(t *testing.T) {
	for _, tc := range []struct {
		name, script, timeout, stderr string
	}{
		{"stderr", `#!/bin/sh
printf 'deny first\nsecond line\n' >&2
exit 7
`, "1s", "bl: deny first"},
		{"timeout", `#!/bin/sh
exec /bin/sleep 1
`, "10ms", "bl: before hook veto timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeFixture(t)
			paths, err := project.UserPaths()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(paths.ConfigFile), 0700); err != nil {
				t.Fatal(err)
			}
			hookPath := filepath.Join(root, "before-hook")
			put(t, hookPath, tc.script)
			if err := os.Chmod(hookPath, 0700); err != nil {
				t.Fatal(err)
			}
			put(t, paths.ConfigFile, fmt.Sprintf(`hooks:
  - name: veto
    on: item.updated
    phase: before
    run: [%q]
    timeout: %s
`, hookPath, tc.timeout))
			itemPath := filepath.Join(root, "items", "BL-012.md")
			before := get(t, itemPath)
			var out, stderr bytes.Buffer
			code := cli.Run(context.Background(), []string{"bl", "-C", root, "set", "12", "title=Rejected"}, cli.BuildInfo{}, strings.NewReader(""), &out, &stderr)
			if code != 5 || !strings.HasPrefix(stderr.String(), tc.stderr) {
				t.Fatalf("before hook result: exit=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
			}
			if got := get(t, itemPath); got != before {
				t.Fatalf("veto wrote the item: %s", got)
			}
			if _, err := os.Stat(filepath.Join(paths.StateDir, "events")); !os.IsNotExist(err) {
				t.Fatalf("veto appended an event: %v", err)
			}
		})
	}
}

func TestWriteValidationAndBatch(t *testing.T) {
	root := writeFixture(t)
	path := filepath.Join(root, "items/BL-012.md")
	before := get(t, path)
	for _, args := range [][]string{
		{"set", "12", "unknown=value"}, {"set", "12", "status=missing"}, {"set", "12", "priority=missing"},
		{"set", "12", "title=Must not persist", "due=not-a-date"},
	} {
		code, out, _ := writeRun(root, "", args...)
		if code != 5 || !strings.Contains(out, `"reason":"rejected"`) {
			t.Fatalf("%v: %d %s", args, code, out)
		}
		if args[2] != "title=Must not persist" && !strings.Contains(out, "allowed") {
			t.Fatalf("missing allowed values: %s", out)
		}
		if get(t, path) != before {
			t.Fatalf("rejected %v changed bytes", args)
		}
	}
	code, out, _ := writeRun(root, "", "set", "12", "999", "priority=high")
	var result struct {
		Data struct {
			Outcomes []struct {
				ID    string
				Item  *struct{ Version string }
				Error *cli.Error
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if code != 3 || len(result.Data.Outcomes) != 2 || result.Data.Outcomes[0].Item == nil || result.Data.Outcomes[0].Item.Version == "" || result.Data.Outcomes[1].Error == nil || result.Data.Outcomes[1].Error.Reason != "not-found" {
		t.Fatalf("partial batch: %d %s", code, out)
	}
	if !strings.Contains(get(t, path), "priority: high") {
		t.Fatal("successful batch item not written")
	}
	for _, name := range []string{"set", "archive", "unarchive"} {
		args := []string{name, "12", "999", "--if-version", "stale"}
		if name == "set" {
			args = append(args, "priority=low")
		}
		current := get(t, path)
		code, out, _ := writeRun(root, "", args...)
		if code != 2 || get(t, path) != current {
			t.Fatalf("multi version %s: %d %s", name, code, out)
		}
	}
	// A draft must be created in its status, never briefly ready for an agent to claim.
	code, out, _ = writeRun(root, "", "new", "Parked", "--status", "missing")
	if _, err := os.Stat(filepath.Join(root, "items/BL-13.md")); code != 5 || !strings.Contains(out, "allowed values") || !os.IsNotExist(err) {
		t.Fatalf("unknown status: %d %s %v", code, out, err)
	}
	code, out, _ = writeRun(root, "", "new", "Parked", "--status", "doing")
	if code != 0 || !strings.Contains(out, `"status":"doing"`) {
		t.Fatalf("new --status: %d %s", code, out)
	}
}

func TestCriterionVersionAndEvidence(t *testing.T) {
	root := writeFixture(t)
	path := filepath.Join(root, "items/BL-012.md")
	old := fileVersion(t, root)
	writeOK(t, root, "", "ac", "12", "rm", "1", "--if-version", old)
	current := get(t, path)
	version := fileVersion(t, root)
	code, out, _ := writeRun(root, "", "ac", "12", "check", "1", "--if-version", old)
	if code != 4 || !strings.Contains(out, `"reason":"conflict"`) || get(t, path) != current {
		t.Fatalf("stale criterion changed: %d %s", code, out)
	}
	writeOK(t, root, "", "ac", "12", "check", "1", "--if-version", version, "--evidence", "fresh proof")
	result := get(t, path)
	if !strings.Contains(result, "- [x] Second\n  Context stays here.\n  Evidence: fresh proof") || strings.Contains(result, "old proof") {
		t.Fatal(result)
	}
	// Rewording keeps the entry's check state, position, and evidence.
	writeOK(t, root, "", "ac", "12", "set", "1", "Second", "reworded")
	if result = get(t, path); !strings.Contains(result, "- [x] Second reworded\n  Context stays here.\n  Evidence: fresh proof") {
		t.Fatal(result)
	}
	code, out, _ = writeRun(root, "", "show", "12")
	var detail struct {
		Data struct {
			Criteria []struct {
				Text, Evidence string
				Checked        bool
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &detail); err != nil {
		t.Fatal(err)
	}
	if code != 0 || len(detail.Data.Criteria) != 1 || detail.Data.Criteria[0].Evidence != "fresh proof" || !detail.Data.Criteria[0].Checked {
		t.Fatalf("show evidence: %d %s", code, out)
	}
	before := get(t, path)
	code, out, _ = writeRun(root, "", "ac", "12", "check", "1", "2", "--evidence", "bad target")
	if code != 2 || get(t, path) != before {
		t.Fatalf("multiple evidence targets: %d %s", code, out)
	}
	writeOK(t, root, "", "ac", "12", "uncheck", "1")
	if strings.Contains(get(t, path), "Evidence:") {
		t.Fatal("uncheck retained evidence")
	}
	// DoD numbering is separate, so --dod must never touch criterion 1.
	writeOK(t, root, "", "ac", "12", "add", "Run checks", "--dod")
	writeOK(t, root, "", "ac", "12", "check", "1", "--dod", "--evidence", "task ci passed")
	result = get(t, path)
	if !strings.Contains(result, "## Definition of done\n\n- [x] Run checks\n  Evidence: task ci passed") || !strings.Contains(result, "- [ ] Second") {
		t.Fatal(result)
	}
	// --text must replace the section without ever launching the editor.
	t.Setenv("EDITOR", "false")
	writeOK(t, root, "Step one.\nStep two.\n", "edit", "12", "--section", "plan", "--text", "-")
	if result = get(t, path); !strings.Contains(result, "## Plan\n\nStep one.\nStep two.\n") {
		t.Fatal(result)
	}
}

func TestEditorRejectsAndRetains(t *testing.T) {
	for _, concurrent := range []bool{true, false} {
		name := "invalid"
		if concurrent {
			name = "concurrent"
		}
		t.Run(name, func(t *testing.T) {
			root := writeFixture(t)
			path := filepath.Join(root, "items/BL-012.md")
			before := get(t, path)
			script := "#!/bin/sh\nset -eu\n"
			wantCode := 5
			if concurrent {
				script += "printf '\\nConcurrent note.\\n' >> items/BL-012.md\nsed \"s/Original'/Edited'/\" \"$1\" > \"$1.next\"\nmv \"$1.next\" \"$1\"\n"
				wantCode = 4
			} else {
				script += "printf 'invalid frontmatter\\n' > \"$1\"\n"
			}
			editor := filepath.Join(root, "editor")
			put(t, editor, script)
			if err := os.Chmod(editor, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EDITOR", editor)
			code, out, _ := writeRun(root, "", "edit", "12")
			var envelope struct {
				Error struct {
					Reason  string
					Details struct{ EditedPath string }
				}
			}
			if err := json.Unmarshal([]byte(out), &envelope); err != nil {
				t.Fatal(err)
			}
			if code != wantCode || envelope.Error.Details.EditedPath == "" {
				t.Fatalf("editor: %d %s", code, out)
			}
			edited := get(t, envelope.Error.Details.EditedPath)
			t.Cleanup(func() { _ = os.Remove(envelope.Error.Details.EditedPath) })
			if concurrent {
				if envelope.Error.Reason != "conflict" || get(t, path) != before+"\nConcurrent note.\n" || !strings.Contains(edited, "title: 'Edited'") {
					t.Fatalf("lost concurrent or editor data: %s %s", get(t, path), edited)
				}
			} else if get(t, path) != before || edited != "invalid frontmatter\n" {
				t.Fatal("invalid edit changed original or was lost")
			}
		})
	}
}

func TestAllSingleWritesGuardVersion(t *testing.T) {
	root := writeFixture(t)
	path := filepath.Join(root, "items/BL-012.md")
	before := get(t, path)
	for _, args := range [][]string{
		{"set", "12", "priority=high"}, {"start", "12"}, {"done", "12"}, {"note", "12", "text"},
		{"comment", "12", "text", "--author", "@agent"}, {"ac", "12", "add", "new"}, {"archive", "12"}, {"unarchive", "12"}, {"edit", "12"},
	} {
		code, out, _ := writeRun(root, "", append(args, "--if-version", "stale")...)
		if code != 4 || !strings.Contains(out, `"reason":"conflict"`) || get(t, path) != before {
			t.Fatalf("unguarded %v: %d %s", args, code, out)
		}
	}
	code, out, _ := writeRun(root, "", "new", "New", "--if-version", "stale")
	if code != 4 || !strings.Contains(out, `"reason":"conflict"`) {
		t.Fatalf("create version: %d %s", code, out)
	}
}

// An unqualified blocker that exists only in another queue previously stored a
// local ref that never resolves, leaving the item silently never ready.
func TestNewResolvesBlockersAcrossSources(t *testing.T) {
	root := writeFixture(t)
	put(t, filepath.Join(root, "backlog.yaml"), "version: 1\nsources: [{id: web, type: native, path: web, prefix: WEB}, {id: api, type: native, path: api, prefix: API}, {id: legacy, type: native, path: legacy, prefix: API}]\nstatuses: [{key: todo, name: To Do, category: todo}, {key: done, name: Done, category: done}]\n")
	writeOK(t, root, "", "new", "--source", "api", "First")
	writeOK(t, root, "", "new", "--source", "api", "Second")
	writeOK(t, root, "", "new", "--source", "legacy", "Legacy first")
	for _, tc := range []struct {
		blocker string
		stored  model.Ref
		warning string
	}{
		{"API-2", model.Ref{Source: "api", ID: "API-2"}, ""},
		{"API-1", model.Ref{ID: "API-1"}, "bl: warning: blocker API-1 matches no single item; qualify it as source:ID\n"},
		{"API-9", model.Ref{ID: "API-9"}, "bl: warning: blocker API-9 matches no single item; qualify it as source:ID\n"},
	} {
		code, out, stderr := writeRun(root, "", "new", "--source", "web", "Blocked <"+tc.blocker)
		if code != 0 || stderr != tc.warning {
			t.Fatalf("<%s: %d %s %q", tc.blocker, code, out, stderr)
		}
		var envelope struct{ Data struct{ ID string } }
		if err := json.Unmarshal([]byte(out), &envelope); err != nil {
			t.Fatal(err)
		}
		doc, err := format.Parse([]byte(get(t, filepath.Join(root, "web", envelope.Data.ID+".md"))))
		if err != nil {
			t.Fatal(err)
		}
		if got := doc.Item().BlockedBy; len(got) != 1 || got[0] != tc.stored {
			t.Fatalf("<%s stored %+v", tc.blocker, got)
		}
	}
}
