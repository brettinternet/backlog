package query_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
)

func fixture() (model.Item, query.Snapshot) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	item := model.Item{Ref: model.Ref{Source: "tasks", ID: "BL-001"}, Title: `Detail "pane" rendering`, Status: "doing", Category: model.Active, Priority: model.High, Rank: "a", Type: "feature", Assignees: []string{"@brett"}, Labels: []string{"tui", "perf"}, Milestone: "m-2", Due: "2026-10-21", Parent: &model.Ref{ID: "BL-3"}, Children: []model.Ref{{ID: "BL-4"}}, BlockedBy: []model.Ref{{ID: "BL-5"}}, Blocks: []model.Ref{{ID: "BL-6"}}, Created: now.Add(-14 * 24 * time.Hour), Updated: now.Add(-6 * time.Hour), Description: "Unicode café", Criteria: []model.Criterion{{Text: "body criterion"}}, DoD: []model.Criterion{{Text: "body dod"}}, Plan: "body plan", Notes: "body notes", Summary: "body summary", Comments: []model.Comment{{Body: "body comment"}}, Fields: map[string]any{"score": 3, "tags": []any{"x", "y"}, "list": []string{"z"}, "Owner": "ops"}}
	s := query.Snapshot{Items: []model.Item{item}, Now: now, Me: []string{"@brett"}, Coordination: true, States: map[model.Ref]query.State{item.Ref: {Readiness: "ready", Claim: "me", Archived: true, Pending: true}}, Settings: project.Settings{Statuses: []project.StatusConfig{{Key: "todo", Name: "To Do"}, {Key: "doing", Name: "In Progress"}, {Key: "done", Name: "Done"}}, Labels: []string{"declared", "perf"}, Milestones: []project.Milestone{{Key: "m-2", Name: "Release two"}, {Key: "m-1", Name: "Release one"}, {Key: "old", Name: "Old", Archived: true}}}}
	return item, s
}
func parse(t *testing.T, input string, hook bool) query.Query {
	t.Helper()
	q, err := query.Parse(input, query.Options{Hook: hook})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// Catches semantic divergence between shared consumers: every predicate has a
// positive and negative observation, including age direction and empty fields.
func TestFields(t *testing.T) {
	t.Parallel()
	item, s := fixture()
	cases := []struct {
		q    string
		want bool
	}{
		{`id:1`, true}, {`id:tasks:1`, true}, {`id:other:1`, false}, {`id:BL-001`, true}, {`id:2`, false},
		{`STATUS:"in progress"`, true}, {`status:done`, false}, {`category:active`, true}, {`category:done`, false},
		{`priority:HIGH`, true}, {`priority:>=high`, true}, {`priority:>high`, false}, {`priority:<urgent`, true}, {`priority:<=medium`, false}, {`priority:none`, false},
		{`assignee:me`, true}, {`assignee:@brett`, true}, {`assignee:none`, false}, {`assignee:@other`, false},
		{`label:no,tui -label:wontfix`, true}, {`label:TUI`, false}, {`label:none`, false}, {`type:feature`, true}, {`type:none`, false},
		{`milestone:m-2`, true}, {`milestone:none`, false}, {`source:tasks`, true}, {`source:none`, false},
		{`parent:3`, true}, {`parent:none`, false}, {`blocked-by:5`, true}, {`blocked-by:4`, false}, {`blocks:6`, true}, {`blocks:5`, false},
		{`created:2026-10-08`, true}, {`created:>=2026-10-09`, false}, {`created:2w`, true}, {`updated:<7d`, true}, {`updated:>12h`, false}, {`updated:<=6h`, true}, {`updated:>=6h`, true}, {`due:<today`, true}, {`due:>today`, false},
		{`"detail \"pane\""`, true}, {`bl-001`, true}, {`café`, false}, {`text:CAFÉ`, true}, {`text:"body criterion"`, true}, {`text:"body dod"`, true}, {`text:"body comment"`, true}, {`text:"body plan"`, true}, {`text:"body notes"`, true}, {`text:"body summary"`, true},
		{`field.score:3`, true}, {`field.score:4`, false}, {`field.tags:x`, true}, {`field.list:z`, true}, {`field.missing:none`, true}, {`field.owner:ops`, true}, {`field.Owner:none`, false},
		{`claimed:me`, true}, {`claimed:others`, false}, {`claimed:none`, false},
		{`is:open`, true}, {`is:closed`, false}, {`is:ready`, true}, {`is:blocked`, false}, {`is:waiting`, false}, {`is:unknown`, false}, {`is:mine`, true}, {`is:claimed`, true}, {`is:archived`, true}, {`is:pending`, true},
		{`has:parent`, true}, {`has:children`, true}, {`has:blockers`, true}, {`has:assignee`, true}, {`has:due`, true}, {`has:criteria`, true}, {`has:claim`, true},
		{`-is:open`, false}, {`is:ready,blocked`, true}, {`"detail pane"`, false}, {``, true},
	}
	for _, tc := range cases {
		t.Run(tc.q, func(t *testing.T) {
			q := parse(t, tc.q, false)
			if got := q.Match(item, s, nil); got != tc.want {
				t.Fatalf("Match = %v, want %v", got, tc.want)
			}
		})
	}
	// Each overlay readiness is distinct; unknown claims are not known-free.
	for _, state := range []string{"ready", "blocked", "waiting", "unknown"} {
		s.States[item.Ref] = query.State{Readiness: state}
		if !parse(t, "is:"+state, false).Match(item, s, nil) {
			t.Fatal(state)
		}
	}
	s.States = nil
	for _, f := range []string{"claimed:none", "is:claimed", "has:claim"} {
		if parse(t, f, false).Match(item, s, nil) {
			t.Fatal(f)
		}
	}
	empty := model.Item{Ref: item.Ref, Category: model.Done}
	for _, f := range []string{"priority:none", "assignee:none", "label:none", "type:none", "milestone:none", "parent:none", "is:closed", "is:unknown"} {
		if !parse(t, f, false).Match(empty, s, nil) {
			t.Fatal(f)
		}
	}
	for _, f := range []string{"parent", "children", "blockers", "assignee", "due", "criteria", "claim"} {
		if parse(t, "has:"+f, false).Match(empty, s, nil) {
			t.Fatal(f)
		}
	}
	for _, category := range []model.StatusCategory{model.Backlog, model.Todo, model.Active, model.Done, model.Canceled} {
		empty.Category = category
		closed := category == model.Done || category == model.Canceled
		if parse(t, "is:closed", false).Match(empty, s, nil) != closed {
			t.Fatal(category)
		}
	}
	s.Now = time.Date(2026, 10, 22, 23, 0, 0, 0, time.FixedZone("west", -7*3600))
	item.Updated = s.Now.Add(-time.Hour)
	if !parse(t, "updated:today", false).Match(item, s, nil) {
		t.Fatal("calendar comparison must use injected location")
	}
}

// Catches lost source locations, misleading typo diagnostics, invalid operators
// and silent acceptance of half-written filters.
func TestErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input      string
		pos        int
		suggestion string
	}{
		{`x      lable:tui`, 7, "label"}, {`is:raedy`, 3, "ready"}, {`label:`, 6, ""}, {`id:a,`, 5, ""}, {`id:,a`, 3, ""}, {`label:>a`, 6, ""}, {`due:2026-02-30`, 4, ""}, {`updated:999999999999999999w`, 8, ""}, {`"abc`, 0, ""}, {`abc\`, 3, ""}, {`-`, 1, ""}, {`field.:x`, 0, ""}, {`sort:bogus`, 5, ""}, {`group:status,label`, 0, ""}, {`-sort:id`, 0, ""}, {`sort:id sort:title`, 8, ""}, {`group:label group:type`, 12, ""}, {`is:open | is:closed`, 8, ""}, {`a && b`, 2, ""},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			_, err := query.Parse(tc.input, query.Options{})
			var pe *query.Error
			if !errors.As(err, &pe) || pe.Pos != tc.pos || pe.Suggestion != tc.suggestion {
				t.Fatalf("error = %#v, want position %d suggestion %q", err, tc.pos, tc.suggestion)
			}
		})
	}
	for _, quoted := range []string{`"|"`, `\|`, `a|b`} {
		if _, err := query.Parse(quoted, query.Options{}); err != nil {
			t.Fatalf("%s: %v", quoted, err)
		}
	}
	_, err := query.Parse(`x      lable:tui`, query.Options{})
	if err.Error() != `unknown field "lable" at 7; did you mean "label"?` {
		t.Fatal(err)
	}
	_, s := fixture()
	q := parse(t, `status:doign label:missing type:missing milestone:missing source:missing assignee:@missing`, false)
	if errs := q.Validate(s); len(errs) != 6 {
		t.Fatalf("validation errors: %v", errs)
	}
	if errs := parse(t, `status:"in progress" label:perf milestone:m-2 assignee:me label:NONE type:NONE assignee:NONE`, false).Validate(s); len(errs) != 0 {
		t.Fatal(errs)
	}
	s.Coordination = false
	for _, f := range []string{"claimed:me", "is:claimed", "has:claim"} {
		if len(parse(t, f, false).Validate(s)) != 1 {
			t.Fatal(f)
		}
	}
}

// Catches hook predicates leaking into ordinary filters or matching the wrong
// finished event. Missing context must not invent event facts.
func TestEvents(t *testing.T) {
	t.Parallel()
	item, s := fixture()
	for _, text := range []string{"changed:status", "changed:readiness", "hook:notify", "action:work", "outcome:ok", "outcome:failed", "outcome:timeout", "outcome:refused"} {
		_, err := query.Parse("x "+text, query.Options{})
		var pe *query.Error
		if !errors.As(err, &pe) || pe.Pos != 2 {
			t.Fatalf("%s: %v", text, err)
		}
		q := parse(t, text, true)
		if q.Match(item, s, nil) {
			t.Fatal("matched absent event", text)
		}
	}
	event := &query.Event{Type: "action.finished", Changed: []string{"status", "readiness"}, Action: "work", Hook: "notify", Outcome: "ok"}
	for _, tc := range []struct {
		q    string
		want bool
	}{{"changed:status changed:readiness action:work outcome:ok", true}, {"hook:notify", false}, {"changed:title", false}, {"action:other", false}, {"outcome:failed", false}} {
		if got := parse(t, tc.q, true).Match(item, s, event); got != tc.want {
			t.Fatal(tc.q, got)
		}
	}
	event.Type = "hook.finished"
	if !parse(t, "hook:notify outcome:ok", true).Match(item, s, event) {
		t.Fatal("hook")
	}
	event.Type = "item.updated"
	if parse(t, "outcome:ok", true).Match(item, s, event) {
		t.Fatal("non-finished outcome")
	}
	for _, outcome := range []string{"ok", "failed", "timeout", "refused"} {
		event.Type = "hook.finished"
		event.Outcome = outcome
		if !parse(t, "outcome:"+outcome, true).Match(item, s, event) {
			t.Fatal(outcome)
		}
	}
}

func ids(items []model.Item) []string {
	out := make([]string, len(items))
	for n, i := range items {
		out[n] = i.Ref.ID
	}
	return out
}

// Catches reversed defaults, lexicographic numeric IDs, and lost next-ranking
// tie breaks. Next comes from the real engine, not a duplicate ranking algorithm.
func TestSort(t *testing.T) {
	t.Parallel()
	a, s := fixture()
	a.Ref.ID = "BL-10"
	b := a
	b.Ref.ID = "BL-2"
	b.Priority = model.Urgent
	b.Rank = "b"
	b.Updated = b.Updated.Add(time.Hour)
	b.Created = b.Created.Add(time.Hour)
	b.Due = "2026-10-20"
	b.Title = "Alpha"
	b.Status = "todo"
	items := []model.Item{a, b}
	s.Next = []model.Ref{a.Ref, b.Ref}
	for _, tc := range []struct {
		key  string
		want []string
	}{{"next", []string{"BL-10", "BL-2"}}, {"priority", []string{"BL-2", "BL-10"}}, {"rank", []string{"BL-10", "BL-2"}}, {"updated", []string{"BL-2", "BL-10"}}, {"created", []string{"BL-10", "BL-2"}}, {"due", []string{"BL-2", "BL-10"}}, {"id", []string{"BL-2", "BL-10"}}, {"title", []string{"BL-2", "BL-10"}}, {"status", []string{"BL-2", "BL-10"}}} {
		for _, desc := range []bool{false, true} {
			copy := slices.Clone(items)
			qtext := "sort:"
			if desc {
				qtext += "-"
			}
			q := parse(t, qtext+tc.key, false)
			s.Sort(copy, q.Sort)
			want := slices.Clone(tc.want)
			if desc {
				slices.Reverse(want)
			}
			if !reflect.DeepEqual(ids(copy), want) {
				t.Fatalf("%s desc=%v: %v", tc.key, desc, ids(copy))
			}
		}
	}
	b.Priority = a.Priority
	items = []model.Item{b, a}
	s.Sort(items, []query.SortKey{{Field: "priority"}})
	if ids(items)[0] != "BL-10" {
		t.Fatal("next tie break")
	}
	s.Next = nil
	s.Sort(items, nil)
	if ids(items)[0] != "BL-2" {
		t.Fatal("ID fallback")
	}
	a.Rank = ""
	a.Due = ""
	for _, key := range []string{"rank", "due"} {
		items = []model.Item{a, b}
		s.Sort(items, []query.SortKey{{Field: key}})
		if ids(items)[0] != "BL-2" {
			t.Fatal("unset last", key)
		}
	}
	// Tie breaks follow the entire real next order, including impact and creation.
	a, s = fixture()
	b = a
	b.Ref.ID = "BL-2"
	b.Priority = model.Urgent
	s.Items = []model.Item{a, b}
	result := engine.Evaluate(engine.Input{Items: s.Items, Config: project.ReadyConfig{Statuses: []string{"active"}}})
	for _, e := range result.Items {
		s.Next = append(s.Next, e.Ref)
	}
	groups := parse(t, "sort:status", false).Project(s, nil)
	if ids(groups[0].Items)[0] != "BL-2" {
		t.Fatal("engine next tie-break")
	}
}

// Catches dropping multi-valued memberships and reordering declared milestones
// lexically instead of following the catalog, including unknown values.
func TestGroupsAndCompletion(t *testing.T) {
	t.Parallel()
	a, s := fixture()
	b := a
	b.Ref.ID = "BL-2"
	b.Status = "todo"
	b.Category = model.Todo
	b.Priority = model.Urgent
	b.Assignees = []string{"@amy"}
	b.Labels = []string{"tui", "other"}
	b.Milestone = "a"
	b.Type = "bug"
	b.Parent = nil
	b.Ref.Source = "remote"
	c := b
	c.Ref.ID = "BL-3"
	c.Milestone = "m-1"
	d := b
	d.Ref.ID = "BL-4"
	d.Milestone = "z"
	s.Items = []model.Item{a, b, c, d}
	for _, tc := range []struct {
		key  string
		want []string
	}{{"status", []string{"todo", "doing"}}, {"category", []string{"todo", "active"}}, {"priority", []string{"urgent", "high"}}, {"assignee", []string{"@amy", "@brett"}}, {"label", []string{"other", "perf", "tui"}}, {"milestone", []string{"m-2", "m-1", "a", "z"}}, {"type", []string{"bug", "feature"}}, {"parent", []string{"", "tasks:BL-3"}}, {"source", []string{"remote", "tasks"}}, {"none", []string{""}}} {
		groups := s.Group(s.Items, tc.key)
		values := make([]string, len(groups))
		for n, g := range groups {
			values[n] = g.Value
			if tc.key == "label" && g.Value == "tui" && len(g.Items) != 4 {
				t.Fatal("lost label membership")
			}
		}
		if !reflect.DeepEqual(values, tc.want) {
			t.Fatalf("%s: %v want %v", tc.key, values, tc.want)
		}
	}
	if got := s.Complete("milestone", "", query.Options{}); !reflect.DeepEqual(got, []query.Candidate{{Value: "m-2", Name: "Release two"}, {Value: "m-1", Name: "Release one"}, {Value: "a"}, {Value: "z"}}) {
		t.Fatal(got)
	}
	got := s.Complete("label", "", query.Options{})
	var values []string
	for _, c := range got {
		values = append(values, c.Value)
	}
	if !reflect.DeepEqual(values, []string{"declared", "perf", "other", "tui"}) {
		t.Fatal(values)
	}
	if got := s.Complete("milestone", "Release", query.Options{}); len(got) != 2 {
		t.Fatal(got)
	}
	if got := s.Complete("", "field.o", query.Options{}); !reflect.DeepEqual(got, []query.Candidate{{Value: "field.owner:"}}) {
		t.Fatal(got)
	}
	if got := s.Complete("", "cha", query.Options{}); len(got) != 0 {
		t.Fatal(got)
	}
	if got := s.Complete("", "cha", query.Options{Hook: true}); len(got) != 1 || got[0].Value != "changed:" {
		t.Fatal(got)
	}
	for _, field := range []string{"status", "assignee", "id", "title", "priority", "category", "is", "has", "sort", "group", "type", "source", "claimed", "outcome"} {
		if len(s.Complete(field, "", query.Options{Hook: true})) == 0 {
			t.Fatal(field)
		}
	}
	if got := s.Complete("status", "In Progress", query.Options{}); len(got) != 1 || got[0].Value != "doing" {
		t.Fatal(got)
	}
	// Filtering before sorting must preserve declared aliases, case folding,
	// deduplication and the declared-before-used ordering of completion results.
	for _, field := range []string{"", "status", "milestone", "label", "id", "title"} {
		for _, prefix := range []string{"", "B", "t", "PER", "Release", "In Progress", "field.o"} {
			all := s.Complete(field, "", query.Options{})
			want := slices.DeleteFunc(all, func(c query.Candidate) bool {
				return !strings.HasPrefix(strings.ToLower(c.Value), strings.ToLower(prefix)) && !strings.HasPrefix(strings.ToLower(c.Name), strings.ToLower(prefix))
			})
			got, err := s.CompleteContext(context.Background(), field, prefix, query.Options{})
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("%s %q: %v, %v; want %v", field, prefix, got, err, want)
			}
		}
	}
	// A superseded editor request must never publish partial completions.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, field := range []string{"", "id"} {
		if got, err := s.CompleteContext(ctx, field, "", query.Options{}); got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled %q: %v, %v", field, got, err)
		}
	}
}

func FuzzQuery(f *testing.F) {
	for _, s := range []string{`is:open label:tui,perf -label:wontfix priority:>=high assignee:me updated:<7d "detail pane"`, `x      lable:tui`, `text:"a\\b\"c"`, `changed:readiness`, `sort:-priority,id group:label`, "", `due:99999999999999999w`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		q, err := query.Parse(input, query.Options{Hook: true})
		if err != nil {
			var pe *query.Error
			if !errors.As(err, &pe) || pe.Pos < 0 || pe.Pos > len(input) {
				t.Fatalf("unpositioned: %v", err)
			}
			return
		}
		item, s := fixture()
		q.Validate(s)
		q.Match(item, s, nil)
		q.Project(s, nil)
		for _, term := range q.Terms {
			if term.Pos < 0 || term.Pos > len(input) {
				t.Fatal("position")
			}
			for _, v := range term.Values {
				if v.Text == "" {
					t.Fatal("empty value")
				}
			}
		}
	})
}
