package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/backlogmd"
	"github.com/brettinternet/backlog/internal/source/beads"
	"github.com/brettinternet/backlog/internal/source/external"
	"github.com/brettinternet/backlog/internal/source/github"
	"github.com/brettinternet/backlog/internal/source/jira"
	"github.com/brettinternet/backlog/internal/source/linear"
	"github.com/brettinternet/backlog/internal/source/native"
	"github.com/brettinternet/backlog/internal/source/remote"
)

// ReadSnapshot is the read-only composition used by command adapters.
type ReadSnapshot struct {
	Query     query.Snapshot
	Ranking   Result
	Warnings  []string
	searchers []func(context.Context, string) (map[model.Ref]bool, error)
	stale     map[model.Ref]bool
	ctx       context.Context
	input     Input
}

// LoadReads loads active items. With archived set, it also loads archive/ on
// demand (query.md is:archived); those items are marked archived and never ready.
// Sprint data loads only when queries, a view, or ready.sprint use it.
func LoadReads(ctx context.Context, cfg project.Config, archived bool, queries ...string) (ReadSnapshot, error) {
	sprints := WantsSprints(cfg, queries...)
	var s ReadSnapshot
	var err error
	if cfg.Workspace != nil {
		s, err = loadWorkspaceReads(ctx, cfg, archived, sprints)
	} else {
		s, err = loadReads(ctx, cfg, archived, false, sprints)
	}
	deriveBlocks(s.Query.Items)
	if err == nil && slices.ContainsFunc(queries, MentionsChanges) {
		linkReads(ctx, cfg, &s)
	}
	return s, err
}

func loadReads(ctx context.Context, cfg project.Config, archived, skipUntrusted, sprints bool) (ReadSnapshot, error) {
	s := ReadSnapshot{Query: query.Snapshot{Items: []model.Item{}, Settings: cfg.Settings, Now: time.Now(), States: map[model.Ref]query.State{}}}
	s.ctx = ctx
	s.stale = map[model.Ref]bool{}
	s.Query.Me = cfg.Identities()
	archivedRefs := map[model.Ref]bool{}
	for n, c := range cfg.Sources {
		if skipUntrusted {
			if err := cfg.RequireTrusted(fmt.Sprintf("sources[%d]", n)); err != nil {
				s.Warnings = append(s.Warnings, err.Error())
				continue
			}
		}
		backend, err := readSource(cfg, c)
		if err != nil {
			return s, err
		}
		delta, err := loadObservation(ctx, backend, source.Cursor{Sprints: sprints})
		if err != nil {
			return s, err
		}
		if archived {
			active := delta
			seen := map[model.Ref]bool{}
			for _, i := range active.Items {
				seen[i.Ref] = true
			}
			// The second load's search index also covers archived items. Merge
			// observations so a partial archive scan retains unseen rows as stale.
			if delta, err = loadObservation(ctx, backend, source.Cursor{IncludeArchive: true, Sprints: sprints}); err != nil {
				return s, err
			}
			if delta.Archived == nil {
				delta.Archived = map[model.Ref]bool{}
				for _, i := range delta.Items {
					delta.Archived[i.Ref] = !seen[i.Ref]
				}
			}
			delta = source.Merge(active, delta)
		}
		for _, sprint := range delta.Sprints {
			sprint.Source = c.ID
			s.Query.Sprints = append(s.Query.Sprints, sprint)
		}
		for ref, stale := range delta.Stale {
			s.stale[ref] = stale
		}
		for ref, archived := range delta.Archived {
			archivedRefs[ref] = archived
		}
		for _, finding := range delta.Findings {
			if finding.Severity == "warning" {
				s.Warnings = append(s.Warnings, finding.Message)
			}
		}
		if delta.TextSearch != nil {
			s.searchers = append(s.searchers, delta.TextSearch)
		} else {
			s.searchers = append(s.searchers, nil)
		}
		for _, item := range delta.Items {
			if archived || !delta.Archived[item.Ref] {
				s.Query.Items = append(s.Query.Items, item)
			}
		}
	}
	s.Query.Items = hidePulls(cfg, s.Query.Items)
	coordination := cfg.Coordination.Enabled()
	claims := map[model.Ref]Claim{}
	if coordination {
		var err error
		claims, _, err = ClaimStates(ctx, s.Query.Items, s.Query.Now)
		if err != nil {
			s.Warnings = append(s.Warnings, err.Error())
		}
	}
	s.Query.Coordination = coordination
	s.input = Input{Items: s.Query.Items, Config: cfg.Ready, Me: s.Query.Me, Archived: archivedRefs, Stale: s.stale, Coordination: coordination, Claims: claims, Sprints: s.Query.Sprints, Sources: cfg.Sources}
	s.Ranking = Evaluate(s.input)
	for _, e := range s.Ranking.Items {
		s.Query.Next = append(s.Query.Next, e.Ref)
		claim := string(claims[e.Ref])
		if claims[e.Ref] == ClaimMine {
			claim = "me"
		}
		s.Query.States[e.Ref] = query.State{Readiness: string(e.Readiness), Archived: archivedRefs[e.Ref], Claim: claim}
	}
	return s, nil
}

