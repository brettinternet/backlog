package backlogmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/testkit"
)

type providerTask struct {
	ID, Title, Status, Type, Priority, Milestone, ParentTaskID, DueDate       string
	Description, ImplementationPlan, ImplementationNotes, FinalSummary        string
	Assignees, Labels, References, Documentation, ModifiedFiles, Dependencies []string
	CreatedAt, UpdatedAt                                                      time.Time
	Ordinal                                                                   int
	AcceptanceCriteria, DefinitionOfDone                                      []model.Criterion
	Comments                                                                  []struct {
		Body, Author string
		CreatedAt    time.Time
	}
}

// Real 1.52 fixtures catch convention drift that a round trip through our own
// parser cannot: due_date, generated indexes, markers, comments and filenames.
// The CLI is a pinned test oracle only; production never executes it.
func TestBacklogMD152Compatibility(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 for pinned Backlog.md 1.52.0 compatibility")
	}
	testkit.Isolate(t)
	t.Setenv("BUN_INSTALL_CACHE_DIR", filepath.Join(t.TempDir(), "bun-cache"))
	t.Setenv("EDITOR", "vi")
	root := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		argv := append([]string{"--package", "backlog.md@1.52.0", "backlog"}, args...)
		cmd := exec.CommandContext(ctx, "bunx", argv...)
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bunx %v: %v\n%s", argv, err, output)
		}
		return string(output)
	}
	if version := strings.TrimSpace(run("--version")); !strings.HasSuffix(version, "1.52.0") {
		t.Fatalf("unexpected provider version: %s", version)
	}
	run("init", "Fixture", "--defaults", "--no-git", "--integration-mode", "none", "--zero-padded-ids", "3", "--check-branches", "false", "--include-remote", "false")
	run("milestone", "add", "Foundation", "--description", "Milestone description.")
	run("milestone", "add", "Prior", "--description", "Archived milestone description.")
	run("milestone", "archive", "Prior")
	configPath := filepath.Join(root, "backlog", "config.yml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config = []byte(strings.Replace(string(config), "labels: []", "labels: [feature, backend, provider]", 1))
	if err := os.WriteFile(configPath, config, 0644); err != nil {
		t.Fatal(err)
	}
	run("task", "create", "Parent", "--plain")
	run("task", "create", "Provider fixture", "--status", "In Progress", "--description", "Description body.", "--assignee", "@alice,@bob", "--labels", "feature,backend", "--priority", "high", "--type", "bug", "--due-date", "2026-12-01", "--ordinal", "500", "--milestone", "Foundation", "--ac", "First criterion", "--ac", "#12 is user text", "--dod", "Verified", "--plan", "Plan content.", "--notes", "Notes content.", "--parent", "TASK-001", "--depends-on", "TASK-001", "--ref", "docs/api.md", "--doc", "https://example.test/spec", "--modified-file", "internal/api.go", "--plain")
	s, err := New(Options{ID: "tasks", Path: root, CommonDir: filepath.Join(root, ".git"), Now: func() time.Time { return adapterClock }})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(context.Background(), source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	var fixture model.Item
	for _, item := range loaded.Items {
		if item.Title == "Provider fixture" {
			fixture = item
		}
	}
	if fixture.Ref.ID == "" {
		t.Fatalf("fixture missing: %+v", loaded)
	}
	run("task", "edit", fixture.Ref.ID, "--status", "Done", "--final-summary", "Shipped.", "--comment", "Provider comment.", "--comment-author", "alice", "--check-ac", "2", "--check-dod", "1", "--plain")
	fixture = readAdapterItem(t, s, fixture.Ref.ID)
	check := func(item model.Item) {
		t.Helper()
		plain := run("task", "view", item.Ref.ID, "--plain")
		var envelope struct{ Task providerTask }
		if err := json.Unmarshal([]byte(run("task", "view", item.Ref.ID, "--json")), &envelope); err != nil {
			t.Fatal(err)
		}
		assertProviderItem(t, item, envelope.Task, plain)
	}
	check(fixture)
	t.Log("Backlog.md 1.52.0 fixture read matches task view --plain and --json for every modeled provider field")

	milestones, labels, err := s.Catalog()
	if err != nil || len(milestones) != 2 || strings.Join(labels, ",") != "feature,backend,provider" {
		t.Fatalf("provider catalog: %+v %v %v", milestones, labels, err)
	}
	list := run("milestone", "list", "--plain", "--show-completed")
	for _, milestone := range milestones {
		if milestone.Archived {
			if milestone.Name != "Prior" || milestone.Description != "Archived milestone description." {
				t.Fatalf("archived milestone mismatch: %+v", milestone)
			}
		} else if milestone.Name != "Foundation" || milestone.Description != "Milestone description." || !strings.Contains(list, milestone.Key+": "+milestone.Name) {
			t.Fatalf("active milestone differs from list: %+v\n%s", milestone, list)
		}
	}
	// A title-valued reference resolves to the same catalog key as the CLI's
	// milestone option, without rewriting it during a read.
	candidate, err := s.locate(context.Background(), fixture.Ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(candidate.absolute)
	data = []byte(strings.Replace(string(data), "milestone: m-0", "milestone: Foundation", 1))
	if err := os.WriteFile(candidate.absolute, data, 0644); err != nil {
		t.Fatal(err)
	}
	if got := readAdapterItem(t, s, fixture.Ref.ID).Milestone; got != "m-0" {
		t.Fatalf("title milestone did not resolve: %q", got)
	}
	fixture = readAdapterItem(t, s, fixture.Ref.ID)
	fixture = applyAdapter(t, s, fixture, model.Mutation{Op: model.Batch, Changes: []model.Mutation{
		{Op: model.Set, Field: "title", Value: "Edited: provider"},
		{Op: model.Set, Field: "status", Value: "In Progress"},
		{Op: model.Set, Field: "milestone", Value: "m-0"},
		{Op: model.Set, Field: "assignees", Value: []string{"@carol"}},
		{Op: model.Set, Field: "priority", Value: "low"},
		{Op: model.Set, Field: "due", Value: "2026-12-02"},
		{Op: model.Add, Field: "labels", Value: "provider"},
		{Op: model.ReplaceSection, Section: "Description", Text: "Edited description."},
		{Op: model.ReplaceSection, Section: "Plan", Text: "Edited plan."},
		{Op: model.AppendNote, Text: "Appended note."},
		{Op: model.ReplaceSection, Section: "Summary", Text: "Edited summary."},
		{Op: model.RemoveCriterion, Section: "criteria", Index: 1},
		{Op: model.AddCriterion, Section: "criteria", Text: "Added criterion"},
		{Op: model.AppendComment, Comment: model.Comment{Time: adapterClock, Author: "@agent", Body: "Adapter comment."}},
	}})
	check(fixture)
	plain := run("task", "view", fixture.Ref.ID, "--plain")
	if !strings.Contains(plain, "- [x] #1 #12 is user text") || !strings.Contains(plain, "- [ ] #2 Added criterion") {
		t.Fatalf("provider did not see sequential generated indexes:\n%s", plain)
	}

	created, err := s.Apply(context.Background(), model.Mutation{Op: model.Create, Item: &model.Item{Title: "Café & API__ 你好 *  spaces.", Status: "In Progress", Description: "Created description.", Criteria: []model.Criterion{{Text: "Created criterion"}}, DoD: []model.Criterion{{Text: "Created DoD"}}, Plan: "Created plan.", Notes: "Created notes.", Summary: "Created summary.", Due: "2026-12-03", Assignees: []string{"@agent"}, Labels: []string{"provider"}, Milestone: "m-0", Fields: map[string]any{"ordinal": 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	check(created)
	t.Log("Adapter edits and padded/title-filename create read back identically through pinned CLI; criteria indexes remain sequential")

	run("task", "create", "Dependent", "--depends-on", created.Ref.ID, "--plain")
	created = applyAdapter(t, s, created, model.Mutation{Op: model.Set, Field: "status", Value: "draft"})
	draftPlain := run("draft", "view", created.Ref.ID, "--plain")
	if !strings.Contains(draftPlain, "Created description.") || !strings.Contains(draftPlain, "#1 Created criterion") || created.Status != "draft" || !strings.HasPrefix(created.Ref.ID, "DRAFT-") {
		t.Fatalf("provider cannot read demoted draft: %+v\n%s", created, draftPlain)
	}
	created = applyAdapter(t, s, created, model.Mutation{Op: model.Set, Field: "status", Value: "Done"})
	check(created)
	loaded, err = s.Load(context.Background(), source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	for _, dependent := range loaded.Items {
		if dependent.Title == "Dependent" {
			check(dependent)
			if len(dependent.BlockedBy) != 1 || dependent.BlockedBy[0].ID != created.Ref.ID {
				t.Fatalf("dependency did not follow draft round trip: %+v", dependent.BlockedBy)
			}
		}
	}
	t.Log("Demotion and promotion allocate provider IDs; pinned draft/task views read both results, and dependent references follow the new identity")
}

func assertProviderItem(t *testing.T, item model.Item, provider providerTask, plain string) {
	t.Helper()
	for _, pair := range []struct {
		name      string
		got, want any
	}{
		{"id", item.Ref.ID, provider.ID}, {"title", item.Title, provider.Title}, {"status", item.Status, provider.Status}, {"type", item.Type, provider.Type}, {"priority", string(item.Priority), provider.Priority}, {"due", item.Due, provider.DueDate}, {"milestone", item.Milestone, provider.Milestone},
		{"assignees", item.Assignees, provider.Assignees}, {"labels", item.Labels, provider.Labels}, {"files", item.Files, provider.ModifiedFiles}, {"refs", item.Refs, append(slices.Clone(provider.References), provider.Documentation...)},
		{"description", item.Description, provider.Description}, {"plan", item.Plan, provider.ImplementationPlan}, {"notes", item.Notes, provider.ImplementationNotes}, {"summary", item.Summary, provider.FinalSummary},
		{"criteria", item.Criteria, provider.AcceptanceCriteria}, {"dod", item.DoD, provider.DefinitionOfDone}, {"created", item.Created, provider.CreatedAt},
	} {
		if !reflect.DeepEqual(pair.got, pair.want) && !(isEmptyCollection(pair.got) && isEmptyCollection(pair.want)) {
			t.Errorf("%s differs: adapter=%#v provider=%#v", pair.name, pair.got, pair.want)
		}
	}
	updated := provider.UpdatedAt
	if updated.IsZero() {
		updated = provider.CreatedAt
	}
	if !item.Updated.Equal(updated) {
		t.Errorf("updated: %v != %v", item.Updated, updated)
	}
	if fmt.Sprint(item.Fields["ordinal"]) != fmt.Sprint(provider.Ordinal) && item.Fields["ordinal"] != nil {
		t.Errorf("ordinal differs: %v != %v", item.Fields["ordinal"], provider.Ordinal)
	}
	if item.Parent != nil && item.Parent.ID != provider.ParentTaskID || item.Parent == nil && provider.ParentTaskID != "" {
		t.Errorf("parent differs: %v != %s", item.Parent, provider.ParentTaskID)
	}
	if !slices.Equal(refStrings(item.BlockedBy), provider.Dependencies) {
		t.Errorf("dependencies differ: %v != %v", item.BlockedBy, provider.Dependencies)
	}
	if len(item.Comments) != len(provider.Comments) {
		t.Fatalf("comments differ: %v != %v", item.Comments, provider.Comments)
	}
	for i, comment := range item.Comments {
		other := provider.Comments[i]
		if comment.Author != other.Author || comment.Body != other.Body || !comment.Time.Equal(other.CreatedAt) {
			t.Errorf("comment differs: %+v != %+v", comment, other)
		}
	}
	// Every nonempty displayed field and body section is also exercised through
	// the acceptance contract's --plain surface, not just its JSON sibling.
	for _, text := range []string{item.Ref.ID + " - " + item.Title, item.Status, item.Type, item.Due, item.Milestone, strings.Join(item.Assignees, ", "), strings.Join(item.Labels, ", "), item.Description, item.Plan, item.Notes, item.Summary, strings.Join(item.Files, ", "), item.Created.Format("2006-01-02 15:04")} {
		if text != "" && !strings.Contains(plain, text) {
			t.Errorf("plain output omits %q:\n%s", text, plain)
		}
	}
	for _, criterion := range append(slices.Clone(item.Criteria), item.DoD...) {
		if !strings.Contains(plain, criterion.Text) {
			t.Errorf("plain output omits criterion %q", criterion.Text)
		}
	}
	for _, comment := range item.Comments {
		if !strings.Contains(plain, comment.Author) || !strings.Contains(plain, comment.Body) {
			t.Errorf("plain output omits comment %+v", comment)
		}
	}
}

func isEmptyCollection(value any) bool {
	v := reflect.ValueOf(value)
	return v.IsValid() && v.Kind() == reflect.Slice && v.Len() == 0
}
