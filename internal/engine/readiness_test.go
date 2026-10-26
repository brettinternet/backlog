package engine_test

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

func ref(id string) model.Ref   { return model.Ref{Source: "tasks", ID: id} }
func item(id string) model.Item { return model.Item{Ref: ref(id), Category: model.Todo} }
func input(items ...model.Item) engine.Input {
	return engine.Input{Items: items, Config: project.ReadyConfig{Statuses: []string{"todo"}, SatisfiedBy: []string{"done", "canceled"}, SkipContainers: true}, Me: []string{"@me"}}
}
func explanation(result engine.Result, id string) engine.Explanation {
	for _, e := range result.Items {
		if e.Ref == ref(id) {
			return e
		}
	}
	panic(id)
}

// Prevent unsafe next-item selection when blockers, ownership, or cached
// references are unresolved; a known blocker must not be masked by unknowns.
func TestReadiness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		change  func(*engine.Input)
		state   engine.Readiness
		reasons []string
	}{
		{"candidate", func(*engine.Input) {}, engine.Ready, []string{"candidate"}},
		{"status", func(in *engine.Input) { in.Items[0].Category = model.Active }, engine.Ineligible, []string{"status"}},
		{"archived", func(in *engine.Input) { in.Archived = map[model.Ref]bool{ref("BL-1"): true} }, engine.Ineligible, []string{"archived"}},
		{"missing", func(in *engine.Input) { in.Items[0].BlockedBy = []model.Ref{ref("BL-99")} }, engine.Unknown, []string{"blocker-unknown"}},
		{"stale", func(in *engine.Input) {
			in.Items[0].BlockedBy = []model.Ref{ref("BL-2")}
			in.Stale = map[model.Ref]bool{ref("BL-2"): true}
		}, engine.Unknown, []string{"blocker-unknown"}},
		{"blocker beats unknown", func(in *engine.Input) { in.Items[0].BlockedBy = []model.Ref{ref("BL-2"), ref("BL-99")} }, engine.Blocked, []string{"blocker", "blocker-unknown"}},
		{"done", func(in *engine.Input) {
			in.Items[0].BlockedBy = []model.Ref{ref("BL-2")}
			in.Items[1].Category = model.Done
		}, engine.Ready, []string{"candidate"}},
		{"canceled", func(in *engine.Input) {
			in.Items[0].BlockedBy = []model.Ref{ref("BL-2")}
			in.Items[1].Category = model.Canceled
		}, engine.Ready, []string{"candidate"}},
		{"custom satisfied", func(in *engine.Input) {
			in.Items[0].BlockedBy = []model.Ref{ref("BL-2")}
			in.Config.SatisfiedBy = []string{"todo"}
		}, engine.Ready, []string{"candidate"}},
		{"cross source resolved", func(in *engine.Input) {
			in.Items[1].Ref.Source = "remote"
			in.Items[1].Category = model.Done
			in.Items[0].BlockedBy = []model.Ref{in.Items[1].Ref}
		}, engine.Ready, []string{"candidate"}},
		{"cross source blocked", func(in *engine.Input) {
			in.Items[1].Ref.Source = "remote"
			in.Items[0].BlockedBy = []model.Ref{in.Items[1].Ref}
		}, engine.Blocked, []string{"blocker"}},
		{"cross source missing", func(in *engine.Input) { in.Items[0].BlockedBy = []model.Ref{{Source: "remote", ID: "BL-2"}} }, engine.Unknown, []string{"blocker-unknown"}},
		// Provider IDs from native files resolve exactly, never by native grammar.
		{"provider blocker", func(in *engine.Input) {
			in.Items = append(in.Items, model.Item{Ref: model.Ref{Source: "issues", ID: "128"}, Category: model.Active})
			in.Items[0].BlockedBy = []model.Ref{{Source: "issues", ID: "128"}}
		}, engine.Blocked, []string{"blocker"}},
		{"provider blocker unloaded", func(in *engine.Input) { in.Items[0].BlockedBy = []model.Ref{{Source: "jira", ID: "10001"}} }, engine.Unknown, []string{"blocker-unknown"}},
		{"local padded", func(in *engine.Input) { in.Items[0].BlockedBy = []model.Ref{{ID: "BL-002"}} }, engine.Blocked, []string{"blocker"}},
		{"ambiguous padded", func(in *engine.Input) {
			in.Items = append(in.Items, item("BL-002"))
			in.Items[0].BlockedBy = []model.Ref{ref("BL-2")}
		}, engine.Unknown, []string{"blocker-unknown"}},
		{"container", func(in *engine.Input) { r := ref("BL-1"); in.Items[1].Parent = &r }, engine.Ineligible, []string{"container"}},
		{"explicit children", func(in *engine.Input) { in.Items[0].Children = []model.Ref{ref("BL-2")} }, engine.Ineligible, []string{"container"}},
		{"missing child", func(in *engine.Input) { in.Items[0].Children = []model.Ref{{ID: "BL-99"}} }, engine.Ineligible, []string{"container"}},
		{"closed child", func(in *engine.Input) {
			in.Items[0].Children = []model.Ref{ref("BL-2")}
			in.Items[1].Category = model.Done
		}, engine.Ready, []string{"candidate"}},
		{"containers allowed", func(in *engine.Input) {
			in.Items[0].Children = []model.Ref{ref("BL-2")}
			in.Config.SkipContainers = false
		}, engine.Ready, []string{"candidate"}},
		{"excluded label", func(in *engine.Input) {
			in.Items[0].Labels = []string{"human"}
			in.Config.ExcludeLabels = []string{"human"}
		}, engine.Ineligible, []string{"excluded-label"}},
		{"excluded label and blocker", func(in *engine.Input) {
			in.Items[0].Labels = []string{"human"}
			in.Config.ExcludeLabels = []string{"human"}
			in.Items[0].BlockedBy = []model.Ref{ref("BL-2")}
		}, engine.Ineligible, []string{"blocker", "excluded-label"}},
		{"outside sprint", func(in *engine.Input) {
			in.Config.Sprint = "current"
			in.Sprints = []model.Sprint{{Key: "s1", State: model.SprintActive}, {Key: "s2", State: model.SprintFuture}}
			in.Items[0].Sprint = "s2"
		}, engine.Ineligible, []string{"sprint"}},
		{"in sprint", func(in *engine.Input) {
			in.Config.Sprint = "current"
			in.Sprints = []model.Sprint{{Key: "s1", State: model.SprintActive}}
			in.Items[0].Sprint = "s1"
		}, engine.Ready, []string{"candidate"}},
		{"assigned others", func(in *engine.Input) { in.Items[0].Assignees = []string{"@other"} }, engine.Waiting, []string{"assignee"}},
		{"assigned me", func(in *engine.Input) { in.Items[0].Assignees = []string{"@me"} }, engine.Ready, []string{"candidate"}},
		{"assigned both", func(in *engine.Input) { in.Items[0].Assignees = []string{"@other", "@me"} }, engine.Ready, []string{"candidate"}},
		{"claim others", func(in *engine.Input) {
			in.Coordination = true
			in.Claims = map[model.Ref]engine.Claim{ref("BL-1"): engine.ClaimOthers}
		}, engine.Waiting, []string{"claimed"}},
		{"claim mine", func(in *engine.Input) {
			in.Coordination = true
			in.Claims = map[model.Ref]engine.Claim{ref("BL-1"): engine.ClaimMine}
		}, engine.Ready, []string{"candidate"}},
		{"claim free", func(in *engine.Input) {
			in.Coordination = true
			in.Claims = map[model.Ref]engine.Claim{ref("BL-1"): engine.ClaimNone}
		}, engine.Ready, []string{"candidate"}},
		{"claim unknown", func(in *engine.Input) {
			in.Coordination = true
			in.Claims = map[model.Ref]engine.Claim{ref("BL-1"): engine.ClaimUnknown}
		}, engine.Unknown, []string{"claim-unknown"}},
		{"claim absent", func(in *engine.Input) { in.Coordination = true }, engine.Unknown, []string{"claim-unknown"}},
		{"coordination disabled", func(in *engine.Input) { in.Claims = map[model.Ref]engine.Claim{ref("BL-1"): engine.ClaimOthers} }, engine.Ready, []string{"candidate"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := input(item("BL-1"), item("BL-2"))
			tc.change(&in)
			got := explanation(engine.Evaluate(in), "BL-1")
			var codes []string
			for _, r := range got.Reasons {
				codes = append(codes, r.Code)
			}
			if got.Readiness != tc.state || !reflect.DeepEqual(codes, tc.reasons) {
				t.Fatalf("got %s %v; want %s %v", got.Readiness, codes, tc.state, tc.reasons)
			}
			for _, r := range got.Reasons {
				if slices.ContainsFunc(r.Refs, func(x model.Ref) bool { return x.Source == "" }) {
					t.Fatalf("reason %s has unqualified refs: %v", r.Code, r.Refs)
				}
			}
		})
	}
}