// loadObservation permits fresh rows from a partial scan, not unknown coverage
// or malformed source data. Completeness is still required for deletion/event
// reconciliation; individual stale rows must remain unready at every consumer.
func loadObservation(ctx context.Context, backend source.Source, cursor source.Cursor) (source.Delta, error) {
	delta, err := backend.Load(ctx, cursor)
	if err != nil {
		return delta, err
	}
	if delta.Coverage != source.Complete && delta.Coverage != source.Partial {
		return delta, fmt.Errorf("source %s has incomplete coverage: %v", backend.Info().ID, delta.Findings)
	}
	for _, finding := range delta.Findings {
		if finding.Severity == "error" {
			return delta, fmt.Errorf("source %s has invalid observations: %v", backend.Info().ID, delta.Findings)
		}
	}
	seen := make(map[model.Ref]bool, len(delta.Items))
	for _, item := range delta.Items {
		ref := canonical(item.Ref)
		if seen[ref] {
			return delta, fmt.Errorf("source %s has duplicate item %s", backend.Info().ID, item.Ref.ID)
		}
		seen[ref] = true
	}
	return delta, nil
}

func readSource(cfg project.Config, c project.SourceConfig) (source.Source, error) {
	if cfg.Workspace != nil {
		return openWorkspaceSource(cfg, c.ID)
	}
	backend, err := rawSource(cfg, c)
	if err != nil {
		return nil, err
	}
	if cfg.Paths.StateDir == "" || cfg.Paths.CacheDir == "" {
		return backend, nil
	}
	return &eventSource{Source: backend, cfg: cfg}, nil
}

var providerSecrets = remote.ProcessSecrets

func providerCredentialsFor(cfg project.Config) *remote.Credentials {
	manager := oauth.NewManager(oauth.Store{StateDir: cfg.Paths.StateDir}, cfg.Paths.CacheDir, oauth.DefaultRegistry(), providerSecrets)
	return &remote.Credentials{Secrets: providerSecrets, OAuth: manager}
}

func providerCache(cfg project.Config, sourceID, identity string, account project.AccountConfig) cache.Store {
	if account.OAuth {
		return cache.NewPrincipal(cfg.Paths.CacheDir, cfg.Project.ID, sourceID, identity, oauth.PrincipalKey(account.Provider, account.Host, account.User))
	}
	return cache.New(cfg.Paths.CacheDir, cfg.Project.ID, sourceID, identity)
}

