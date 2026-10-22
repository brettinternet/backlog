package backlogmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/project"
	"go.yaml.in/yaml/v3"
)

func TestImportPreservesBacklogDataAndReportsMappings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	backlog := filepath.Join(root, "backlog")
	writeImportFixture(t, backlog)
	target := filepath.Join(root, "native")

	preview, err := Import(context.Background(), root, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("dry run created target: %v", err)
	}
	if preview.Counts.Tasks != 7 || preview.Counts.Drafts != 2 || preview.Counts.Archived != 2 || preview.Counts.Milestones != 2 {
		t.Fatalf("unexpected counts: %+v", preview.Counts)
	}
	if preview.Counts.Files != len(preview.OutputPaths) || len(preview.Files) != 9 {
		t.Fatalf("report does not cover every output: counts=%+v paths=%d reports=%d", preview.Counts, len(preview.OutputPaths), len(preview.Files))
	}
	if !hasConfigMapping(preview.Configuration, "task_prefix", "sources[0].prefix", "TASK") {
		t.Fatalf("native prefix was not inferred from item IDs: %+v", preview.Configuration)
	}
	if len(preview.UnresolvedMilestones) != 1 || preview.UnresolvedMilestones[0].Value != "Unknown Release" {
		t.Fatalf("unresolved milestone not reported: %+v", preview.UnresolvedMilestones)
	}
	if !containsSkipped(preview.Skipped, "docs/") || !containsSkipped(preview.Skipped, "decisions/") {
		t.Fatalf("non-task content was not reported as skipped: %+v", preview.Skipped)
	}
	if _, err := json.Marshal(preview); err != nil {
		t.Fatalf("report is not JSON serializable: %v", err)
	}

	report, err := Import(context.Background(), root, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflectStringSlicesEqual(preview.OutputPaths, report.OutputPaths) {
		t.Fatal("dry-run and real import plans differ")
	}
	configBytes, err := os.ReadFile(filepath.Join(target, ".config", "backlog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var settings project.Settings
	if err := yaml.Unmarshal(configBytes, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Project.Name != "sample" || len(settings.Labels) != 3 || len(settings.Defaults.DOD) != 2 {
		t.Fatalf("source configuration was not mapped: %+v", settings)
	}
	if len(settings.Milestones) != 2 || !settings.Milestones[0].Archived && !settings.Milestones[1].Archived {
		t.Fatalf("milestones were not imported with archive state: %+v", settings.Milestones)
	}
	if strings.Join(settings.Ready.ExcludeLabels, ",") != "blocked,deferred" {
		t.Fatalf("ready labels not mapped: %+v", settings.Ready)
	}

	readItem := func(path string) *format.Document {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := format.Parse(data)
		if err != nil {
			t.Fatalf("invalid native output %s: %v", path, err)
		}
		return doc
	}
	dotted := readItem("backlog/TASK-001.02.md")
	item := dotted.Item()
	if item.Ref.ID != "TASK-001.02" || item.Status != "todo" || item.Milestone != "m-1" || item.Due != "2026-05-01" || item.Priority != "high" || strings.Join(item.Assignees, ",") != "@alice" || strings.Join(item.Refs, ",") != "docs/example.md,https://example.test/spec" || strings.Join(item.Files, ",") != "internal/example.go" {
		t.Fatalf("dotted ID, status, or title milestone mapping lost: %+v", item)
	}
	if item.Parent == nil || item.Parent.ID != "TASK-001" || len(item.BlockedBy) != 1 || item.BlockedBy[0].ID != "TASK-20" || len(item.Related) != 1 || item.Related[0].ID != "TASK-10" || item.DuplicateOf == nil || item.DuplicateOf.ID != "TASK-20" {
		t.Fatalf("references were not rewritten, including archived dependency: %+v", item)
	}
	if len(item.Criteria) != 1 || !item.Criteria[0].Checked || !strings.Contains(item.Criteria[0].Text, "checked criterion") {
		t.Fatalf("checked state or criterion text was lost: %+v", item.Criteria)
	}
	if strings.Contains(dotted.SectionText("Acceptance criteria"), "<!--") || !strings.Contains(dotted.SectionText("Description"), "description content") || !strings.Contains(dotted.SectionText("Description"), "## Migration Detail\n\nKeep this legacy section.") {
		t.Fatalf("section markers were not removed or description lost")
	}
	if len(item.DoD) != 1 || item.DoD[0].Checked || item.Plan != "1. Keep order." || item.Summary != "Summary text." {
		t.Fatalf("body sections were not mapped: dod=%+v plan=%q summary=%q", item.DoD, item.Plan, item.Summary)
	}
	if len(item.Comments) != 1 || item.Comments[0].Author != "brett" || item.Comments[0].Time.UTC().Format(time.RFC3339) != "2026-01-02T03:04:00Z" || item.Comments[0].Body != "A retained comment." {
		t.Fatalf("comment was not mapped to native structure: %+v", item.Comments)
	}
	custom, ok := item.Fields["custom"].(map[string]any)
	if !ok || custom["enabled"] != true {
		t.Fatalf("unknown metadata was not retained: %#v", item.Fields)
	}

	archived := readItem("backlog/archive/TASK-20.md").Item()
	if archived.Ref.ID != "TASK-20" {
		t.Fatalf("archived task ID was not retained: %s", archived.Ref.ID)
	}
	completed := readItem("backlog/TASK-12.md").Item()
	if completed.Status != "done" {
		t.Fatalf("completed partition did not map to done: %s", completed.Status)
	}
	draft := readItem("backlog/DRAFT-001.md").Item()
	if draft.Status != "draft" {
		t.Fatalf("draft status was not assigned: %s", draft.Status)
	}
	archivedDraft := readItem("backlog/archive/DRAFT-002.md").Item()
	if archivedDraft.Status != "draft" {
		t.Fatalf("archived draft status was not assigned: %s", archivedDraft.Status)
	}
	first := readItem("backlog/archive/TASK-20.md").Item().Rank
	second := readItem("backlog/TASK-12.md").Item().Rank
	third := readItem("backlog/TASK-10.md").Item().Rank
	fourth := readItem("backlog/TASK-001.md").Item().Rank
	fifth := readItem("backlog/TASK-001.02.md").Item().Rank
	if !(first < second && second < third && third < fourth && fourth < fifth) {
		t.Fatalf("ordinal order was not retained by rank: %q %q %q %q %q", first, second, third, fourth, fifth)
	}
	if !hasMapping(preview.Files, "tasks/task-001.02.md", "dependencies", "blocked_by") || !hasMapping(preview.Files, "tasks/task-001.02.md", "custom", "custom") {
		t.Fatalf("per-field mapping report incomplete: %+v", preview.Files)
	}
}

func TestImportUsesRepositoryBacklogDirectoryConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixtureFile(t, root, "backlog.config.yml", "project_name: worklease\nbacklog_directory: docs/backlog\ntask_prefix: task\nstatuses: [To Do, In Progress, Done]\nlabels: [Blocked]\n")
	writeFixtureFile(t, root, "docs/backlog/tasks/task-1.md", basicTask("task-1", "To Do", "ordinal: 1\n"))
	report, err := Import(context.Background(), root, filepath.Join(root, "native"), true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts.Tasks != 1 || report.Files[0].SourcePath != "tasks/task-1.md" || report.Files[0].OutputPath != "backlog/TASK-1.md" {
		t.Fatalf("repository config did not select the backlog directory: %+v", report)
	}
	direct, err := Import(context.Background(), filepath.Join(root, "docs", "backlog"), filepath.Join(root, "native-direct"), true)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Configuration[0].Value != "worklease" || direct.Files[0].SourcePath != "tasks/task-1.md" || !hasConfigMapping(direct.Configuration, "task_prefix", "sources[0].prefix", "TASK") {
		t.Fatalf("backlog-directory input did not inherit ancestor config or normalize its prefix: %+v", direct)
	}
}

func TestImportRejectsCollisionsAndNonEmptyDryRunTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	backlog := filepath.Join(root, "backlog")
	writeFixtureFile(t, backlog, "tasks/task-1.md", basicTask("task-1", "To Do", "ordinal: 1\n"))
	writeFixtureFile(t, backlog, "completed/TASK-1.md", basicTask("TASK-1", "To Do", "ordinal: 2\n"))
	if _, err := Import(context.Background(), root, filepath.Join(root, "native"), true); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("expected case-insensitive ID collision refusal, got %v", err)
	}

	// A comment line shaped like a native comment header would split one
	// comment into two and attribute its text to a forged author.
	forged := filepath.Join(root, "forged")
	writeFixtureFile(t, forged, "tasks/task-1.md", basicTask("task-1", "To Do", "")+"## Comments\n<!-- COMMENTS:BEGIN -->\nauthor: brett\ncreated: 2026-01-02 03:04\n---\nFirst\n### 2026-01-03T00:00:00Z mallory\nquoted\n---\n<!-- COMMENTS:END -->\n")
	if _, err := Import(context.Background(), forged, filepath.Join(root, "forged-native"), true); err == nil || !strings.Contains(err.Error(), "native comment header") {
		t.Fatalf("expected comment structure refusal, got %v", err)
	}

	target := filepath.Join(root, "not-empty")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), root, target, true); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("expected non-empty target refusal in dry-run, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(target, "keep"))
	if err != nil || string(data) != "untouched" {
		t.Fatalf("non-empty target was modified: %q, %v", data, err)
	}
}

