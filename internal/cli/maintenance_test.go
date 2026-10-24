package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/testkit"
)

func maintenanceItem(id, status, fields, body string) string {
	return "---\nid: " + id + "\ntitle: " + id + " item\nstatus: " + status + "\ncreated: 2026-10-21T00:00:00Z\nupdated: 2026-10-22T00:00:00Z\n" + fields + "---\n\n" + body
}

func findingsFromJSON(t *testing.T, output string) ([]struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
}, string) {
	t.Helper()
	var envelope struct {
		Error *struct {
			Reason string `json:"reason"`
		} `json:"error"`
		Data struct {
			Findings []struct {
				Code     string `json:"code"`
				Severity string `json:"severity"`
				Path     string `json:"path"`
				Line     int    `json:"line"`
			} `json:"findings"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("invalid check JSON: %v: %s", err, output)
	}
	reason := ""
	if envelope.Error != nil {
		reason = envelope.Error.Reason
	}
	return envelope.Data.Findings, reason
}

func TestInitCreatesValidProjectAndRefusesOverwrite(t *testing.T) {
	testkit.Isolate(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, ".git", "config"), "[core]\n bare = false\n")

	code, output, stderr := writeRun(root, "", "init")
	if code != 0 || stderr != "" || !strings.Contains(output, `"command":"init"`) {
		t.Fatalf("init: %d %s %s", code, output, stderr)
	}
	configPath := filepath.Join(root, "backlog.yaml")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	// Without a shared remote, the committed config must not pin this checkout's path.
	if strings.Contains(string(before), "id: /") {
		t.Fatalf("init wrote a host-local project id:\n%s", before)
	}
	if info, err := os.Stat(filepath.Join(root, "backlog")); err != nil || !info.IsDir() {
		t.Fatalf("backlog directory missing: %v", err)
	}
	code, output, stderr = writeRun(root, "", "check")
	if code != 0 || stderr != "" {
		t.Fatalf("check initialized project: %d %s %s", code, output, stderr)
	}
	findings, reason := findingsFromJSON(t, output)
	if reason != "" || len(findings) != 0 {
		t.Fatalf("initialized check findings: %#v (%s)", findings, reason)
	}

	entries, err := os.ReadDir(filepath.Join(root, "backlog"))
	if err != nil {
		t.Fatal(err)
	}
	code, output, stderr = writeRun(root, "", "init")
	if code != 5 || stderr != "" || !strings.Contains(output, `"reason":"rejected"`) {
		t.Fatalf("second init: %d %s %s", code, output, stderr)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	afterEntries, err := os.ReadDir(filepath.Join(root, "backlog"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || len(entries) != len(afterEntries) {
		t.Fatal("second init changed the project")
	}
}

func TestCheckReportsEveryFindingCodeWithLocations(t *testing.T) {
	root := writeFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "items", "archive"), 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, "items", "BL-1.md"), "---\ntitle: [broken\n---\n")
	put(t, filepath.Join(root, "items", "BL-2.md"), "---\nid: BL-2\ntitle: Missing required\nstatus: todo\n---\n")
	put(t, filepath.Join(root, "items", "BL-3.md"), maintenanceItem("BL-3", "todo", "priority: impossible\n", ""))
	put(t, filepath.Join(root, "items", "BL-4.md"), maintenanceItem("BL-5", "todo", "", ""))
	put(t, filepath.Join(root, "items", "BL-6.md"), maintenanceItem("BL-6", "todo", "", ""))
	put(t, filepath.Join(root, "items", "archive", "BL-6.md"), maintenanceItem("BL-6", "todo", "", ""))
	put(t, filepath.Join(root, "items", "BL-7.md"), maintenanceItem("BL-7", "unknown", "", ""))
	put(t, filepath.Join(root, "items", "BL-8.md"), maintenanceItem("BL-8", "todo", "blocked_by: [BL-9]\n", ""))
	put(t, filepath.Join(root, "items", "BL-9.md"), maintenanceItem("BL-9", "todo", "blocked_by: [BL-8]\n", ""))
	put(t, filepath.Join(root, "items", "BL-10.md"), maintenanceItem("BL-10", "todo", "parent: BL-11\n", ""))
	put(t, filepath.Join(root, "items", "BL-11.md"), maintenanceItem("BL-11", "todo", "parent: BL-10\n", ""))
	put(t, filepath.Join(root, "items", "BL-12.md"), maintenanceItem("BL-12", "todo", "", "## Notes\n\nFirst.\n\n## Notes\n\nSecond.\n"))
	put(t, filepath.Join(root, "items", "BL-13.md"), maintenanceItem("BL-13", "todo", "blocked_by: [BL-999]\n", ""))
	put(t, filepath.Join(root, "items", "BL-14.md"), maintenanceItem("BL-14", "todo", "", "<<<<<<< ours\nnot yaml\n=======\nalso not yaml\n>>>>>>> theirs\n"))
	put(t, filepath.Join(root, "items", "BL-15.md"), strings.TrimSuffix(strings.ReplaceAll(maintenanceItem("BL-15", "todo", "", ""), "\n", "\r\n"), "\r\n"))
	put(t, filepath.Join(root, "items", "BL-16.md"), maintenanceItem("BL-16", "todo", "refs: [BL-2, 0c32301, docs/x.md, items, \"https://example.com/a\"]\n", ""))

	code, output, stderr := writeRun(root, "", "check")
	if code != 5 || stderr != "" {
		t.Fatalf("check: %d %s %s", code, output, stderr)
	}
	findings, reason := findingsFromJSON(t, output)
	if reason != "rejected" {
		t.Fatalf("check error reason: %q: %s", reason, output)
	}
	codes := map[string]bool{}
	for _, finding := range findings {
		codes[finding.Code] = true
		if finding.Path == "" || finding.Line < 1 {
			t.Errorf("finding lacks location: %#v", finding)
		}
	}
	for _, expected := range []string{
		"invalid-frontmatter", "missing-required", "invalid-value", "filename-mismatch", "duplicate-id",
		"unknown-status", "blocker-cycle", "parent-cycle", "duplicate-section", "merge-conflict", "dangling-ref", "invalid-ref", "noncanonical",
	} {
		if !codes[expected] {
			t.Errorf("check omitted %s: %#v", expected, findings)
		}
	}
	if strings.Count(output, `"invalid-ref"`) != 2 || !strings.Contains(output, "path: BL-2") || !strings.Contains(output, "path: 0c32301") {
		t.Errorf("invalid-ref should flag only BL-2 and 0c32301: %s", output)
	}
	foundArchivePath := false
	for _, finding := range findings {
		if finding.Code == "duplicate-id" && finding.Path == "items/archive/BL-6.md" {
			foundArchivePath = true
		}
	}
	if !foundArchivePath {
		t.Fatal("archive duplicate finding did not name its path")
	}
}

func TestCheckFixAndConflictWriteRefusal(t *testing.T) {
	root := writeFixture(t)
	put(t, filepath.Join(root, "items", "BL-20.md"), maintenanceItem("BL-21", "todo", "", ""))
	mergeUpdated := maintenanceItem("BL-22", "todo", "", "")
	mergeUpdated = strings.Replace(mergeUpdated, "updated: 2026-10-22T00:00:00Z\n", "<<<<<<< ours\nupdated: 2026-10-22T00:00:00Z\n=======\nupdated: 2026-10-23T00:00:00Z\n>>>>>>> theirs\n", 1)
	put(t, filepath.Join(root, "items", "BL-22.md"), mergeUpdated)
	diff3Updated := maintenanceItem("BL-23", "todo", "", "")
	diff3Updated = strings.Replace(diff3Updated, "updated: 2026-10-22T00:00:00Z\n", "<<<<<<< ours\nupdated: 2026-10-22T00:00:00Z\n||||||| base\nupdated: 2026-10-21T00:00:00Z\n=======\nupdated: 2026-10-24T00:00:00Z\n>>>>>>> theirs\n", 1)
	put(t, filepath.Join(root, "items", "BL-23.md"), diff3Updated)
	mergeComments := maintenanceItem("BL-24", "todo", "", "## Comments\n\n<<<<<<< ours\n### 2026-10-22T01:00:00Z @a\n\nfirst comment\n=======\n### 2026-10-22T02:00:00Z @b\n\nsecond comment\n>>>>>>> theirs\n")
	put(t, filepath.Join(root, "items", "BL-24.md"), mergeComments)
	diff3Comments := maintenanceItem("BL-26", "todo", "", "## Comments\n\n<<<<<<< ours\n### 2026-10-22T01:00:00Z @a\n\nbase comment\n### 2026-10-22T02:00:00Z @b\n\nours comment\n||||||| base\n### 2026-10-22T01:00:00Z @a\n\nbase comment\n=======\n### 2026-10-22T01:00:00Z @a\n\nbase comment\n### 2026-10-22T03:00:00Z @c\n\ntheirs comment\n>>>>>>> theirs\n")
	put(t, filepath.Join(root, "items", "BL-26.md"), diff3Comments)
	mixed := maintenanceItem("BL-25", "todo", "", "## Plan\n\n<<<<<<< ours\nkeep ours\n=======\nkeep theirs\n>>>>>>> theirs\n")
	mixed = strings.Replace(mixed, "updated: 2026-10-22T00:00:00Z\n", "<<<<<<< ours\nupdated: 2026-10-22T00:00:00Z\n=======\nupdated: 2026-10-23T00:00:00Z\n>>>>>>> theirs\n", 1)
	put(t, filepath.Join(root, "items", "BL-25.md"), mixed)
	unsupported := maintenanceItem("BL-27", "todo", "", "<<<<<<< ours\nupdated: 2026-10-22T00:00:00Z\n=======\nupdated: 2026-10-23T00:00:00Z\n>>>>>>> theirs\n")
	put(t, filepath.Join(root, "items", "BL-27.md"), unsupported)
	noncanonical := strings.TrimSuffix(strings.ReplaceAll(maintenanceItem("BL-28", "todo", "", "Body."), "\n", "\r\n"), "\r\n")
	put(t, filepath.Join(root, "items", "BL-28.md"), noncanonical)

	code, output, stderr := writeRun(root, "", "check")
	if code != 5 || stderr != "" {
		t.Fatalf("initial check: %d %s %s", code, output, stderr)
	}
	initial, _ := findingsFromJSON(t, output)
	for _, finding := range initial {
		if finding.Path == "items/BL-22.md" && finding.Code != "merge-conflict" {
			t.Fatalf("frontmatter marker parsed as %s", finding.Code)
		}
	}

	code, output, stderr = writeRun(root, "", "check", "--fix")
	if code != 5 || stderr != "" {
		t.Fatalf("check --fix: %d %s %s", code, output, stderr)
	}
	findings, reason := findingsFromJSON(t, output)
	if reason != "rejected" || len(findings) != 2 {
		t.Fatalf("unresolved findings: %#v (%s)", findings, reason)
	}
	for _, finding := range findings {
		if finding.Code != "merge-conflict" {
			t.Fatalf("unexpected remaining finding: %#v", finding)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "items", "BL-20.md")); !os.IsNotExist(err) {
		t.Fatalf("filename mismatch was not repaired: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "items", "BL-21.md")); err != nil {
		t.Fatal(err)
	}
	for id, winner := range map[string]string{"BL-22": "2026-10-23T00:00:00Z", "BL-23": "2026-10-24T00:00:00Z", "BL-25": "2026-10-23T00:00:00Z"} {
		text := get(t, filepath.Join(root, "items", id+".md"))
		if !strings.Contains(text, "updated: "+winner) {
			t.Fatalf("%s did not select latest timestamp: %s", id, text)
		}
	}
	for id, expected := range map[string][]string{
		"BL-24": {"first comment", "second comment"},
		"BL-26": {"base comment", "ours comment", "theirs comment"},
	} {
		text := get(t, filepath.Join(root, "items", id+".md"))
		for _, comment := range expected {
			if !strings.Contains(text, comment) {
				t.Errorf("%s dropped %q: %s", id, comment, text)
			}
		}
		if id == "BL-26" && strings.Count(text, "base comment") != 1 {
			t.Errorf("diff3 duplicate comment was not removed: %s", text)
		}
		if _, err := format.Parse([]byte(text)); err != nil {
			t.Errorf("%s remained unparseable: %v", id, err)
		}
	}
	mixedResult := get(t, filepath.Join(root, "items", "BL-25.md"))
	if !strings.Contains(mixedResult, "<<<<<<< ours\nkeep ours") || !strings.Contains(mixedResult, ">>>>>>> theirs") {
		t.Fatal("unsupported hunk was changed or removed")
	}
	if get(t, filepath.Join(root, "items", "BL-27.md")) != unsupported {
		t.Fatal("unsupported-only hunk was modified")
	}
	canonical := get(t, filepath.Join(root, "items", "BL-28.md"))
	if strings.Contains(canonical, "\r") || !strings.HasSuffix(canonical, "\n") {
		t.Fatal("noncanonical line endings were not repaired")
	}

	before := get(t, filepath.Join(root, "items", "BL-27.md"))
	code, output, stderr = writeRun(root, "", "set", "27", "title=Changed")
	if code != 5 || stderr != "" || !strings.Contains(output, "merge-conflict") {
		t.Fatalf("write to conflicted file: %d %s %s", code, output, stderr)
	}
	if get(t, filepath.Join(root, "items", "BL-27.md")) != before {
		t.Fatal("write changed a conflicted item")
	}
}

func TestRenumberRewritesAllReferenceFormsAndRefusesAmbiguityAndCollision(t *testing.T) {
	root := writeFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "items", "archive"), 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "backlog.yaml")
	config := strings.Replace(get(t, configPath), "sources: [{id: tasks, type: native, path: items, prefix: BL}]", "sources: [{id: tasks, type: native, path: items, prefix: BL}, {id: other, type: native, path: other, prefix: GH}]", 1)
	put(t, configPath, config)
	if err := os.MkdirAll(filepath.Join(root, "other"), 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, "other", "BL-9.md"), maintenanceItem("BL-9", "todo", "related: [BL-9, tasks:BL-9]\n", ""))
	put(t, filepath.Join(root, "items", "BL-8.md"), maintenanceItem("BL-8", "todo", "parent: BL-9\nblocked_by: [BL-9, tasks:BL-9]\nrelated: [BL-9, tasks:BL-9]\nduplicate_of: tasks:BL-9\n", ""))
	put(t, filepath.Join(root, "items", "BL-9.md"), maintenanceItem("BL-9", "todo", "", "active duplicate\n"))
	put(t, filepath.Join(root, "items", "archive", "BL-9.md"), maintenanceItem("BL-9", "todo", "", "archived duplicate\n"))
	put(t, filepath.Join(root, "items", "archive", "BL-7.md"), maintenanceItem("BL-7", "todo", "related: [tasks:BL-9]\n", ""))
	put(t, filepath.Join(root, "items", "BL-40.md"), maintenanceItem("BL-40", "todo", "", "target\n"))
	activeBefore := get(t, filepath.Join(root, "items", "BL-9.md"))
	archiveBefore := get(t, filepath.Join(root, "items", "archive", "BL-9.md"))
	referrerBefore := get(t, filepath.Join(root, "items", "BL-8.md"))

	code, output, stderr := writeRun(root, "", "renumber", "BL-9", "BL-30")
	if code != 2 || stderr != "" || !strings.Contains(output, "matches multiple items") {
		t.Fatalf("ambiguous renumber: %d %s %s", code, output, stderr)
	}
	if get(t, filepath.Join(root, "items", "BL-9.md")) != activeBefore || get(t, filepath.Join(root, "items", "archive", "BL-9.md")) != archiveBefore || get(t, filepath.Join(root, "items", "BL-8.md")) != referrerBefore {
		t.Fatal("ambiguous renumber changed files")
	}

	code, output, stderr = writeRun(root, "", "renumber", "tasks:archive/BL-9.md", "BL-40")
	if code != 4 || stderr != "" || !strings.Contains(output, "target ID already exists") {
		t.Fatalf("target collision: %d %s %s", code, output, stderr)
	}
	if get(t, filepath.Join(root, "items", "BL-8.md")) != referrerBefore {
		t.Fatal("target collision partially rewrote references")
	}

	code, output, stderr = writeRun(root, "", "renumber", "tasks:archive/BL-9.md", "BL-30")
	if code != 0 || stderr != "" || !strings.Contains(output, `"to":"BL-30"`) {
		t.Fatalf("renumber: %d %s %s", code, output, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "items", "archive", "BL-9.md")); !os.IsNotExist(err) {
		t.Fatalf("old archived file remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "items", "archive", "BL-30.md")); err != nil {
		t.Fatal(err)
	}
	updatedReferrer := get(t, filepath.Join(root, "items", "BL-8.md"))
	for _, ref := range []string{"parent: BL-30", `blocked_by: [BL-30, "tasks:BL-30"]`, `related: [BL-30, "tasks:BL-30"]`, "duplicate_of: tasks:BL-30"} {
		if !strings.Contains(updatedReferrer, ref) {
			t.Errorf("reference form %q was not rewritten: %s", ref, updatedReferrer)
		}
	}
	if !strings.Contains(get(t, filepath.Join(root, "items", "archive", "BL-7.md")), `related: ["tasks:BL-30"]`) {
		t.Fatal("archived qualified reference was not rewritten")
	}
	otherSourceRef := get(t, filepath.Join(root, "other", "BL-9.md"))
	if !strings.Contains(otherSourceRef, `related: [BL-9, "tasks:BL-30"]`) {
		t.Fatalf("renumber changed unrelated unqualified ref or missed qualified ref: %s", otherSourceRef)
	}
	code, output, stderr = writeRun(root, "", "check")
	if code != 0 || stderr != "" {
		t.Fatalf("check after renumber: %d %s %s", code, output, stderr)
	}
	findings, reason := findingsFromJSON(t, output)
	if reason != "" || len(findings) != 0 {
		t.Fatalf("renumber left validation findings: %#v (%s)", findings, reason)
	}
}
