// Package engine computes source-independent projections over loaded items.
package engine

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

type Readiness string

const (
	Ready      Readiness = "ready"
	Blocked    Readiness = "blocked"
	Waiting    Readiness = "waiting"
	Unknown    Readiness = "unknown"
	Ineligible Readiness = "ineligible"
)

type Claim string

const (
	ClaimNone    Claim = "none"
	ClaimMine    Claim = "mine"
	ClaimOthers  Claim = "others"
	ClaimUnknown Claim = "unknown"
)

// Input contains a complete loaded snapshot and resolved configuration. Missing
// targets are unknown even when a source's load was otherwise complete. Stale
// marks individual cached targets whose freshness cannot be established.
// No input maps or items are modified. Refs with an empty source are local to
// the referring item. Claims are ignored unless Coordination is enabled.
type Input struct {
	Projects        map[string]project.Config // Per-item policy in a composed workspace.
	Items           []model.Item
	Config          project.ReadyConfig
	Me              []string
	Archived, Stale map[model.Ref]bool
	Coordination    bool
	Claims          map[model.Ref]Claim
}
type Reason struct {
	Code   string      `json:"code"`
	Refs   []model.Ref `json:"refs,omitempty"`
	Values []string    `json:"values,omitempty"`
}
type Explanation struct {
	Ref           model.Ref      `json:"ref"`
	Readiness     Readiness      `json:"readiness"`
	Reasons       []Reason       `json:"reasons"`
	Priority      model.Priority `json:"priority"`
	Rank          string         `json:"rank,omitempty"`
	UnblockImpact int            `json:"unblockImpact"`
	Unblocks      []model.Ref    `json:"unblocks,omitempty"` // first two in natural order
}
type Result struct {
	Items      []Explanation `json:"items"`            // all items in ranking order
	Candidates []model.Ref   `json:"candidates"`       // ready items in the same order
	Cycles     [][]model.Ref `json:"cycles,omitempty"` // blocked-by strongly connected components
}

// Evaluate computes readiness and a total order, including excluded items.
// Impact walks only open dependents, with a reusable visited array; it stores
// counts and a bounded preview, not a quadratic transitive-closure matrix.
func Evaluate(in Input) Result {
	out, deps, reverse := assess(in)
	return rank(in, out, deps, reverse)
}

