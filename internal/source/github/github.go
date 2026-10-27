// Package github adapts GitHub Issues and Projects v2. Queries, identity checks,
// independent project scans and non-retried writes derive from Worklease v1.9.3
// internal/queue/github{,_projects,_write}.go; recovery journals are not ported.
package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

type Options struct {
	ID, Host, Repo string
	Account        project.AccountConfig
	Project        *project.GitHubProjectConfig
	HTTP           *http.Client
	Scheduler      *remote.Scheduler
	Credentials    *remote.Credentials
	Secrets        *remote.Secrets
	SourceIDs      map[string]string // lower-case host/repository to configured source alias
	// Now is the clock used to classify Projects v2 iterations into active,
	// future and closed when the catalog is fetched. Query evaluation never
	// consults it directly.
	Now func() time.Time
	// StatusLabels maps label names to categories for issues without a
	// Projects v2 binding. Names match case-insensitively, as on GitHub.
	StatusLabels map[string]string
}
type Source struct {
	o               Options
	statusLabels    map[string]string // lower-case label name to configured key
	client          remote.Client
	mu              sync.Mutex
	repositoryID    string
	scopes          string
	writable        bool
	issueWrite      bool
	projectGrants   map[string]bool
	projectWritable bool
	// readSprints enables the iteration value and catalog on single-item reads
	// once a sprint-enabled load or ReadSprints has requested them.
	readSprints bool
}