func rawSource(cfg project.Config, c project.SourceConfig) (source.Source, error) {
	for i, configured := range cfg.Sources {
		if configured.ID == c.ID {
			if err := cfg.RequireTrusted(fmt.Sprintf("sources[%d]", i)); err != nil {
				return nil, source.Refuse("unavailable", err.Error())
			}
			break
		}
	}
	if c.Type == "jira" {
		site, err := jira.CanonicalSite(c.Site)
		if err != nil {
			return nil, err
		}
		account, err := cfg.SourceAccount(c)
		if err != nil {
			return nil, source.Refuse("unavailable", err.Error())
		}
		backend, err := jira.New(jira.Options{ID: c.ID, Site: site, JQL: c.JQL, CloudID: c.CloudID, HardLinkTypes: c.HardLinkTypes, UnsuccessfulResolutions: c.UnsuccessfulResolutions, Account: account, AllowWrites: c.AllowWrites, Secrets: providerSecrets, Credentials: providerCredentialsFor(cfg)})
		if err != nil {
			return nil, err
		}
		identity, _ := json.Marshal(struct {
			Source      project.SourceConfig
			User, Email string
		}{c, account.User, account.Email})
		return remote.New(backend, providerCache(cfg, c.ID, string(identity), account), providerSecrets), nil
	}
	if c.Type == "linear" {
		account, err := cfg.SourceAccount(c)
		if err != nil {
			return nil, source.Refuse("unavailable", err.Error())
		}
		backend, err := linear.New(linear.Options{ID: c.ID, Organization: c.Site, Team: c.Team, Project: c.Project, Account: account, AllowWrites: c.AllowWrites, Secrets: providerSecrets, Credentials: providerCredentialsFor(cfg)})
		if err != nil {
			return nil, err
		}
		identity, _ := json.Marshal(struct {
			Source project.SourceConfig
			User   string
		}{c, account.User})
		return remote.New(backend, providerCache(cfg, c.ID, string(identity), account), providerSecrets), nil
	}
	if c.Type == "github" {
		host := c.Host
		if host == "" {
			host = "github.com"
		}
		account, err := cfg.SourceAccount(c)
		if err != nil {
			return nil, source.Refuse("unavailable", err.Error())
		}
		aliases := map[string]string{}
		for _, configured := range cfg.Sources {
			// A project-only source names issues OWNER/REPO#N, so it's no alias.
			if configured.Type != "github" || configured.Repo == "" {
				continue
			}
			h := configured.Host
			if h == "" {
				h = "github.com"
			}
			key := strings.ToLower(h + "/" + configured.Repo)
			if _, duplicate := aliases[key]; duplicate {
				aliases[key] = ""
			} else {
				aliases[key] = configured.ID
			}
		}
		backend, err := github.New(github.Options{ID: c.ID, Host: host, Repo: c.Repo, Account: account, Project: c.GitHubProject, StatusLabels: c.StatusLabels, Secrets: providerSecrets, Credentials: providerCredentialsFor(cfg), SourceIDs: aliases})
		if err != nil {
			return nil, err
		}
		// Configuration and principal partition cached permissions and project mappings.
		identity, _ := json.Marshal(struct {
			Source  project.SourceConfig
			User    string
			Aliases map[string]string
		}{c, account.User, aliases})
		return remote.New(backend, providerCache(cfg, c.ID, string(identity), account), providerSecrets), nil
	}
	if c.Type == "github-pulls" {
		f := c.PullsForge()
		account, err := cfg.ForgeAccount(f)
		if err != nil {
			return nil, source.Refuse("unavailable", err.Error())
		}
		forge, err := githubForge(cfg, f)
		if err != nil {
			return nil, err
		}
		identity, _ := json.Marshal(struct {
			Source project.SourceConfig
			User   string
		}{c, account.User})
		return remote.New(github.NewPulls(c.ID, forge), providerCache(cfg, c.ID, string(identity), account), providerSecrets), nil
	}
	if c.Type == "external" {
		return external.New(external.Options{ID: c.ID, Run: c.Run, Dir: cfg.Discovery.ControlRoot})
	}
	if c.Type != "native" && c.Type != "beads" && c.Type != "backlog-md" {
		return nil, fmt.Errorf("source %s (%s) is not available in this build", c.ID, c.Type)
	}
	statuses := map[string]model.StatusCategory{}
	for _, v := range cfg.Statuses {
		statuses[v.Key] = model.StatusCategory(v.Category)
	}
	path := c.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(cfg.Discovery.ControlRoot, path)
	}
	if c.Type == "backlog-md" {
		return backlogmd.New(backlogmd.Options{ID: c.ID, Path: path, CommonDir: cfg.Discovery.CommonDir, Statuses: statuses, ReadOnly: cfg.ReadOnlySources[c.ID]})
	}
	if c.Type == "beads" {
		return beads.New(beads.Options{ID: c.ID, Path: path, Statuses: statuses})
	}
	return native.New(native.Options{ID: c.ID, Path: path, Prefix: c.Prefix, CommonDir: cfg.Discovery.CommonDir, ProjectID: cfg.Project.ID, Statuses: statuses, CacheDir: cfg.Paths.CacheDir, AutoCommit: cfg.Git.AutoCommit, ControlRoot: cfg.Discovery.ControlRoot, AutoCommitBlocked: cfg.AutoCommitBlocked()})
}

// ReadItem reads one source-qualified ref without parsing or ranking the entire backlog.
func ReadItem(ctx context.Context, cfg project.Config, ref model.Ref) (model.Item, error) {
	if ref.Source == "" || ref.ID == "" {
		return model.Item{}, source.Refuse("invalid-id", "source and item ID are required")
	}
	items, err := ReadDetail(ctx, cfg, ref.Source+":"+ref.ID)
	if err != nil {
		return model.Item{}, err
	}
	if len(items) == 0 {
		return model.Item{}, source.Refuse("not-found", ref.ID)
	}
	if len(items) > 1 {
		return model.Item{}, source.Refuse("ambiguous-id", ref.ID)
	}
	return items[0], nil
}