func assess(in Input) (Result, [][]int, [][]int) {
	n := len(in.Items)
	index := make(map[model.Ref]int, n)
	for i, item := range in.Items {
		key := canonical(item.Ref)
		if _, exists := index[key]; exists {
			index[key] = -1
		} else {
			index[key] = i
		}
	}
	// resolve qualifies a source-local ref and returns its index, or -1 when
	// it is missing or ambiguous.
	resolve := func(ref model.Ref, owner model.Ref) (model.Ref, int) {
		if ref.Source == "" {
			ref.Source = owner.Source
		}
		if i, ok := index[canonical(ref)]; ok {
			return ref, i
		}
		return ref, -1
	}
	deps, reverse, children := make([][]int, n), make([][]int, n), make([][]int, n)
	missing, missingChildren := make([][]model.Ref, n), make([][]model.Ref, n)
	for i, item := range in.Items {
		for _, ref := range item.BlockedBy {
			ref, j := resolve(ref, item.Ref)
			if j < 0 {
				missing[i] = append(missing[i], ref)
				continue
			}
			deps[i] = append(deps[i], j)
			reverse[j] = append(reverse[j], i)
		}
		for _, ref := range item.Children {
			if ref, j := resolve(ref, item.Ref); j < 0 {
				missingChildren[i] = append(missingChildren[i], ref)
			} else {
				children[i] = append(children[i], j)
			}
		}
		if item.Parent != nil {
			if _, j := resolve(*item.Parent, item.Ref); j >= 0 {
				children[j] = append(children[j], i)
			}
		}
	}
	out := Result{Items: make([]Explanation, n), Candidates: []model.Ref{}}
	for i, item := range in.Items {
		config, me, coordination := in.Config, in.Me, in.Coordination
		if p, ok := in.Projects[item.Project]; ok {
			config, me, coordination = p.Ready, []string{p.Me.Default}, p.Coordination.Worklease != nil
		}
		e := Explanation{Ref: item.Ref, Readiness: Ready, Reasons: []Reason{}, Priority: item.Priority, Rank: item.Rank}
		add := func(code string, refs []model.Ref, values []string) {
			slices.SortFunc(refs, compareRef)
			refs = slices.Compact(refs)
			slices.Sort(values)
			values = slices.Compact(values)
			e.Reasons = append(e.Reasons, Reason{code, refs, values})
		}
		if in.Stale[item.Ref] {
			add("stale", nil, nil)
		}
		if in.Archived[item.Ref] {
			add("archived", nil, nil)
		}
		if !slices.Contains(config.Statuses, string(item.Category)) {
			add("status", nil, []string{string(item.Category)})
		}
		if config.SkipContainers {
			open := slices.Clone(missingChildren[i])
			for _, j := range children[i] {
				if !closed(in.Items[j]) && !in.Archived[in.Items[j].Ref] {
					open = append(open, in.Items[j].Ref)
				}
			}
			if len(open) > 0 {
				add("container", open, nil)
			}
		}
		var blockers, unknown []model.Ref
		unknown = append(unknown, missing[i]...)
		for _, j := range deps[i] {
			target := in.Items[j]
			if in.Stale[target.Ref] {
				unknown = append(unknown, target.Ref)
			} else if !slices.Contains(config.SatisfiedBy, string(target.Category)) {
				blockers = append(blockers, target.Ref)
			}
		}
		if len(blockers) > 0 {
			add("blocker", blockers, nil)
		}
		var labels []string
		for _, label := range item.Labels {
			if slices.Contains(config.ExcludeLabels, label) {
				labels = append(labels, label)
			}
		}
		if len(labels) > 0 {
			add("excluded-label", nil, labels)
		}
		if len(item.Assignees) > 0 {
			mine := false
			for _, assignee := range item.Assignees {
				mine = mine || slices.Contains(me, assignee)
			}
			if !mine {
				add("assignee", nil, slices.Clone(item.Assignees))
			}
		}
		if coordination {
			switch in.Claims[item.Ref] {
			case ClaimMine, ClaimNone: // owned by this caller or known free
			case ClaimOthers:
				add("claimed", nil, nil)
			default:
				add("claim-unknown", nil, nil)
			}
		}
		if len(unknown) > 0 {
			add("blocker-unknown", unknown, nil)
		}
		// Precedence keeps known unsatisfied blockers dominant over unknown edges.
		for _, reason := range e.Reasons {
			state := Unknown
			switch reason.Code {
			case "archived", "status", "container":
				state = Ineligible
			case "blocker", "excluded-label":
				state = Blocked
			case "assignee", "claimed":
				state = Waiting
			}
			if severity(state) > severity(e.Readiness) {
				e.Readiness = state
			}
		}
		if e.Readiness == Ready {
			add("candidate", nil, nil)
		}
		out.Items[i] = e
	}
	return out, deps, reverse
}

func rank(in Input, out Result, deps, reverse [][]int) Result {
	n := len(in.Items)
	created := make(map[model.Ref]int, n)
	for i, item := range in.Items {
		created[item.Ref] = i
	}
	seen := make([]int, n)
	stack := make([]int, 0, n)
	for i := range in.Items {
		generation := i + 1
		seen[i] = generation
		stack = append(stack[:0], reverse[i]...)
		var first []model.Ref
		for len(stack) > 0 {
			j := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[j] == generation {
				continue
			}
			seen[j] = generation
			if closed(in.Items[j]) || in.Archived[in.Items[j].Ref] {
				continue
			}
			out.Items[i].UnblockImpact++
			first = append(first, in.Items[j].Ref)
			slices.SortFunc(first, compareRef)
			if len(first) > 2 {
				first = first[:2]
			}
			stack = append(stack, reverse[j]...)
		}
		out.Items[i].Unblocks = slices.Clone(first)
	}
	out.Cycles = cycles(in.Items, deps)
	slices.SortFunc(out.Items, func(a, b Explanation) int {
		if c := cmp.Compare(priority(a.Priority), priority(b.Priority)); c != 0 {
			return c
		}
		if a.Rank != b.Rank {
			if a.Rank == "" {
				return 1
			}
			if b.Rank == "" {
				return -1
			}
			return strings.Compare(a.Rank, b.Rank)
		}
		if c := cmp.Compare(b.UnblockImpact, a.UnblockImpact); c != 0 {
			return c
		}
		ai, bi := created[a.Ref], created[b.Ref]
		if c := in.Items[ai].Created.Compare(in.Items[bi].Created); c != 0 {
			return c
		}
		return compareRef(a.Ref, b.Ref)
	})
	for _, e := range out.Items {
		if e.Readiness == Ready {
			out.Candidates = append(out.Candidates, e.Ref)
		}
	}
	return out
}
func closed(item model.Item) bool {
	return item.Category == model.Done || item.Category == model.Canceled
}
func severity(s Readiness) int {
	switch s {
	case Ineligible:
		return 4
	case Blocked:
		return 3
	case Waiting:
		return 2
	case Unknown:
		return 1
	}
	return 0
}
func priority(p model.Priority) int {
	switch p {
	case model.Urgent:
		return 0
	case model.High:
		return 1
	case model.Medium:
		return 2
	case model.Low:
		return 3
	}
	return 4
}

