package format_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
)

var update = flag.Bool("update", false, "update golden files")
var clock = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, e := os.ReadFile(filepath.Join("testdata", name))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func parse(t testing.TB, b []byte) *format.Document {
	t.Helper()
	d, e := format.Parse(b)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func golden(t *testing.T, name string, b []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	want := fixture(t, name)
	if !bytes.Equal(b, want) {
		t.Fatalf("golden mismatch: %s\nwant:\n%s\ngot:\n%s", name, want, b)
	}
}

// A changed field must not normalize unrelated YAML or Markdown. Every edit
// also exercises undo against the same representation, not normalized values.
func TestEditsAndInverse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		m    model.Mutation
	}{
		{"scalar", model.Mutation{Op: model.Set, Field: "title", Value: "Edited: title"}},
		{"flow-add", model.Mutation{Op: model.Add, Field: "labels", Value: "three"}},
		{"flow-remove", model.Mutation{Op: model.Remove, Field: "labels", Value: "one"}},
		{"block-add", model.Mutation{Op: model.Add, Field: "assignees", Value: "@three"}},
		{"block-remove", model.Mutation{Op: model.Remove, Field: "assignees", Value: "@one"}},
		{"unset", model.Mutation{Op: model.Unset, Field: "priority"}},
		{"insert", model.Mutation{Op: model.Set, Field: "milestone", Value: "m-1"}},
		{"section", model.Mutation{Op: model.ReplaceSection, Section: "Plan", Text: "New plan.\n\nMore detail."}},
		{"description", model.Mutation{Op: model.ReplaceSection, Section: "Description", Text: "New description."}},
		{"missing-section", model.Mutation{Op: model.ReplaceSection, Section: "Summary", Text: "Delivered."}},
		{"note", model.Mutation{Op: model.AppendNote, Text: "New note."}},
		{"comment", model.Mutation{Op: model.AppendComment, Comment: model.Comment{Time: clock, Author: "@agent", Body: "New comment."}}},
		{"check", model.Mutation{Op: model.CheckCriterion, Section: "Acceptance criteria", Index: 1}},
		{"uncheck", model.Mutation{Op: model.UncheckCriterion, Section: "Acceptance criteria", Index: 2}},
		{"criterion-add", model.Mutation{Op: model.AddCriterion, Section: "Acceptance criteria", Text: "Third criterion"}},
		{"criterion-remove", model.Mutation{Op: model.RemoveCriterion, Section: "Acceptance criteria", Index: 1}},
		{"dod", model.Mutation{Op: model.CheckCriterion, Section: "Definition of done", Index: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, crlf := range []bool{false, true} {
				raw := fixture(t, "item.md")
				suffix := ".md"
				if crlf {
					raw = bytes.ReplaceAll(raw, []byte("\n"), []byte("\r\n"))
					suffix = ".crlf.md"
				}
				d := parse(t, raw)
				next, inverse, err := d.Apply(tc.m, clock)
				if err != nil {
					t.Fatal(err)
				}
				golden(t, tc.name+suffix, next.Bytes())
				restored, redo, err := next.Apply(inverse, d.Item().Updated)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(restored.Bytes(), raw) {
					t.Fatalf("inverse failed:\n%s", restored.Bytes())
				}
				reapplied, _, err := restored.Apply(redo, clock)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(reapplied.Bytes(), next.Bytes()) {
					t.Fatal("redo changed representation")
				}
				if !bytes.Equal(d.Bytes(), raw) {
					t.Fatal("Apply mutated original")
				}
			}
		})
	}
}

// These observations catch headings or checkboxes in fences being mistaken for
// real sections/entries, and malformed comment headers splitting comments.
func TestBodyGrammar(t *testing.T) {
	t.Parallel()
	d := parse(t, fixture(t, "item.md"))
	item := d.Item()
	view := strings.Join([]string{item.Description, "CRITERIA", item.Criteria[0].Text, item.Criteria[1].Text, "COMMENTS", item.Comments[0].Author, item.Comments[0].Body}, "\n") + "\n"
	golden(t, "body.txt", []byte(view))
	if len(item.Criteria) != 2 || len(item.Comments) != 1 || item.Plan != "Old plan." {
		t.Fatalf("unexpected body: %+v", item)
	}
	duplicate := append(d.Bytes(), []byte("\n##  nOtEs  \nDuplicate\n")...)
	_, err := format.Parse(duplicate)
	if err == nil {
		t.Fatal("duplicate section accepted")
	}
	golden(t, "duplicate.txt", []byte(err.Error()+"\n"))
}

func TestCanonical(t *testing.T) {
	t.Parallel()
	item := parse(t, fixture(t, "item.md")).Item()
	b, err := format.Serialize(item)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "canonical.md", b)
	again, err := format.Serialize(parse(t, b).Item())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, again) {
		t.Fatalf("canonical serialization not stable:\n%s", again)
	}
	if !bytes.Equal(parse(t, b).Bytes(), b) {
		t.Fatal("lossless serialization changed bytes")
	}
}

