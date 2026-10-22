package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/cli"
	"github.com/brettinternet/backlog/internal/format"
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
	put(t, filepath.Join(root, ".config/backlog.yaml"), "version: 1\nsources: [{id: tasks, type: native, path: items, prefix: BL}]\nstatuses: [{key: todo, name: To Do, category: todo}, {key: doing, name: Doing, category: active}, {key: done, name: Done, category: done}]\nworkflow: {start: doing, done: done}\ndefaults: {type: task, dod: [Run checks]}\n")
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
			writeOK(t, root, tc.stdin, tc.args...)
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
