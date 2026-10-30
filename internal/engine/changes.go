package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source/github"
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
		out[p.Project.ID] = changes.Options{Discovery: p.Discovery, CacheDir: p.Paths.CacheDir, Trailer: p.History.CommitTrailer, Git: p.Changes.Git, Forges: forges(p)}
	}
	return out
}

// TrackChanges keeps cfg's changes current off the UI loop, or returns nil
// when no project reads any.
func TrackChanges(ctx context.Context, cfg project.Config) *changes.Tracker {
	return changes.Track(ctx, ChangesOptions(cfg))
}

// forges are p's configured forges, which need trust like sources, else the
// GitHub origin when exactly one account is configured for its host.
func forges(p project.Config) []changes.Forge {
	configs := p.Changes.Forges
	explicit := len(configs) > 0
	if !explicit {
		host, repo := project.Origin(p.Discovery)
		accounts := 0
		for _, a := range p.Accounts["github"] {
			if a.Host == host {
				accounts++
			}
		}
		if host == "" || accounts != 1 {
			return nil
		}
		configs = []project.ForgeConfig{{Type: "github", Host: host, Repo: repo}}
	}
	var out []changes.Forge
	for i, f := range configs {
		if f.Host == "" {
			f.Host = "github.com"
		}
		name := "github " + f.Repo
		if explicit {
			if err := p.RequireTrusted(fmt.Sprintf("changes.forges[%d]", i)); err != nil {
				out = append(out, failedForge{name, err})
				continue
			}
		}
		forge, err := newForge(p, f)
		if err != nil {
			out = append(out, failedForge{name, err})
			continue
		}
		out = append(out, forge)
	}
	return out
}

func newForge(p project.Config, f project.ForgeConfig) (*github.Forge, error) {
	account, err := forgeAccount(p, f)
	if err != nil {
		return nil, err
	}
	id := "forge:" + f.Host + "/" + f.Repo
	identity, _ := json.Marshal(struct {
		Forge project.ForgeConfig
		User  string
	}{f, account.User})
	path := ""
	if p.Paths.CacheDir != "" {
		path = strings.TrimSuffix(providerCache(p, id, string(identity), account).Path, ".sqlite") + ".pulls.json"
	}
	return github.NewForge(github.Options{ID: id, Host: f.Host, Repo: f.Repo, Account: account, Secrets: providerSecrets, Credentials: providerCredentialsFor(p)}, path)
}

// forgeAccount is the named account, else that of a GitHub source reading
// the same repository, else the only account for the forge's host.
func forgeAccount(p project.Config, f project.ForgeConfig) (project.AccountConfig, error) {
	if f.Account != "" {
		return p.SourceAccount(project.SourceConfig{ID: "forge", Type: "github", Host: f.Host, Account: f.Account})
	}
	for _, s := range p.Sources {
		host := s.Host
		if host == "" {
			host = "github.com"
		}
		if s.Type == "github" && host == f.Host && strings.EqualFold(s.Repo, f.Repo) {
			return p.SourceAccount(s)
		}
	}
	var found []project.AccountConfig
	for _, a := range p.Accounts["github"] {
		if a.Host == f.Host {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		return project.AccountConfig{}, fmt.Errorf("name one of the GitHub accounts for %s", f.Host)
	}
	found[0].Provider = "github"
	return found[0], nil
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
	result := ChangesResult{Changes: changes.Enrich(ctx, linked[ref]), Providers: d.Statuses}
	if result.Changes == nil {
		result.Changes = []changes.Change{}
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
		case "change", "checks", "review":
			return true
		case "has":
			for _, v := range t.Values {
				if strings.EqualFold(v.Text, "change") {
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
		out[n] = query.Change{State: c.State, Checks: c.Checks, Review: c.Review}
	}
	return out
}