// Text is the compact ranking explanation. Exclusion reasons remain available
// in the structured explanation and are appended for non-candidates.
func (e Explanation) Text() string {
	p := string(e.Priority)
	if p == "" {
		p = "unset"
	}
	parts := []string{p}
	if e.Rank != "" {
		parts = append(parts, "rank "+e.Rank)
	}
	impact := fmt.Sprintf("unblocks %d", e.UnblockImpact)
	if len(e.Unblocks) > 0 {
		refs := make([]string, 0, 3)
		for _, ref := range e.Unblocks {
			refs = append(refs, ref.ID)
		}
		if e.UnblockImpact > len(refs) {
			refs = append(refs, fmt.Sprintf("+%d", e.UnblockImpact-len(refs)))
		}
		impact += " (" + strings.Join(refs, ", ") + ")"
	}
	parts = append(parts, impact)
	if e.Readiness != Ready {
		codes := make([]string, len(e.Reasons))
		for i, r := range e.Reasons {
			codes[i] = r.Code
		}
		parts = append(parts, string(e.Readiness)+": "+strings.Join(codes, ", "))
	}
	return strings.Join(parts, " · ")
}

// canonical preserves source identity while treating padded numeric segments as
// equal for lookup. Natural comparison avoids integer overflow for imported IDs.
func canonical(ref model.Ref) model.Ref {
	var b strings.Builder
	for i := 0; i < len(ref.ID); {
		if ref.ID[i] < '0' || ref.ID[i] > '9' {
			b.WriteByte(ref.ID[i])
			i++
			continue
		}
		j := i
		for j < len(ref.ID) && ref.ID[j] >= '0' && ref.ID[j] <= '9' {
			j++
		}
		digits := strings.TrimLeft(ref.ID[i:j], "0")
		if digits == "" {
			digits = "0"
		}
		b.WriteString(digits)
		i = j
	}
	ref.ID = b.String()
	return ref
}
func compareRef(a, b model.Ref) int {
	x, y := canonical(a).ID, canonical(b).ID
	for i, j := 0, 0; i < len(x) || j < len(y); {
		if i == len(x) {
			return -1
		}
		if j == len(y) {
			return 1
		}
		if x[i] >= '0' && x[i] <= '9' && y[j] >= '0' && y[j] <= '9' {
			ii, jj := i, j
			for ii < len(x) && x[ii] >= '0' && x[ii] <= '9' {
				ii++
			}
			for jj < len(y) && y[jj] >= '0' && y[jj] <= '9' {
				jj++
			}
			if c := cmp.Compare(ii-i, jj-j); c != 0 {
				return c
			}
			if c := strings.Compare(x[i:ii], y[j:jj]); c != 0 {
				return c
			}
			i, j = ii, jj
		} else {
			if c := cmp.Compare(x[i], y[j]); c != 0 {
				return c
			}
			i++
			j++
		}
	}
	if c := strings.Compare(a.Source, b.Source); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// Tarjan's algorithm reports actual cycle members, not downstream victims.
func cycles(items []model.Item, deps [][]int) [][]model.Ref {
	n := len(items)
	number, low := make([]int, n), make([]int, n)
	active := make([]bool, n)
	stack := []int{}
	next := 0
	var result [][]model.Ref
	var visit func(int)
	visit = func(v int) {
		next++
		number[v], low[v] = next, next
		stack = append(stack, v)
		active[v] = true
		for _, w := range deps[v] {
			if number[w] == 0 {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if active[w] {
				low[v] = min(low[v], number[w])
			}
		}
		if low[v] != number[v] {
			return
		}
		var component []model.Ref
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			active[w] = false
			component = append(component, items[w].Ref)
			if w == v {
				break
			}
		}
		if len(component) > 1 || slices.Contains(deps[v], v) {
			slices.SortFunc(component, compareRef)
			result = append(result, component)
		}
	}
	for v := range items {
		if number[v] == 0 {
			visit(v)
		}
	}
	slices.SortFunc(result, func(a, b []model.Ref) int { return compareRef(a[0], b[0]) })
	return result
}
