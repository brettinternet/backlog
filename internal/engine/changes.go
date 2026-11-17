package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source/github"
	"github.com/brettinternet/backlog/internal/source/gitlab"
)

// ChangesOptions are each project's change providers, keyed by project ID
// (design.md D43). A workspace has one per member project.
func ChangesOptions(cfg project.Config) map[string]changes.Options {
	projects := []project.Config{cfg}
	if cfg.Workspace != nil {
		projects = cfg.Workspace.Projects
	}
	out := map[string]changes.Options{}
	for _, p := range projects {
		out[p.Project.ID] = changes.Options{Discovery: p.Discovery, CacheDir: p.Paths.CacheDir, StateDir: p.Paths.StateDir, Trailer: p.History.CommitTrailer, Git: p.Changes.Git, Forges: forges(p)}
	}
	return out
}

// TrackChanges keeps cfg's changes current off the UI loop, or returns nil
// when no project reads any.
func TrackChanges(ctx context.Context, cfg project.Config) *changes.Tracker {
	return changes.Track(ctx, ChangesOptions(cfg))
}

// forges are p's resolved forges, each that couldn't be built reporting why
// as its refresh failure.
func forges(p project.Config) []changes.Forge {
	resolved, _ := ResolveForges(p)
	var out []changes.Forge
	for _, f := range resolved {
		if f.Err != nil {
			out = append(out, failedForge{f.Type + " " + f.Repo, f.Err})
			continue
		}
		out = append(out, f.Forge)
	}
	return out
}

// ResolvedForge is one of a project's forges: Forge when it could be built,
// else Err. Explicit ones come from changes.forges, the other from origin;
// Untrusted ones wait for bl trust. Cache is its pull request cache, empty
// without one.
type ResolvedForge struct {
	Type, Host, Repo, Account string
	Explicit, Untrusted       bool
	Cache                     string
	Forge                     changes.Forge
	Err                       error
}

// ForgeGap is why a GitHub or GitLab origin gave no forge: Accounts are the
// several accounts of Type for Host, or none.
type ForgeGap struct {
	Type, Host, Repo string
	Accounts         []string
}