// Catch ordering regressions at each level, including natural IDs, fractional
// ranks, timestamp order, and deterministic source/padded-ID fallbacks.
func TestRanking(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*model.Item, *model.Item)
		want   string
	}{
		{"priority", func(a, b *model.Item) { a.Priority = model.Low; b.Priority = model.Urgent }, "BL-10"},
		{"unset priority", func(a, b *model.Item) { b.Priority = model.Low }, "BL-10"},
		{"rank", func(a, b *model.Item) { a.Rank = "a4"; b.Rank = "a3" }, "BL-10"},
		{"ranked first", func(a, b *model.Item) { b.Rank = "a3" }, "BL-10"},
		{"created", func(a, b *model.Item) { a.Created = time.Unix(2, 0); b.Created = time.Unix(1, 0) }, "BL-10"},
		{"natural", func(a, b *model.Item) {}, "BL-9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, b := item("BL-9"), item("BL-10")
			tc.change(&a, &b)
			in := input(a, b)
			got := engine.Evaluate(in)
			if got.Candidates[0].ID != tc.want {
				t.Fatalf("%v", got.Candidates)
			}
			slices.Reverse(in.Items)
			if !reflect.DeepEqual(got, engine.Evaluate(in)) {
				t.Fatal("input order changed result")
			}
		})
	}
	a, b, c := item("BL-9"), item("BL-10"), item("BL-11")
	c.BlockedBy = []model.Ref{b.Ref}
	in := input(a, b, c, item("BL-007"), item("BL-7"))
	other := item("BL-7")
	other.Ref.Source = "remote"
	in.Items = append(in.Items, other)
	want := engine.Evaluate(in)
	if want.Candidates[0] != b.Ref {
		t.Fatalf("impact tie-break: %v", want.Candidates)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 100 {
		rng.Shuffle(len(in.Items), func(i, j int) { in.Items[i], in.Items[j] = in.Items[j], in.Items[i] })
		if !reflect.DeepEqual(want, engine.Evaluate(in)) {
			t.Fatal("shuffled ranking changed")
		}
	}
}