func New(o Options) (*Source, error) {
	if o.Host == "" {
		o.Host = "github.com"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	parts := strings.Split(o.Repo, "/")
	if o.ID == "" || len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(o.Repo, " :@?#\\\t\r\n") || strings.ContainsAny(o.Host, "/\\ :@?#\t\r\n") || o.Account.Host != o.Host || o.Account.User == "" || o.Secrets == nil || o.Credentials == nil {
		return nil, source.Refuse("unavailable", "GitHub requires host, owner/repo and a matching user account")
	}
	if p := o.Project; p != nil {
		if p.ID == "" || p.FieldID == "" || p.Owner == "" || p.Number < 1 || len(p.Options) == 0 {
			return nil, source.Refuse("rejected", "invalid Projects v2 binding")
		}
		for option, category := range p.Options {
			if option == "" || !validCategory(category) {
				return nil, source.Refuse("rejected", "invalid project status mapping")
			}
		}
	}
	var statusLabels map[string]string
	if len(o.StatusLabels) > 0 {
		if o.Project != nil {
			return nil, source.Refuse("rejected", "invalid status label mapping")
		}
		statusLabels = map[string]string{}
		for label, category := range o.StatusLabels {
			folded := strings.ToLower(label)
			if strings.TrimSpace(label) == "" || folded == "open" || folded == "closed" || folded == "unknown" || statusLabels[folded] != "" || !validCategory(category) {
				return nil, source.Refuse("rejected", "invalid status label mapping")
			}
			statusLabels[folded] = label
		}
	}
	return &Source{o: o, statusLabels: statusLabels, client: remote.Client{Host: apiHost(o.Host), Account: o.Account.User, HTTP: o.HTTP, Scheduler: o.Scheduler, Secrets: o.Secrets, OAuth: o.Account.OAuth}}, nil
}
func apiHost(host string) string {
	if host == "github.com" {
		return "api.github.com"
	}
	return host
}
func (s *Source) base() string {
	if s.o.Host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + s.o.Host + "/api/v3"
}
func (s *Source) endpoint() string {
	if s.o.Host == "github.com" {
		return "https://api.github.com/graphql"
	}
	return "https://" + s.o.Host + "/api/graphql"
}
func (s *Source) Info() source.Info {
	return source.Info{ID: s.o.ID, Kind: "github", Display: s.o.Repo, Capabilities: model.Capabilities{model.Set: true, model.AppendComment: true}}
}
func (s *Source) request(ctx context.Context, c remote.Credential, method, endpoint string, payload any) (remote.Response, error) {
	var body *bytes.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return remote.Response{}, source.Refuse("rejected", "invalid payload")
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return remote.Response{}, source.Refuse("unavailable", "invalid endpoint")
	}
	if body != nil {
		req.Body = http.NoBody
		if body.Len() > 0 {
			req, err = http.NewRequestWithContext(ctx, method, endpoint, body)
			if err != nil {
				return remote.Response{}, err
			}
		}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	return s.client.Do(ctx, req, c)
}
func (s *Source) authenticate(ctx context.Context) (remote.Credential, error) {
	return s.o.Credentials.Resolve(ctx, s.o.Account, func(ctx context.Context, c remote.Credential) (string, error) {
		if s.o.Account.OAuth {
			if err := s.appPermissions(ctx, c); err != nil {
				return "", err
			}
		}
		response, err := s.request(ctx, c, http.MethodPost, s.endpoint(), map[string]any{"query": `query {viewer {login}}`})
		if err != nil {
			return "", err
		}
		var data struct{ Viewer struct{ Login string } }
		if decode(response.Body, &data) != nil || data.Viewer.Login == "" {
			return "", source.Refuse("unavailable", "invalid principal")
		}
		s.scopes = response.Header.Get("X-OAuth-Scopes")
		return data.Viewer.Login, nil
	})
}
func (s *Source) query(ctx context.Context, c remote.Credential, query string, variables any, out any) error {
	response, err := s.request(ctx, c, http.MethodPost, s.endpoint(), map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	return decode(response.Body, out)
}
func decode(raw []byte, out any) error {
	var result struct {
		Data   json.RawMessage
		Errors []json.RawMessage
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Errors) > 0 || len(result.Data) == 0 || string(result.Data) == "null" || json.Unmarshal(result.Data, out) != nil {
		return source.Refuse("unavailable", "GitHub returned incomplete or invalid data")
	}
	return nil
}
func (s *Source) variables() map[string]any {
	parts := strings.Split(s.o.Repo, "/")
	return map[string]any{"owner": parts[0], "repo": parts[1]}
}
func (s *Source) identity(ctx context.Context, c remote.Credential) error {
	var result struct {
		Repository *struct {
			ID, NameWithOwner string
			ViewerPermission  string
		}
	}
	if err := s.query(ctx, c, `query($owner:String!,$repo:String!){repository(owner:$owner,name:$repo){id nameWithOwner viewerPermission}}`, s.variables(), &result); err != nil {
		return err
	}
	r := result.Repository
	if r == nil || r.ID == "" || !strings.EqualFold(r.NameWithOwner, s.o.Repo) || s.repositoryID != "" && s.repositoryID != r.ID {
		return source.Refuse("unavailable", "repository identity changed or inaccessible")
	}
	s.repositoryID = r.ID
	s.writable = r.ViewerPermission == "ADMIN" || r.ViewerPermission == "MAINTAIN" || r.ViewerPermission == "WRITE" || r.ViewerPermission == "TRIAGE"
	return nil
}

type pageInfo struct {
	HasNextPage bool
	EndCursor   string
}
type related struct {
	ID         string
	Number     int
	Repository struct{ NameWithOwner string }
}
type relationConnection struct {
	Nodes    []related
	PageInfo *pageInfo
}
type issue struct {
	ID                                   string
	Number                               int
	Title, Body, State, StateReason, URL string
	CreatedAt, UpdatedAt                 time.Time
	Repository                           struct{ NameWithOwner string }
	Assignees                            struct {
		Nodes    []struct{ Login string }
		PageInfo *pageInfo
	}
	Labels struct {
		Nodes    []struct{ Name string }
		PageInfo *pageInfo
	}
	BlockedBy, SubIssues relationConnection
	Parent               *related
}

const issueFields = `id number title body state stateReason url createdAt updatedAt repository{nameWithOwner} assignees(first:100){nodes{login} pageInfo{hasNextPage endCursor}} labels(first:100){nodes{name} pageInfo{hasNextPage endCursor}} blockedBy(first:100){nodes{id number repository{nameWithOwner}} pageInfo{hasNextPage endCursor}} subIssues(first:100){nodes{id number repository{nameWithOwner}} pageInfo{hasNextPage endCursor}} parent{id number repository{nameWithOwner}}`
const listQuery = `query($owner:String!,$repo:String!,$after:String){repository(owner:$owner,name:$repo){id nameWithOwner issues(first:100,after:$after,states:[OPEN,CLOSED],orderBy:{field:CREATED_AT,direction:ASC}){nodes{` + issueFields + `} pageInfo{hasNextPage endCursor}}}}`
const detailQuery = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){id nameWithOwner issue(number:$number){` + issueFields + `}}}`

func advance(p *pageInfo, seen map[string]bool) (string, error) {
	if p == nil {
		return "", source.Refuse("unavailable", "missing pagination metadata")
	}
	if !p.HasNextPage {
		return "", nil
	}
	if p.EndCursor == "" || seen[p.EndCursor] {
		return "", source.Refuse("unavailable", "invalid pagination cursor")
	}
	seen[p.EndCursor] = true
	return p.EndCursor, nil
}
func (s *Source) Load(ctx context.Context, cursor source.Cursor) (source.Delta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.authenticate(ctx)
	if err != nil {
		return source.Delta{}, err
	}
	if err = s.identity(ctx, c); err != nil {
		return source.Delta{}, err
	}
	// A 304 validates this hint only, never the complete issue or project snapshot.
	if _, err = s.request(ctx, c, http.MethodGet, s.base()+"/repos/"+s.o.Repo+"/issues?state=all&sort=updated&direction=desc&per_page=1", nil); err != nil {
		return source.Delta{}, err
	}
	projects, catalog, err := s.projects(ctx, c, cursor.Sprints)
	if err != nil {
		return source.Delta{}, err
	}
	sprintNames := map[string]string{}
	for _, sprint := range catalog {
		sprintNames[sprint.Key] = sprint.Name
	}
	delta := source.Delta{Coverage: source.Complete, Sprints: catalog, Cursor: source.Cursor{Sprints: cursor.Sprints}}
	vars := s.variables()
	seen := map[string]bool{}
	ids := map[string]bool{}
	for {
		var result struct {
			Repository *struct {
				ID, NameWithOwner string
				Issues            struct {
					Nodes    []issue
					PageInfo *pageInfo
				}
			}
		}
		if err = s.query(ctx, c, listQuery, vars, &result); err != nil {
			return source.Delta{}, err
		}
		if result.Repository == nil || result.Repository.ID != s.repositoryID || !strings.EqualFold(result.Repository.NameWithOwner, s.o.Repo) {
			return source.Delta{}, source.Refuse("unavailable", "repository identity changed")
		}
		for _, row := range result.Repository.Issues.Nodes {
			if ids[row.ID] {
				return source.Delta{}, source.Refuse("unavailable", "duplicate issue identity")
			}
			ids[row.ID] = true
			item, err := s.item(ctx, c, row, projects[row.ID], sprintNames)
			if err != nil {
				return source.Delta{}, err
			}
			if mapped := s.mappedLabels(item.Labels); row.State == "OPEN" && len(mapped) > 1 {
				delta.Findings = append(delta.Findings, source.Finding{Code: "github-status-labels", Severity: "warning", Path: s.o.Repo + "#" + item.Ref.ID, Message: fmt.Sprintf("%s#%s has several status labels (%s), so its status is unknown until one remains", s.o.Repo, item.Ref.ID, strings.Join(mapped, ", "))})
			}
			delta.Items = append(delta.Items, item)
		}
		after, err := advance(result.Repository.Issues.PageInfo, seen)
		if err != nil {
			return source.Delta{}, err
		}
		if after == "" {
			return delta, nil
		}
		vars["after"] = after
	}
}
func validNumber(id string) (int, error) {
	n, err := strconv.Atoi(id)
	if err != nil || n < 1 || strconv.Itoa(n) != id {
		return 0, source.Refuse("invalid-id", "positive issue number required")
	}
	return n, nil
}

// ReadSprints enables the iteration value and catalog on single-item reads.
// The remote wrapper calls it when its cache holds sprint demand.
func (s *Source) ReadSprints() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readSprints = true
}
func (s *Source) Read(ctx context.Context, id string) (model.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.authenticate(ctx)
	if err != nil {
		return model.Item{}, err
	}
	return s.read(ctx, c, id)
}
func (s *Source) read(ctx context.Context, c remote.Credential, id string) (model.Item, error) {
	n, err := validNumber(id)
	if err != nil {
		return model.Item{}, err
	}
	if err = s.identity(ctx, c); err != nil {
		return model.Item{}, err
	}
	vars := s.variables()
	vars["number"] = n
	var result struct {
		Repository *struct {
			ID, NameWithOwner string
			Issue             *issue
		}
	}
	if err = s.query(ctx, c, detailQuery, vars, &result); err != nil {
		return model.Item{}, err
	}
	if result.Repository == nil || result.Repository.ID != s.repositoryID || !strings.EqualFold(result.Repository.NameWithOwner, s.o.Repo) {
		return model.Item{}, source.Refuse("unavailable", "repository identity changed")
	}
	if result.Repository.Issue == nil {
		return model.Item{}, source.Refuse("not-found", "issue unavailable")
	}
	if result.Repository.Issue.Number != n {
		return model.Item{}, source.Refuse("unavailable", "issue identity changed")
	}
	projects, catalog, err := s.projects(ctx, c, s.readSprints)
	if err != nil {
		return model.Item{}, err
	}
	sprintNames := map[string]string{}
	for _, sprint := range catalog {
		sprintNames[sprint.Key] = sprint.Name
	}
	return s.item(ctx, c, *result.Repository.Issue, projects[result.Repository.Issue.ID], sprintNames)
}
func (s *Source) ref(row related) model.Ref {
	id := s.o.Host + "/" + row.Repository.NameWithOwner
	if alias := s.o.SourceIDs[strings.ToLower(id)]; alias != "" {
		id = alias
	}
	if strings.EqualFold(row.Repository.NameWithOwner, s.o.Repo) {
		id = s.o.ID
	}
	return model.Ref{Source: id, ID: strconv.Itoa(row.Number)}
}
func (s *Source) item(ctx context.Context, c remote.Credential, row issue, p projectItem, sprintNames map[string]string) (model.Item, error) {
	if row.ID == "" || row.Number < 1 || row.UpdatedAt.IsZero() || !strings.EqualFold(row.Repository.NameWithOwner, s.o.Repo) {
		return model.Item{}, source.Refuse("unavailable", "invalid issue identity")
	}
	item := model.Item{Ref: model.Ref{Source: s.o.ID, ID: strconv.Itoa(row.Number)}, Title: row.Title, Type: "issue", Description: row.Body, Status: strings.ToLower(row.State), Category: model.Todo, URL: row.URL, Created: row.CreatedAt, Updated: row.UpdatedAt, Fields: map[string]any{"id": row.ID, "repositoryID": s.repositoryID, "stateReason": row.StateReason}}
	if row.State == "CLOSED" {
		item.Category = model.Done
		if row.StateReason == "NOT_PLANNED" {
			item.Category = model.Canceled
		}
	} else if row.State != "OPEN" {
		return model.Item{}, source.Refuse("unavailable", "unknown issue state")
	}
	if row.Assignees.PageInfo == nil || row.Labels.PageInfo == nil || row.Assignees.PageInfo.HasNextPage || row.Labels.PageInfo.HasNextPage {
		return model.Item{}, source.Refuse("unavailable", "issue labels or assignees exceed complete read limit")
	}
	for _, v := range row.Assignees.Nodes {
		item.Assignees = append(item.Assignees, v.Login)
	}
	for _, v := range row.Labels.Nodes {
		item.Labels = append(item.Labels, v.Name)
	}
	if s.statusLabels != nil && row.State == "OPEN" {
		// Several mapped labels fail closed rather than guess one.
		item.Status, item.Category = "unknown", model.Backlog
		if mapped := s.mappedLabels(item.Labels); len(mapped) == 1 {
			item.Status, item.Category = mapped[0], model.StatusCategory(s.o.StatusLabels[mapped[0]])
		}
	}
	for _, edge := range []struct {
		name       string
		connection relationConnection
		kind       model.EdgeKind
	}{{"blockedBy", row.BlockedBy, model.BlockedBy}, {"subIssues", row.SubIssues, model.Parent}} {
		nodes := edge.connection.Nodes
		next, err := advance(edge.connection.PageInfo, map[string]bool{})
		if err != nil {
			return model.Item{}, err
		}
		seen := map[string]bool{}
		for next != "" {
			vars := s.variables()
			vars["number"] = row.Number
			vars["after"] = next
			var result struct {
				Repository *struct {
					Issue *struct{ BlockedBy, SubIssues relationConnection }
				}
			}
			q := `query($owner:String!,$repo:String!,$number:Int!,$after:String){repository(owner:$owner,name:$repo){issue(number:$number){` + edge.name + `(first:100,after:$after){nodes{id number repository{nameWithOwner}} pageInfo{hasNextPage endCursor}}}}}`
			if err = s.query(ctx, c, q, vars, &result); err != nil {
				return model.Item{}, err
			}
			if result.Repository == nil || result.Repository.Issue == nil {
				return model.Item{}, source.Refuse("unavailable", "relations inaccessible")
			}
			conn := result.Repository.Issue.BlockedBy
			if edge.name == "subIssues" {
				conn = result.Repository.Issue.SubIssues
			}
			nodes = append(nodes, conn.Nodes...)
			next, err = advance(conn.PageInfo, seen)
			if err != nil {
				return model.Item{}, err
			}
		}
		for _, r := range nodes {
			if r.Number < 1 || r.ID == "" || r.Repository.NameWithOwner == "" {
				return model.Item{}, source.Refuse("unavailable", "invalid relation")
			}
			ref := s.ref(r)
			if edge.kind == model.BlockedBy {
				item.BlockedBy = append(item.BlockedBy, ref)
				item.Edges = append(item.Edges, model.Edge{Kind: model.BlockedBy, Target: ref})
			} else {
				item.Children = append(item.Children, ref)
			}
		}
	}
	if row.Parent != nil {
		r := s.ref(*row.Parent)
		item.Parent = &r
		item.Edges = append(item.Edges, model.Edge{Kind: model.Parent, Target: r})
	}
	if s.o.Project != nil {
		item.Fields["projectItemID"] = p.ID
		item.Fields["projectOptionID"] = p.Value.OptionID
		item.Fields["projectUpdatedAt"] = p.UpdatedAt
		item.Fields["projectStatus"] = p.Value.Name
		if row.State == "OPEN" {
			item.Status = p.Value.OptionID
			category := s.o.Project.Options[p.Value.OptionID]
			item.Category = model.StatusCategory(category)
			if category == "" {
				item.Status = "unknown"
				item.Category = model.Backlog
			} else if p.Value.Name != "" {
				item.Names = map[string]string{p.Value.OptionID: p.Value.Name}
			}
		}
	}
	// The sprint is read-only and fetched only on demand, so it stays out of
	// the fingerprint: reads with and without it must agree for --if-version.
	raw, _ := json.Marshal(item)
	hash := sha256.Sum256(raw)
	item.Version = hex.EncodeToString(hash[:])
	if s.o.Project != nil && p.Sprint.IterationID != "" {
		item.Sprint = p.Sprint.IterationID
		if name := sprintNames[item.Sprint]; name != "" {
			if item.Names == nil {
				item.Names = map[string]string{}
			}
			item.Names[item.Sprint] = name
		}
	}
	return item, nil
}

// mappedLabels returns the configured keys of the status labels among labels.
func (s *Source) mappedLabels(labels []string) []string {
	var keys []string
	for _, label := range labels {
		if key := s.statusLabels[strings.ToLower(label)]; key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}
func validCategory(v string) bool {
	return v == "backlog" || v == "todo" || v == "active" || v == "done" || v == "canceled"
}

// PathEscape is used only for issue numbers after validation, not arbitrary URLs.
func (s *Source) issuePath(id string) string {
	return s.base() + "/repos/" + s.o.Repo + "/issues/" + url.PathEscape(id)
}