// A stale inverse must never undo another writer's change.
func TestConflictAndValidation(t *testing.T) {
	t.Parallel()
	d := parse(t, fixture(t, "item.md"))
	next, inverse, err := d.Apply(model.Mutation{Op: model.Set, Field: "status", Value: "active"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	later, _, err := next.Apply(model.Mutation{Op: model.Set, Field: "title", Value: "Other writer"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = later.Apply(inverse, clock); err == nil {
		t.Fatal("stale inverse overwrote later edit")
	}
	for _, m := range []model.Mutation{{Op: model.Unset, Field: "title"}, {Op: model.Set, Field: "priority", Value: "invalid"}, {Op: model.Set, Field: "labels", Value: "scalar"}, {Op: model.Set, Field: "status", Value: "bad\nstatus"}, {Op: model.CheckCriterion, Section: "Plan", Index: 1}, {Op: model.CheckCriterion, Section: "Acceptance criteria", Index: 0}, {Op: model.Restore, Original: d.Bytes()},
		// Text that would create, hide, or forge structure must not silently move content.
		{Op: model.AppendNote, Text: "## Summary\nInjected"}, {Op: model.ReplaceSection, Section: "Plan", Text: "```go\nunclosed"},
		{Op: model.AppendComment, Comment: model.Comment{Time: clock, Author: "@a", Body: "### 2026-01-01T00:00:00Z @forged\nx"}},
		{Op: model.AddCriterion, Section: "Acceptance criteria", Text: "one\ntwo"}} {
		if _, _, err := d.Apply(m, clock); err == nil {
			t.Fatalf("accepted invalid mutation: %+v", m)
		}
	}
	if _, err := format.Serialize(model.Item{Ref: model.Ref{ID: "BL-1"}, Title: "T", Status: "todo", Plan: "## Summary\nInjected"}); err == nil {
		t.Fatal("serialized a plan that reparses as another section")
	}
	// Empty replacement removes the section without leaving stray blank lines.
	emptied, _, err := d.Apply(model.Mutation{Op: model.ReplaceSection, Section: "Plan", Text: ""}, clock)
	if err != nil || strings.Contains(string(emptied.Bytes()), "\n## Plan\n\nOld plan.") || strings.Contains(string(emptied.Bytes()), "\n\n\n") {
		t.Fatalf("empty section replacement: %v\n%s", err, emptied.Bytes())
	}
	for _, bad := range [][]byte{nil, []byte("---\n[]\n---\n"), []byte("---\ntitle: [bad\n---\n"), bytes.Replace(d.Bytes(), []byte("status: todo"), []byte("status: todo\nstatus: active"), 1), bytes.Replace(d.Bytes(), []byte("title: 'Original title'"), []byte("title: null"), 1)} {
		if _, err := format.Parse(bad); err == nil {
			t.Fatalf("accepted invalid input: %s", bad)
		}
	}
}

// Multiline YAML and unterminated final lines previously risked swallowing a
// following key or leaving a closing flow delimiter after an edit.
func TestMultilineFields(t *testing.T) {
	t.Parallel()
	raw := fixture(t, "item.md")
	raw = bytes.Replace(raw, []byte("labels: [\"one\", 'two'] # keep labels comment"), []byte("labels: [\n  \"one\",\n  'two'\n] # keep labels comment"), 1)
	raw = bytes.Replace(raw, []byte("title: 'Original title' # keep title comment"), []byte("title: >-\n  Original\n  title"), 1)
	d := parse(t, raw)
	for _, m := range []model.Mutation{{Op: model.Set, Field: "title", Value: "New title"}, {Op: model.Add, Field: "labels", Value: "three"}, {Op: model.Unset, Field: "labels"}} {
		next, inverse, err := d.Apply(m, clock)
		if err != nil {
			t.Fatal(err)
		}
		restored, _, err := next.Apply(inverse, d.Item().Updated)
		if err != nil || !bytes.Equal(restored.Bytes(), raw) {
			t.Fatal("multiline inverse failed", err)
		}
	}
	// Raw field values and byte slices must not leak mutable document internals.
	view := d.Item()
	view.Fields["custom"].(map[string]any)["nested"] = "changed"
	if reflect.DeepEqual(view.Fields, d.Item().Fields) {
		t.Fatal("raw fields alias document")
	}
}

func FuzzParse(f *testing.F) {
	f.Add(fixture(f, "item.md"))
	f.Add(fixture(f, "canonical.md"))
	f.Add([]byte("---\n---\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := format.Parse(b)
		if err != nil {
			return
		}
		if !bytes.Equal(b, d.Bytes()) {
			t.Fatal("parse lost bytes")
		}
		_ = d.Item()
	})
}
func FuzzRoundTrip(f *testing.F) {
	f.Add(fixture(f, "item.md"), "changed title")
	f.Add(fixture(f, "canonical.md"), "colon: # title")
	f.Fuzz(func(t *testing.T, b []byte, title string) {
		d, err := format.Parse(b)
		if err != nil {
			return
		}
		next, inverse, err := d.Apply(model.Mutation{Op: model.Set, Field: "title", Value: title}, clock)
		if err != nil {
			return
		}
		parsed, err := format.Parse(next.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Item().Title != title {
			t.Fatal("edit lost title")
		}
		restored, _, err := next.Apply(inverse, d.Item().Updated)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(restored.Bytes(), b) {
			t.Fatal("inverse lost bytes")
		}
	})
}