// ItemFile is the file that stores a native or Backlog.md item. ok is false
// for a source whose items aren't files.
func ItemFile(ctx context.Context, cfg project.Config, ref model.Ref) (path string, ok bool, err error) {
	for _, c := range cfg.Sources {
		if c.ID != ref.Source {
			continue
		}
		backend, err := rawSource(cfg, c)
		if err != nil {
			return "", false, err
		}
		filer, ok := backend.(source.ItemFiler)
		if !ok {
			return "", false, nil
		}
		path, err = filer.ItemFile(ctx, ref.ID)
		return path, true, err
	}
	return "", false, source.Refuse("not-found", "unknown source "+ref.Source)
}

// ActivityReader is the provider activity reader for ref's source. ok is
// false for a source without the read-activity capability.
func ActivityReader(cfg project.Config, ref model.Ref) (reader source.ActivityReader, kind string, ok bool, err error) {
	for _, c := range cfg.Sources {
		if c.ID != ref.Source {
			continue
		}
		backend, err := rawSource(cfg, c)
		if err != nil {
			return nil, c.Type, false, err
		}
		reader, ok = backend.(source.ActivityReader)
		return reader, c.Type, ok && backend.Info().Capabilities.Allows(model.ReadActivity), nil
	}
	return nil, "", false, source.Refuse("not-found", "unknown source "+ref.Source)
}

// ReadDetail resolves shorthand without parsing or ranking the entire backlog.
// It returns every match instead of guessing and includes explicitly named archives.
func ReadDetail(ctx context.Context, cfg project.Config, value string) ([]model.Item, error) {
	sourceID, id, qualified := strings.Cut(value, ":")
	if !qualified {
		id = value
	}
	numeric := id != ""
	for _, c := range id {
		numeric = numeric && c >= '0' && c <= '9'
	}
	matches := []model.Item{}
	for _, c := range cfg.Sources {
		if qualified && sourceID != c.ID {
			continue
		}
		backend, err := readSource(cfg, c)
		if err != nil {
			return nil, err
		}
		requested := id
		if numeric && c.Prefix != "" {
			requested = c.Prefix + "-" + id
		}
		item, err := backend.Read(ctx, requested)
		if err != nil {
			var refusal *source.Error
			if errors.As(err, &refusal) && (refusal.Reason == "not-found" || refusal.Reason == "invalid-id") {
				continue
			}
			return nil, err
		}
		matches = append(matches, item)
	}
	return matches, nil
}

// reassess ranks one item again after a fresh read replaces its row. A fresh
// read is not stale; a provider that reports archival keeps it unready.
func (s *ReadSnapshot) reassess(fresh model.Item) Explanation {
	in := s.input
	in.Items = slices.Clone(in.Items)
	for n := range in.Items {
		if in.Items[n].Ref == fresh.Ref {
			in.Items[n] = fresh
		}
	}
	in.Archived, in.Stale = maps.Clone(in.Archived), maps.Clone(in.Stale)
	in.Archived[fresh.Ref] = in.Archived[fresh.Ref] || fresh.Fields["archived"] == true
	delete(in.Stale, fresh.Ref)
	for _, e := range Evaluate(in).Items {
		if e.Ref == fresh.Ref {
			return e
		}
	}
	return Explanation{Ref: fresh.Ref}
}

// remoteSource reports a provider whose cached snapshot can lag the provider.
func remoteSource(cfg project.Config, id string) bool {
	for _, c := range cfg.Sources {
		if c.ID == id {
			return c.Type == "linear" || c.Type == "github" || c.Type == "jira" || c.Type == "github-pulls"
		}
	}
	return false
}

func (s *ReadSnapshot) Parse(text string) (query.Query, error) {
	q, err := query.Parse(text, query.Options{})
	if err != nil {
		return q, err
	}
	if errs := q.Validate(s.Query); len(errs) > 0 {
		return q, errs[0]
	}
	s.Query.TextMatches = map[string]map[model.Ref]bool{}
	for _, term := range q.Terms {
		if term.Field != "text" {
			continue
		}
		for _, value := range term.Values {
			if _, ok := s.Query.TextMatches[value.Text]; ok {
				continue
			}
			matches := map[model.Ref]bool{}
			complete := len(s.searchers) > 0
			for _, search := range s.searchers {
				if search == nil {
					complete = false
					break
				}
				found, e := search(s.ctx, value.Text)
				if e != nil || found == nil {
					complete = false
					break
				}
				for ref := range found {
					matches[ref] = true
				}
			}
			if complete {
				s.Query.TextMatches[value.Text] = matches
			}
		}
	}
	return q, nil
}