// Shared dependents must not inflate impact, cycles must terminate, and cycle
// reporting must exclude a dependent that is not itself in the cycle.
func TestImpactAndCycles(t *testing.T) {
	t.Parallel()
	a, b, c, d, e := item("BL-1"), item("BL-2"), item("BL-3"), item("BL-4"), item("BL-5")
	b.BlockedBy = []model.Ref{a.Ref}
	c.BlockedBy = []model.Ref{a.Ref}
	d.BlockedBy = []model.Ref{b.Ref, c.Ref}
	e.Category = model.Done
	e.BlockedBy = []model.Ref{a.Ref}
	got := engine.Evaluate(input(a, b, c, d, e))
	x := explanation(got, "BL-1")
	if x.UnblockImpact != 3 || !reflect.DeepEqual(x.Unblocks, []model.Ref{b.Ref, c.Ref}) {
		t.Fatalf("diamond: %+v", x)
	}
	a.BlockedBy = []model.Ref{b.Ref}
	got = engine.Evaluate(input(a, b, c, d, e))
	if explanation(got, "BL-1").UnblockImpact != 3 || !reflect.DeepEqual(got.Cycles, [][]model.Ref{{a.Ref, b.Ref}}) {
		t.Fatalf("cycle: %+v", got)
	}
	a.BlockedBy = []model.Ref{a.Ref}
	got = engine.Evaluate(input(a))
	if explanation(got, "BL-1").UnblockImpact != 0 || !reflect.DeepEqual(got.Cycles, [][]model.Ref{{a.Ref}}) {
		t.Fatalf("self cycle: %+v", got)
	}
}
func TestExplanationText(t *testing.T) {
	t.Parallel()
	a := item("BL-1")
	a.Priority = model.High
	a.Rank = "a3"
	in := input(a)
	for _, id := range []string{"BL-9", "BL-12", "BL-15", "BL-20"} {
		b := item(id)
		b.BlockedBy = []model.Ref{a.Ref}
		in.Items = append(in.Items, b)
	}
	e := explanation(engine.Evaluate(in), "BL-1")
	if got := e.Text(); got != "high · unblocks 4 (BL-9, BL-12, +2)" {
		t.Fatal(got)
	}
	in.Items[0].BlockedBy = []model.Ref{ref("BL-99")}
	if got := explanation(engine.Evaluate(in), "BL-1").Text(); got != "high · unblocks 4 (BL-9, BL-12, +2) · blocker-unknown: BL-99" {
		t.Fatal(got)
	}
}

// Catches a sprint write landing in the wrong sprint: roles must follow the
// catalog, names must not shadow keys, and closed or ambiguous targets refuse.
func TestResolveSprint(t *testing.T) {
	catalog := []model.Sprint{
		{Key: "s1", Name: "Sprint 1", State: model.SprintClosed},
		{Key: "s2", Name: "Sprint 2", State: model.SprintActive},
		{Key: "s3", Name: "s2", State: model.SprintFuture},
		{Key: "s4", Name: "Later", State: model.SprintFuture},
		{Key: "s5", Name: "later", State: model.SprintFuture},
	}
	for value, want := range map[string]string{"current": "s2", "Next": "s3", "none": "", "": "", "s2": "s2", "sprint 2": "s2", "s3": "s3"} {
		if got, err := engine.ResolveSprint(catalog, value); err != nil || got != want {
			t.Errorf("%q: got %q %v, want %q", value, got, err, want)
		}
	}
	for _, value := range []string{"Sprint 1", "later", "missing"} {
		if got, err := engine.ResolveSprint(catalog, value); err == nil {
			t.Errorf("%q resolved to %q", value, got)
		}
	}
}
