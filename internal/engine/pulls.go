package engine

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/github"
)

// linkedPulls maps each github-pulls item to the items in its project, other
// than pull requests, that link it the way linked changes would (design.md
// D44). Every read surface hides these.
func linkedPulls(cfg project.Config, items []model.Item) map[model.Ref][]model.Ref {
	if !slices.ContainsFunc(cfg.Sources, func(s project.SourceConfig) bool { return s.Type == "github-pulls" }) {
		return nil
	}
	kinds := map[string]string{}
	for _, s := range cfg.Sources {
		kinds[s.ID] = s.Type
	}
	type group struct {
		items []changes.Item
		data  changes.Data
		pulls map[string]model.Ref
	}
	groups := map[string]*group{}
	for _, item := range items {
		key := cmp.Or(item.Project, cfg.Project.ID)
		g := groups[key]
		if g == nil {
			g = &group{pulls: map[string]model.Ref{}}
			groups[key] = g
		}
		switch kinds[item.Ref.Source] {
		case "github-pulls":
			pr := github.PullRequest(item)
			g.data.PullRequests = append(g.data.PullRequests, pr)
			g.pulls[pr.URL] = item.Ref
		case "github":
			node, _ := item.Fields["id"].(string)
			g.items = append(g.items, changes.Item{Ref: item.Ref, NodeID: node, Refs: item.Refs})
		default:
			g.items = append(g.items, changes.Item{Ref: item.Ref, Refs: item.Refs})
		}
	}
	out := map[model.Ref][]model.Ref{}
	for _, g := range groups {
		if len(g.pulls) == 0 {
			continue
		}
		for ref, cs := range changes.Link(g.items, g.data) {
			for _, c := range cs {
				if pull, ok := g.pulls[c.URL]; ok && c.Number > 0 && !slices.Contains(out[pull], ref) {
					out[pull] = append(out[pull], ref)
				}
			}
		}
	}
	for _, refs := range out {
		slices.SortFunc(refs, func(a, b model.Ref) int {
			return cmp.Or(strings.Compare(a.Source, b.Source), strings.Compare(a.ID, b.ID))
		})
	}
	return out
}

// hidePulls drops the github-pulls items another item links.
func hidePulls(cfg project.Config, items []model.Item) []model.Item {
	hidden := linkedPulls(cfg, items)
	if len(hidden) == 0 {
		return items
	}
	return slices.DeleteFunc(items, func(i model.Item) bool { return hidden[i.Ref] != nil })
}

// HiddenPull refuses item as not found when it is a pull request another item
// links, naming the linker, so show and next agree with ls (design.md D44).
// Writes and release still resolve it.
func HiddenPull(ctx context.Context, cfg project.Config, item model.Item) error {
	if !slices.ContainsFunc(cfg.Sources, func(s project.SourceConfig) bool { return s.ID == item.Ref.Source && s.Type == "github-pulls" }) {
		return nil
	}
	reads, err := LoadReads(ctx, cfg, false)
	if err != nil {
		return err
	}
	items := slices.DeleteFunc(slices.Clone(reads.Query.Items), func(i model.Item) bool { return i.Ref == item.Ref })
	linkers := linkedPulls(cfg, append(items, item))[item.Ref]
	if len(linkers) == 0 {
		return nil
	}
	names := make([]string, len(linkers))
	for n, ref := range linkers {
		names[n] = ref.Source + ":" + ref.ID
	}
	return source.Refuse("not-found", fmt.Sprintf("%s:%s is linked to %s", item.Ref.Source, item.Ref.ID, strings.Join(names, ", ")))
}
