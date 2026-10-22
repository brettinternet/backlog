package query_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/query"
)

// Catches metadata accidentally becoming title text (and literal title text
// accidentally becoming metadata), including quoted/escaped token boundaries.
func TestQuickAdd(t *testing.T) {
	t.Parallel()
	item, err := query.QuickAdd(`Debounce detail rendering !high @me @amy #tui #perf ^BL-3 <BL-10 <remote:42 ~m-2 :feature`, "@brett")
	if err != nil {
		t.Fatal(err)
	}
	want := model.Item{Title: "Debounce detail rendering", Priority: model.High, Assignees: []string{"@brett", "@amy"}, Labels: []string{"tui", "perf"}, Parent: &model.Ref{ID: "BL-3"}, BlockedBy: []model.Ref{{ID: "BL-10"}, {Source: "remote", ID: "42"}}, Milestone: "m-2", Type: "feature"}
	if !reflect.DeepEqual(item, want) {
		t.Fatalf("%#v", item)
	}
	for _, tc := range []struct {
		input, title string
		priority     model.Priority
	}{
		{`"!high" \@me \#1 \^BL-3 \<BL-4 \~m-2 \:bug`, "!high @me #1 ^BL-3 <BL-4 ~m-2 :bug", ""},
		{`"a \"quote\" and \\ slash"`, `a "quote" and \ slash`, ""},
		{`title !u`, "title", model.Urgent}, {`title !H`, "title", model.High}, {`title !m`, "title", model.Medium}, {`title !l`, "title", model.Low},
		{`title !high !low`, "title", model.Low}, {`hello\ world`, "hello world", ""},
	} {
		got, err := query.QuickAdd(tc.input, "@brett")
		if err != nil || got.Title != tc.title || got.Priority != tc.priority {
			t.Fatalf("%q: %#v %v", tc.input, got, err)
		}
	}
	got, err := query.QuickAdd(`title #"two words" @"a b" ~"release one" :"feature request" #"two words" <1 <1 ^a ^b`, "")
	if err != nil || !reflect.DeepEqual(got.Labels, []string{"two words"}) || !reflect.DeepEqual(got.Assignees, []string{"@a b"}) || got.Milestone != "release one" || got.Type != "feature request" || len(got.BlockedBy) != 1 || got.Parent.ID != "b" {
		t.Fatalf("%#v %v", got, err)
	}
	// Colons are reference syntax only for ^ and <.
	got, err = query.QuickAdd(`title #area: ~:q3 ::bug @:x`, "")
	if err != nil || !reflect.DeepEqual(got.Labels, []string{"area:"}) || got.Milestone != ":q3" || got.Type != ":bug" || !reflect.DeepEqual(got.Assignees, []string{"@:x"}) {
		t.Fatalf("%#v %v", got, err)
	}
	for _, input := range []string{"", "!high", `title !nope`, `title @me`, `title #`, `title ^`, `title <`, `title ~`, `title :`, `title !`, `title @`, `title ^remote:`, `"unfinished`, `unfinished\`} {
		_, err := query.QuickAdd(input, "")
		var pe *query.Error
		if !errors.As(err, &pe) || pe.Pos < 0 || pe.Pos > len(input) {
			t.Fatalf("%q: %v", input, err)
		}
	}
}
func FuzzQuickAdd(f *testing.F) {
	for _, s := range []string{`Debounce detail rendering !high @me #tui #perf ^BL-3 <BL-10 ~m-2 :feature`, `\#1 "!high"`, `title #"two words"`, "", `"unfinished`, `title !h`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		item, err := query.QuickAdd(input, "@brett")
		if err != nil {
			var pe *query.Error
			if !errors.As(err, &pe) || pe.Pos < 0 || pe.Pos > len(input) {
				t.Fatalf("unpositioned: %v", err)
			}
			return
		}
		if item.Title == "" {
			t.Fatal("missing title")
		}
	})
}