// ResolveForges resolves p's configured forges, which need trust like
// sources, else the GitHub or GitLab origin when exactly one account of that
// type is configured for its host. Without either, gap says why an origin
// that looks like GitHub or GitLab gave none; it is nil for any other origin.
func ResolveForges(p project.Config) (out []ResolvedForge, gap *ForgeGap) {
	configs := p.Changes.Forges
	explicit := len(configs) > 0
	if !explicit {
		host, repo := project.Origin(p.Discovery)
		if host == "" {
			return nil, nil
		}
		for _, kind := range []string{"github", "gitlab"} {
			found := p.HostAccounts(kind, host)
			if len(found) == 1 {
				configs = append(configs, project.ForgeConfig{Type: kind, Host: host, Repo: repo})
			} else if len(found) > 1 && gap == nil {
				gap = &ForgeGap{Type: kind, Host: host, Repo: repo}
				for _, a := range found {
					gap.Accounts = append(gap.Accounts, cmp.Or(a.Name, a.User))
				}
			}
		}
		if len(configs) != 1 {
			if gap == nil && len(configs) == 0 {
				if kind := map[string]string{"github.com": "github", "gitlab.com": "gitlab"}[host]; kind != "" {
					gap = &ForgeGap{Type: kind, Host: host, Repo: repo}
				}
			}
			return nil, gap
		}
	}
	for i, f := range configs {
		if f.Host == "" {
			f.Host = map[string]string{"github": "github.com", "gitlab": "gitlab.com"}[f.Type]
		}
		r := ResolvedForge{Type: f.Type, Host: f.Host, Repo: f.Repo, Account: f.Account, Explicit: explicit}
		if explicit {
			r.Err = p.RequireTrusted(fmt.Sprintf("changes.forges[%d]", i))
			r.Untrusted = r.Err != nil
		}
		if r.Err == nil {
			var account project.AccountConfig
			if account, r.Err = p.ForgeAccount(f); r.Err == nil {
				r.Account, r.Cache = cmp.Or(account.Name, account.User), forgePath(p, f, account)
				r.Forge, r.Err = newForge(p, f)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func newForge(p project.Config, f project.ForgeConfig) (changes.Forge, error) {
	if f.Type == "gitlab" {
		account, err := p.ForgeAccount(f)
		if err != nil {
			return nil, err
		}
		return gitlab.NewForge(gitlab.Options{Host: f.Host, Repo: f.Repo, Account: account, Secrets: providerSecrets, Credentials: providerCredentialsFor(p)}, forgePath(p, f, account))
	}
	return githubForge(p, f)
}

// githubForges share one forge per cache file in the process, so the changes
// tracker and a github-pulls source refresh it under one lock (design.md D44).
var githubForges sync.Map

func githubForge(p project.Config, f project.ForgeConfig) (*github.Forge, error) {
	account, err := p.ForgeAccount(f)
	if err != nil {
		return nil, err
	}
	path := forgePath(p, f, account)
	if forge, ok := githubForges.Load(path); ok && path != "" {
		return forge.(*github.Forge), nil
	}
	forge, err := github.NewForge(github.Options{ID: "forge:" + f.Host + "/" + f.Repo, Host: f.Host, Repo: f.Repo, Account: account, Secrets: providerSecrets, Credentials: providerCredentialsFor(p)}, path)
	if err != nil || path == "" {
		return forge, err
	}
	shared, _ := githubForges.LoadOrStore(path, forge)
	return shared.(*github.Forge), nil
}

// forgePath is f's pull request cache, the same for every account name that
// resolves to one user.
func forgePath(p project.Config, f project.ForgeConfig, account project.AccountConfig) string {
	if p.Paths.CacheDir == "" {
		return ""
	}
	// GitHub repository names ignore case, so a source and origin spelled
	// differently share one cache.
	f.Account = ""
	if f.Type == "github" {
		f.Repo = strings.ToLower(f.Repo)
	}
	identity, _ := json.Marshal(struct {
		Forge project.ForgeConfig
		User  string
	}{f, account.User})
	return strings.TrimSuffix(providerCache(p, "forge:"+f.Host+"/"+f.Repo, string(identity), account).Path, ".sqlite") + ".pulls.json"
}

// failedForge reports why a forge couldn't be built as its refresh failure.
type failedForge struct {
	name string
	err  error
}

func (f failedForge) Name() string { return f.name }
func (f failedForge) Web() string  { return "" }
func (f failedForge) PullRequests(context.Context) ([]changes.PullRequest, error) {
	return nil, f.err
}
func (f failedForge) Pull(context.Context, int) (changes.Detail, error) {
	return changes.Detail{}, f.err
}
func (f failedForge) Review(context.Context, int, string, bool) (changes.Review, bool, error) {
	return changes.Review{}, false, f.err
}

// LinkChanges links items to state's changes, each item only within its own
// project. Workspace items carry their project; others belong to cfg's.
func LinkChanges(cfg project.Config, items []model.Item, state *changes.State) map[model.Ref][]changes.Change {
	if state == nil {
		return nil
	}
	byProject := map[string][]changes.Item{}
	for _, item := range items {
		key := item.Project
		if key == "" {
			key = cfg.Project.ID
		}
		node, _ := item.Fields["id"].(string)
		if !githubItem(cfg, item.Ref.Source) {
			node = ""
		}
		byProject[key] = append(byProject[key], changes.Item{Ref: item.Ref, NodeID: node, Refs: item.Refs})
	}
	out := map[model.Ref][]changes.Change{}
	for key, d := range state.Projects {
		for ref, cs := range changes.Link(byProject[key], d) {
			out[ref] = cs
		}
	}
	return out
}

func githubItem(cfg project.Config, source string) bool {
	for _, s := range cfg.Sources {
		if s.ID == source {
			return s.Type == "github"
		}
	}
	return false
}

// ChangesResult is one item's changes, furthest first, and how each provider
// fared, as bl changes and the MCP changes tool return them.
type ChangesResult struct {
	Changes   []changes.Change `json:"changes"`
	Providers []changes.Status `json:"providers"`
}

// ItemChanges loads, links, and enriches one item's changes once, for bl
// changes and MCP. Every item is read so an ID that names several reads as
// ambiguous, as it does in the TUI.
func ItemChanges(ctx context.Context, cfg project.Config, ref model.Ref) (ChangesResult, error) {
	return loadItemChanges(ctx, cfg, ref, false)
}

// ItemChangesDetail is ItemChanges with each open pull request's verdict,
// reviewers, check timing, and the first failing runs' steps, annotations,
// and log excerpts, for bl changes --detail and MCP detail.
func ItemChangesDetail(ctx context.Context, cfg project.Config, ref model.Ref) (ChangesResult, error) {
	return loadItemChanges(ctx, cfg, ref, true)
}

func loadItemChanges(ctx context.Context, cfg project.Config, ref model.Ref, detail bool) (ChangesResult, error) {
	reads, err := LoadReads(ctx, cfg, false)
	if err != nil {
		return ChangesResult{}, err
	}
	key := cfg.Project.ID
	for _, item := range reads.Query.Items {
		if item.Ref == ref && item.Project != "" {
			key = item.Project
		}
	}
	o, ok := ChangesOptions(cfg)[key]
	if !ok {
		return ChangesResult{}, fmt.Errorf("no project %s", key)
	}
	d := changes.Load(ctx, o)
	linked := LinkChanges(cfg, reads.Query.Items, &changes.State{Projects: map[string]changes.Data{key: d}})
	result := ChangesResult{Changes: changes.Enrich(ctx, linked[ref], o.Forges, false), Providers: d.Statuses}
	if result.Changes == nil {
		result.Changes = []changes.Change{}
	}
	if detail {
		changes.AddFailures(ctx, result.Changes, o.Forges)
	}
	for i, c := range result.Changes {
		if !detail {
			result.Changes[i] = c.Brief()
		}
		// A tour that can't be read is left out; bl tour ID reports why.
		if stored, ok, err := LoadTour(cfg, ref, c.ID); err == nil && ok {
			result.Changes[i].Tour = &changes.Tour{Ref: stored.Tour.Ref, Steps: len(stored.Tour.Steps), Stale: stored.Tour.Ref != c.Head}
		}
	}
	if result.Providers == nil {
		result.Providers = []changes.Status{}
	}
	return result, nil
}

// LinkedChanges loads ref's changes for history.Load.
func LinkedChanges(cfg project.Config, ref model.Ref) func(context.Context) ([]changes.Change, error) {
	return func(ctx context.Context) ([]changes.Change, error) {
		result, err := ItemChanges(ctx, cfg, ref)
		return result.Changes, err
	}
}

// MentionsChanges reports whether text filters by linked changes, so reads
// load them only then. Unparsable text mentions nothing.
func MentionsChanges(text string) bool {
	q, err := query.Parse(text, query.Options{})
	if err != nil {
		return false
	}
	for _, t := range q.Terms {
		switch t.Field {
		case "change", "checks", "review", "merge":
			return true
		case "has":
			for _, v := range t.Values {
				if strings.EqualFold(v.Text, "change") || strings.EqualFold(v.Text, "pr") {
					return true
				}
			}
		}
	}
	return false
}

// linkReads loads every project's changes once and sets each item's query
// state from them.
func linkReads(ctx context.Context, cfg project.Config, s *ReadSnapshot) {
	state := &changes.State{Projects: map[string]changes.Data{}}
	for key, o := range ChangesOptions(cfg) {
		state.Projects[key] = changes.Load(ctx, o)
	}
	for ref, cs := range LinkChanges(cfg, s.Query.Items, state) {
		if st, ok := s.Query.States[ref]; ok {
			st.Changes = queryChanges(cs)
			s.Query.States[ref] = st
		}
	}
}

// queryChanges are what queries match of cs.
func queryChanges(cs []changes.Change) []query.Change {
	out := make([]query.Change, len(cs))
	for n, c := range cs {
		out[n] = query.Change{State: c.State, Checks: c.Checks, Review: c.Review, Merge: changes.ListMerge(c), PR: c.Stage == changes.StagePR || c.Number > 0}
	}
	return out
}

// BranchItems reads the invoking checkout's current branch, never the control
// root's (D5), and the items it names under D43's branch rule. A detached HEAD
// returns no branch and no items.
func BranchItems(ctx context.Context, cfg project.Config) (string, []model.Item, error) {
	branch := cfg.Discovery.Branch()
	if branch == "" {
		return "", nil, nil
	}
	// Archived items count, so unarchive . restores the branch's item.
	reads, err := LoadReads(ctx, cfg, true)
	if err != nil {
		return branch, nil, err
	}
	var refs []model.Ref
	byRef := map[model.Ref]model.Item{}
	for _, item := range reads.Query.Items {
		if item.Project != "" && item.Project != cfg.Project.ID {
			continue
		}
		refs = append(refs, item.Ref)
		byRef[item.Ref] = item
	}
	var items []model.Item
	for _, ref := range changes.BranchItems(refs, branch) {
		items = append(items, byRef[ref])
	}
	return branch, items, nil
}