func FuzzParseSourceDocument(f *testing.F) {
	f.Add("---\nid: task-1\ntitle: Task\nstatus: To Do\n---\n## Description\ntext")
	f.Add("---\nfoo: [\n---\n```\n## Acceptance criteria\n<!-- AC:BEGIN -->")
	f.Add("---\nid: x\nid: y\n---\n")
	f.Fuzz(func(t *testing.T, input string) {
		_, _ = parseSourceDocument([]byte(input))
	})
}

func writeImportFixture(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, root, "config.yml", `project_name: sample
default_status: To Do
statuses: [To Do, In Progress, Done]
labels: [blocked, deferred, feature]
definition_of_done: ["task ci passes", "review complete"]
`)
	writeFixtureFile(t, root, "tasks/task-001.md", basicTask("task-001", "To Do", "ordinal: 150\nmilestone: m-1\n"))
	writeFixtureFile(t, root, "tasks/task-001.02.md", `---
id: task-001.02
title: Dotted child
status: To Do
assignee: '@alice'
priority: high
created_date: '2026-01-01 10:00'
updated_date: '2026-01-02 12:30'
due: 2026-05-01
labels: [feature]
dependencies: [task-20]
parent_task_id: task-001
related: [task-10]
duplicate_of: task-20
ordinal: 200
references: [docs/example.md]
documentation: [https://example.test/spec]
modified_files: [internal/example.go]
milestone: Foundation
custom:
  enabled: true
  values: [x, 7]
---
## Description
<!-- SECTION:DESCRIPTION:BEGIN -->
description content
<!-- SECTION:DESCRIPTION:END -->
## Migration Detail
Keep this legacy section.
## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] checked criterion
<!-- AC:END -->
## Definition of Done
<!-- DOD:BEGIN -->
- [ ] unchecked dod
<!-- DOD:END -->
## Implementation Plan
<!-- SECTION:PLAN:BEGIN -->
1. Keep order.
<!-- SECTION:PLAN:END -->
## Comments
<!-- COMMENTS:BEGIN -->
author: brett
created: 2026-01-02 03:04
---
A retained comment.
---
<!-- COMMENTS:END -->
## Final Summary
<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Summary text.
<!-- SECTION:FINAL_SUMMARY:END -->
`)
	writeFixtureFile(t, root, "tasks/task-10.md", basicTask("task-10", "To Do", "ordinal: 100\nmilestone: Unknown Release\n"))
	writeFixtureFile(t, root, "completed/task-12.md", basicTask("task-12", "To Do", "ordinal: 50\n"))
	writeFixtureFile(t, root, "archive/tasks/task-20.md", basicTask("task-20", "Done", "ordinal: 1\n"))
	writeFixtureFile(t, root, "drafts/draft-001.md", basicTask("draft-001", "Draft", ""))
	writeFixtureFile(t, root, "archive/drafts/draft-002.md", basicTask("draft-002", "Draft", "ordinal: 250\n"))
	writeFixtureFile(t, root, "milestones/m-1 - foundation.md", `---
id: m-1
title: Foundation
---
## Description
Base milestone description.
`)
	writeFixtureFile(t, root, "archive/milestones/m-0 - prior.md", `---
id: m-0
title: Prior
---
## Description
Archived milestone.
`)
	writeFixtureFile(t, root, "docs/design.md", "not imported")
	writeFixtureFile(t, root, "decisions/decision.md", "not imported")
}

func basicTask(id, status, extra string) string {
	return "---\nid: " + id + "\ntitle: Task " + id + "\nstatus: " + status + "\ncreated_date: '2026-01-01 00:00'\nupdated_date: '2026-01-02 00:00'\n" + extra + "---\n## Description\nBody for " + id + "\n"
}

func writeFixtureFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func containsSkipped(skipped []SkippedContent, path string) bool {
	for _, entry := range skipped {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func hasConfigMapping(mappings []FieldMapping, source, output string, value any) bool {
	for _, mapping := range mappings {
		if mapping.Source == source && mapping.Output == output && mapping.Value == value {
			return true
		}
	}
	return false
}

func hasMapping(files []FileReport, sourcePath, sourceField, outputField string) bool {
	for _, file := range files {
		if file.SourcePath != sourcePath {
			continue
		}
		for _, mapping := range file.Mappings {
			if mapping.Source == sourceField && mapping.Output == outputField {
				return true
			}
		}
	}
	return false
}

func reflectStringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
