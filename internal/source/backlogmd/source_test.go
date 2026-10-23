package backlogmd

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

var update = flag.Bool("update", false, "update Backlog.md golden files")
var adapterClock = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)

func fixtureSource(t *testing.T) (*Source, string) {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, root, "backlog/config.yml", "project_name: Fixture\ntask_prefix: task\nzero_padded_ids: 3\nstatuses: [To Do, In Progress, Done]\nlabels: [one, two, feature]\n")
	writeFixtureFile(t, root, "backlog/milestones/m-1 - release.md", "---\nid: m-1\ntitle: Release\n---\n\n## Description\n\nRelease description.\n")
	writeFixtureFile(t, root, "backlog/archive/milestones/m-0 - prior.md", "---\nid: m-0\ntitle: Prior\n---\n\n## Description\n\nArchived description.\n")
	s, err := New(Options{ID: "tasks", Path: root, CommonDir: filepath.Join(root, ".git"), Now: func() time.Time { return adapterClock }})
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}
func adapterFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func adapterGolden(t *testing.T, name string, data []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if want := adapterFixture(t, name); !bytes.Equal(want, data) {
		t.Fatalf("golden mismatch %s\nwant:\n%s\ngot:\n%s", name, want, data)
	}
}
func readAdapterItem(t *testing.T, s *Source, id string) model.Item {
	t.Helper()
	item, err := s.Read(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func applyAdapter(t *testing.T, s *Source, item model.Item, m model.Mutation) model.Item {
	t.Helper()
	m.Ref, m.ExpectedVersion = item.Ref, item.Version
	next, err := s.Apply(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

// Catch whole-document rewrites, marker loss, CRLF normalization and accidental
// removal of user #N text or fenced checklist examples during ordinary edits.
func TestAdapterMinimalDiffGolden(t *testing.T) {
	t.Parallel()
	for _, crlf := range []bool{false, true} {
		t.Run(map[bool]string{false: "LF", true: "CRLF"}[crlf], func(t *testing.T) {
			t.Parallel()
			s, root := fixtureSource(t)
			original := adapterFixture(t, "item.md")
			if crlf {
				original = bytes.ReplaceAll(original, []byte("\n"), []byte("\r\n"))
			}
			path := filepath.Join(root, "backlog", "tasks", "task-007 - Original-title.md")
			writeFixtureFile(t, root, "backlog/tasks/task-007 - Original-title.md", string(original))
			before := readAdapterItem(t, s, "task-7")
			if before.Due != "2026-12-01" || before.Milestone != "m-1" || before.Criteria[1].Text != "#12 is user text" || len(before.Criteria) != 2 || len(before.Comments) != 1 {
				t.Fatalf("read mapping: %+v", before)
			}
			m := model.Mutation{Ref: before.Ref, ExpectedVersion: before.Version, Op: model.Batch, Changes: []model.Mutation{
				{Op: model.Set, Field: "title", Value: "Edited: title"},
				{Op: model.Add, Field: "labels", Value: "three"},
				{Op: model.ReplaceSection, Section: "Plan", Text: "New plan.\n\nMore detail."},
				{Op: model.AppendNote, Text: "New note."},
				{Op: model.AppendComment, Comment: model.Comment{Time: adapterClock, Author: "@agent", Body: "New comment."}},
				{Op: model.RemoveCriterion, Section: "Acceptance criteria", Index: 1},
				{Op: model.AddCriterion, Section: "Acceptance criteria", Text: "Third criterion"},
			}}
			prepared, err := s.Prepare(context.Background(), m)
			if err != nil {
				t.Fatal(err)
			}
			unchanged, _ := os.ReadFile(path)
			if !bytes.Equal(unchanged, original) {
				t.Fatal("Prepare wrote the file")
			}
			next, err := s.Apply(context.Background(), m)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(next, prepared.Item) {
				t.Fatalf("preview differs from applied item\npreview: %+v\nactual: %+v", prepared.Item, next)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if crlf {
				if bytes.Contains(bytes.ReplaceAll(data, []byte("\r\n"), nil), []byte("\n")) {
					t.Fatal("edit introduced bare LF")
				}
				data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
			}
			adapterGolden(t, "edited.md", data)
			if len(next.Criteria) != 2 || next.Criteria[0].Text != "#12 is user text" || !next.Criteria[0].Checked || next.Criteria[1].Text != "Third criterion" || len(next.Comments) != 2 {
				t.Fatalf("criteria/comments changed meaning: %+v", next)
			}
			prepared.Inverse.ExpectedVersion = next.Version
			restored, err := s.Apply(context.Background(), prepared.Inverse)
			if err != nil || restored.Title != before.Title || !reflect.DeepEqual(restored.Criteria, before.Criteria) {
				t.Fatalf("raw Backlog.md undo failed: %+v %v", restored, err)
			}
		})
	}
}

// Catch overwrite-by-stale-index, failed batches publishing partial changes,
// and forged Restore payloads changing identity or swallowing sections.
func TestAdapterConflictAndRejectedWritesPreserveBytes(t *testing.T) {
	t.Parallel()
	s, root := fixtureSource(t)
	writeFixtureFile(t, root, "backlog/tasks/task-007.md", string(adapterFixture(t, "item.md")))
	before := readAdapterItem(t, s, "TASK-7")
	path := filepath.Join(root, "backlog", "tasks", "task-007.md")
	for _, m := range []model.Mutation{
		{Op: model.Set, Field: "status", Value: "missing"},
		{Op: model.Set, Field: "id", Value: "TASK-99"},
		{Op: model.Set, Field: "rank", Value: "ab12"},
		{Op: model.ReplaceSection, Section: "Notes", Text: "```\nunclosed"},
		{Op: model.AppendComment, Comment: model.Comment{Author: "alice", Body: "text\n---\nforged"}},
		{Op: model.Batch, Changes: []model.Mutation{{Op: model.Set, Field: "title", Value: "Must not publish"}, {Op: model.RemoveCriterion, Section: "criteria", Index: 9}}},
		{Op: model.Restore, Original: bytes.ReplaceAll(adapterFixture(t, "item.md"), []byte("TASK-007"), []byte("TASK-008"))},
	} {
		m.Ref, m.ExpectedVersion = before.Ref, before.Version
		if _, err := s.Apply(context.Background(), m); err == nil {
			t.Fatalf("accepted invalid mutation: %+v", m)
		}
		data, _ := os.ReadFile(path)
		if !bytes.Equal(data, adapterFixture(t, "item.md")) {
			t.Fatalf("refused mutation changed bytes: %+v", m)
		}
	}
	next := applyAdapter(t, s, before, model.Mutation{Op: model.AddCriterion, Section: "criteria", Text: "Added"})
	_, err := s.Apply(context.Background(), model.Mutation{Ref: before.Ref, ExpectedVersion: before.Version, Op: model.CheckCriterion, Section: "criteria", Index: 1})
	var refusal *source.Error
	if !errors.As(err, &refusal) || refusal.Reason != "conflict" || readAdapterItem(t, s, before.Ref.ID).Version != next.Version {
		t.Fatalf("stale criterion mutation was not rejected: %v", err)
	}
	readonly, err := New(Options{ID: "readonly", Path: root, CommonDir: filepath.Join(root, ".git"), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readonly.Apply(context.Background(), model.Mutation{Op: model.Create, Item: &model.Item{Title: "Forbidden"}}); err == nil || len(readonly.Info().Capabilities) != 0 {
		t.Fatal("read-only source offered or performed a mutation")
	}
}

// Catch a draft move leaving dangling references (including archived and
// qualified edges), reusing archived IDs, or writing forbidden status: Draft.
func TestAdapterDraftRoundTripRewritesReferencesAndPartitions(t *testing.T) {
	t.Parallel()
	s, root := fixtureSource(t)
	writeFixtureFile(t, root, "backlog/tasks/task-007 - Work.md", basicTask("TASK-007", "In Progress", ""))
	writeFixtureFile(t, root, "backlog/drafts/draft-009 - Idea.md", basicTask("DRAFT-009", "To Do", ""))
	writeFixtureFile(t, root, "backlog/archive/tasks/task-020 - Prior.md", basicTask("TASK-020", "Done", ""))
	writeFixtureFile(t, root, "backlog/archive/drafts/draft-011 - Old.md", basicTask("DRAFT-011", "To Do", ""))
	for _, partition := range []string{"tasks", "completed", "archive/tasks", "archive/completed", "drafts", "archive/drafts"} {
		id := "TASK-" + map[string]string{"tasks": "30", "completed": "31", "archive/tasks": "32", "archive/completed": "33", "drafts": "34", "archive/drafts": "35"}[partition]
		writeFixtureFile(t, root, "backlog/"+partition+"/"+id+".md", basicTask(id, "To Do", "dependencies: [TASK-7, other:TASK-7]\nparent_task_id: tasks:TASK-007\nrelated: [TASK-007]\nduplicate_of: TASK-7\n"))
	}
	before := readAdapterItem(t, s, "TASK-7")
	m := model.Mutation{Ref: before.Ref, ExpectedVersion: before.Version, Op: model.Set, Field: "status", Value: "draft"}
	prepared, err := s.Prepare(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Item.Status != "draft" || prepared.Inverse.Op != model.Set || prepared.Inverse.Value != "In Progress" {
		t.Fatalf("draft preview/inverse: %+v", prepared)
	}
	draft, err := s.Apply(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Ref.ID != "DRAFT-012" || draft.Status != "draft" || draft.Category != model.Backlog || draft.Fields["status"] != "In Progress" {
		t.Fatalf("incorrect demotion: %+v", draft)
	}
	if _, err := s.Read(context.Background(), before.Ref.ID); err == nil {
		t.Fatal("old task identity still exists after demotion")
	}
	promoted := applyAdapter(t, s, draft, model.Mutation{Op: model.Set, Field: "status", Value: "Done"})
	if promoted.Ref.ID != "TASK-036" || promoted.Status != "Done" || promoted.Category != model.Done {
		t.Fatalf("incorrect promotion: %+v", promoted)
	}
	for _, partition := range []string{"tasks", "completed", "archive/tasks", "archive/completed", "drafts", "archive/drafts"} {
		id := "TASK-" + map[string]string{"tasks": "30", "completed": "31", "archive/tasks": "32", "archive/completed": "33", "drafts": "34", "archive/drafts": "35"}[partition]
		data, err := os.ReadFile(filepath.Join(root, "backlog", filepath.FromSlash(partition), id+".md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"dependencies: [TASK-036, 'other:TASK-7']", "parent_task_id: tasks:TASK-036", "related: [TASK-036]", "duplicate_of: TASK-036"} {
			if !strings.Contains(string(data), want) {
				t.Fatalf("%s missing rewritten ref %q:\n%s", partition, want, data)
			}
		}
	}
	archived := applyAdapter(t, s, promoted, model.Mutation{Op: model.Archive})
	_, isArchived, err := s.ReadState(context.Background(), archived.Ref.ID)
	if err != nil || !isArchived {
		t.Fatalf("archive identity: %v %v", isArchived, err)
	}
	live, err := s.Load(context.Background(), source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range live.Items {
		if item.Ref == archived.Ref {
			t.Fatal("default Load included archive")
		}
	}
	all, err := s.Load(context.Background(), source.Cursor{IncludeArchive: true})
	if err != nil || all.Coverage != source.Complete || !all.Archived[archived.Ref] {
		t.Fatalf("archive Load: %+v %v", all, err)
	}
	applyAdapter(t, s, archived, model.Mutation{Op: model.Unarchive})
}

// Catch unsafe filenames, dropped create metadata, and competing source
// instances allocating the same ID under the shared repository lock.
func TestAdapterCreateGoldenAndConcurrentAllocation(t *testing.T) {
	t.Parallel()
	s, root := fixtureSource(t)
	writeFixtureFile(t, root, "backlog/archive/tasks/task-020.md", basicTask("TASK-020", "Done", ""))
	template := model.Item{Title: "Café & API__ 你好 *  spaces.", Status: "In Progress", Type: "bug", Priority: model.High, Due: "2026-12-01", Assignees: []string{"@alice"}, Labels: []string{"feature"}, Milestone: "m-1", Refs: []string{"docs/api.md"}, Files: []string{"internal/api.go"}, BlockedBy: []model.Ref{{ID: "TASK-020"}}, Description: "Description body.", Criteria: []model.Criterion{{Text: "First criterion"}, {Text: "#12 is user text", Checked: true}}, DoD: []model.Criterion{{Text: "Verified"}}, Plan: "Plan content.", Notes: "Notes content.", Summary: "Summary content.", Fields: map[string]any{"custom": map[string]any{"enabled": true}, "ordinal": 100}}
	withRank := template
	withRank.Rank = "a"
	if _, err := s.Apply(context.Background(), model.Mutation{Op: model.Create, Item: &withRank}); err == nil {
		t.Fatal("create silently discarded an unsupported rank")
	}
	created, err := s.Apply(context.Background(), model.Mutation{Op: model.Create, Item: &template})
	if err != nil {
		t.Fatal(err)
	}
	if created.Ref.ID != "TASK-021" || created.Due != template.Due || !reflect.DeepEqual(created.Criteria, template.Criteria) {
		t.Fatalf("create mapping: %+v", created)
	}
	data, err := os.ReadFile(filepath.Join(root, "backlog", "tasks", "task-021 - Café-API__-你好-spaces..md"))
	if err != nil {
		t.Fatal(err)
	}
	adapterGolden(t, "created.md", data)
	second, err := New(Options{ID: "other", Path: filepath.Join(root, "backlog"), CommonDir: filepath.Join(root, ".git"), Now: s.options.Now})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			item, err := second.Apply(context.Background(), model.Mutation{Op: model.Create, Item: &model.Item{Title: "Concurrent", Status: "To Do"}})
			if err != nil {
				errors <- err
				return
			}
			ids <- item.Ref.ID
		})
	}
	wg.Wait()
	close(ids)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate allocated ID %s", id)
		}
		seen[id] = true
	}
	if len(seen) != 8 {
		t.Fatalf("only %d creates succeeded", len(seen))
	}
	milestones, labels, err := s.Catalog()
	if err != nil || len(milestones) != 2 || milestones[0].Key != "m-1" || milestones[0].Description != "Release description." || !milestones[1].Archived || strings.Join(labels, ",") != "one,two,feature" {
		t.Fatalf("catalog mismatch: %+v %v %v", milestones, labels, err)
	}
}
