// Package linear ports Worklease v1.9.3 internal/queue/linear{,_write}.go's
// identity, paging, overlap and directional relations to the source contract.
// D15 replaces Worklease's receipt and recovery journals.
package linear

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

type Options struct {
	ID, Organization, Team, Project string
	Account                         project.AccountConfig
	AllowWrites                     bool
	HTTP                            *http.Client
	Scheduler                       *remote.Scheduler
	Credentials                     *remote.Credentials
	Secrets                         *remote.Secrets
	Now                             func() time.Time
}
type Source struct {
	o      Options
	client remote.Client
	mu     sync.Mutex
	active bool
}

func New(o Options) (*Source, error) {
	if o.ID == "" || !uuid(o.Organization) || !uuid(o.Team) || o.Project != "" && !uuid(o.Project) || !uuid(o.Account.User) || o.Secrets == nil || o.Credentials == nil {
		return nil, source.Refuse("unavailable", "Linear requires organization, team, optional project and account user UUIDs")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Source{o: o, client: remote.Client{Host: "api.linear.app", Account: o.Account.User, HTTP: o.HTTP, Scheduler: o.Scheduler, Secrets: o.Secrets, RawAuthorization: true}}, nil
}
func uuid(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
func (s *Source) Info() source.Info {
	return source.Info{ID: s.o.ID, Kind: "linear", Display: s.o.Team, Capabilities: model.Capabilities{model.Set: s.o.AllowWrites, model.AppendComment: s.o.AllowWrites}}
}
func (s *Source) query(ctx context.Context, c remote.Credential, q string, v any, out any) error {
	raw, err := json.Marshal(map[string]any{"query": q, "variables": v})
	if err != nil {
		return source.Refuse("rejected", "invalid Linear request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.linear.app/graphql", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(ctx, req, c)
	if err != nil {
		return err
	}
	return decode(response.Body, out)
}
func decode(raw []byte, out any) error {
	var e struct {
		Data   json.RawMessage
		Errors []json.RawMessage
	}
	if json.Unmarshal(raw, &e) != nil || len(e.Errors) > 0 || len(e.Data) == 0 || string(e.Data) == "null" || json.Unmarshal(e.Data, out) != nil {
		return source.Refuse("unavailable", "Linear returned incomplete or invalid data")
	}
	return nil
}

const identityQuery = `query($team:String!){viewer{id active organization{id}} team(id:$team){id}}`

func (s *Source) authenticate(ctx context.Context) (remote.Credential, error) {
	s.active = false
	return s.o.Credentials.Resolve(ctx, s.o.Account, func(ctx context.Context, c remote.Credential) (string, error) {
		var r struct {
			Viewer *struct {
				ID           string
				Active       bool
				Organization *struct{ ID string }
			}
			Team *struct{ ID string }
		}
		if err := s.query(ctx, c, identityQuery, map[string]any{"team": s.o.Team}, &r); err != nil {
			return "", err
		}
		if r.Viewer == nil || r.Viewer.Organization == nil || r.Viewer.Organization.ID != s.o.Organization || r.Team == nil || r.Team.ID != s.o.Team {
			return "", source.Refuse("unavailable", "Linear identity or scope changed")
		}
		s.active = r.Viewer.Active
		return r.Viewer.ID, nil
	})
}

type pageInfo struct {
	HasNextPage bool
	EndCursor   string
}

func advance(p *pageInfo, seen map[string]bool) (string, error) {
	if p == nil {
		return "", source.Refuse("unavailable", "missing Linear pagination metadata")
	}
	if !p.HasNextPage {
		return "", nil
	}
	if p.EndCursor == "" || seen[p.EndCursor] {
		return "", source.Refuse("unavailable", "missing or repeated Linear cursor")
	}
	seen[p.EndCursor] = true
	return p.EndCursor, nil
}

type connection[T any] struct {
	Nodes    []T
	PageInfo *pageInfo
}
type entity struct {
	ID, Identifier string
	Team           *struct{ ID string }
	Project        *struct{ ID string }
}
type relation struct {
	Type                string
	RelatedIssue, Issue *entity
}
type issue struct {
	ID, Identifier, Title, Description, URL string
	CreatedAt, UpdatedAt                    time.Time
	ArchivedAt                              *time.Time
	Trashed                                 bool
	Priority                                int
	Team                                    *struct{ ID string }
	Project                                 *struct{ ID string }
	State                                   *struct{ ID, Name, Type string }
	Assignee                                *struct{ ID string }
	Labels                                  connection[struct{ ID, Name string }]
	Parent                                  *entity
	Relations, InverseRelations             *connection[relation]
}

const issueFields = `id identifier title description url createdAt updatedAt archivedAt trashed priority team{id} project{id} state{id name type} assignee{id} labels(first:250){nodes{id name} pageInfo{hasNextPage endCursor}} parent{id identifier team{id} project{id}}`

// Keep list pages lightweight: nesting a 250-label connection under 250
// issues multiplies GraphQL complexity. Read detail only after deduplication.
const listFields = `id identifier updatedAt team{id} project{id}`
const listQuery = `query($team:String!,$after:String,$count:Int!){team(id:$team){id issues(first:$count,after:$after,includeArchived:true,orderBy:updatedAt){nodes{` + listFields + `} pageInfo{hasNextPage endCursor}}}}`
const incrementalQuery = `query($team:String!,$after:String,$count:Int!,$since:DateTimeOrDuration!){team(id:$team){id issues(first:$count,after:$after,includeArchived:true,orderBy:updatedAt,filter:{updatedAt:{gte:$since}}){nodes{` + listFields + `} pageInfo{hasNextPage endCursor}}}}`
const detailQuery = `query($id:String!){issue(id:$id){` + issueFields + `}}`
const entityFields = `id identifier team{id} project{id}`
const relationsQuery = `query($id:String!,$after:String,$count:Int!){issue(id:$id){id relations(first:$count,after:$after){nodes{type relatedIssue{` + entityFields + `}} pageInfo{hasNextPage endCursor}}}}`
const inverseQuery = `query($id:String!,$after:String,$count:Int!){issue(id:$id){id inverseRelations(first:$count,after:$after){nodes{type issue{` + entityFields + `}} pageInfo{hasNextPage endCursor}}}}`

func (s *Source) within(r issue) bool {
	return uuid(r.ID) && r.Identifier != "" && !r.UpdatedAt.IsZero() && r.Team != nil && r.Team.ID == s.o.Team && (s.o.Project == "" || r.Project != nil && r.Project.ID == s.o.Project)
}

type syncCursor struct {
	Watermark      time.Time
	Reconciled     time.Time
	Scope          string
	IncludeArchive bool
}

func (s *Source) scope() string {
	return s.o.Organization + ":" + s.o.Team + ":" + s.o.Project + ":" + s.o.Account.User
}

const reconcileInterval = time.Hour
const changeOverlap = 2 * time.Minute
const detailBatchSize = 10
const relationBatchPageSize = 25

// Load reads only the overlapping change window between hourly reconciliations.
// Reconciliation scans all rows and relations, including a 30-day recovery window
// for moving-order gaps. Neither traversal is an absence proof.
func (s *Source) Load(ctx context.Context, cursor source.Cursor) (source.Delta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := s.o.Now().UTC()
	since := start
	var state syncCursor
	if cursor.Value != "" {
		c := &state
		if len(cursor.Value) > 2048 || json.Unmarshal([]byte(cursor.Value), c) != nil || c.Scope != s.scope() || c.Watermark.IsZero() || c.Watermark.After(start) || c.Reconciled.After(start) {
			return source.Delta{}, source.Refuse("rejected", "invalid Linear sync cursor")
		}
		since = c.Watermark
	}
	c, err := s.authenticate(ctx)
	if err != nil {
		return source.Delta{}, err
	}
	reconcile := state.Reconciled.IsZero() || start.Sub(state.Reconciled) >= reconcileInterval || state.IncludeArchive != cursor.IncludeArchive
	windows := []time.Time{since.Add(-changeOverlap)}
	if reconcile {
		windows = []time.Time{{}, since.Add(-30 * 24 * time.Hour)}
	}
	rows := map[string]issue{}
	for _, window := range windows {
		seen := map[string]bool{}
		vars := map[string]any{"team": s.o.Team, "count": 250}
		q := listQuery
		if !window.IsZero() {
			q = incrementalQuery
			vars["since"] = window.Format(time.RFC3339Nano)
		}
		for page := 0; ; page++ {
			if page >= 1000 {
				return source.Delta{}, source.Refuse("unavailable", "Linear issue page budget exceeded")
			}
			var r struct {
				Team *struct {
					ID     string
					Issues connection[issue]
				}
			}
			if err = s.query(ctx, c, q, vars, &r); err != nil {
				return source.Delta{}, err
			}
			if r.Team == nil || r.Team.ID != s.o.Team || r.Team.Issues.Nodes == nil {
				return source.Delta{}, source.Refuse("unavailable", "Linear team unavailable")
			}
			for _, row := range r.Team.Issues.Nodes {
				if !uuid(row.ID) || row.Team == nil || row.Team.ID != s.o.Team {
					return source.Delta{}, source.Refuse("unavailable", "Linear issue identity changed")
				}
				if s.o.Project != "" && (row.Project == nil || row.Project.ID != s.o.Project) {
					continue
				}
				if !s.within(row) {
					return source.Delta{}, source.Refuse("unavailable", "invalid Linear issue")
				}
				rows[row.ID] = row
			}
			next, err := advance(r.Team.Issues.PageInfo, seen)
			if err != nil {
				return source.Delta{}, err
			}
			if next == "" {
				break
			}
			vars["after"] = next
		}
	}
	delta := source.Delta{Coverage: source.Partial, Incremental: !reconcile, RetainUnseen: !reconcile, Archived: map[model.Ref]bool{}}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for offset := 0; offset < len(ids); offset += detailBatchSize {
		batch, err := s.details(ctx, c, ids[offset:min(offset+detailBatchSize, len(ids))])
		if err != nil {
			return source.Delta{}, err
		}
		for _, row := range batch {
			item, err := s.item(ctx, c, row)
			if err != nil {
				return source.Delta{}, err
			}
			if row.ArchivedAt != nil || row.Trashed {
				delta.Archived[item.Ref] = true
				if !cursor.IncludeArchive {
					continue
				}
			}
			delta.Items = append(delta.Items, item)
		}
	}
	if reconcile {
		state.Reconciled = start
	}
	raw, _ := json.Marshal(syncCursor{Watermark: start, Reconciled: state.Reconciled, Scope: s.scope(), IncludeArchive: cursor.IncludeArchive})
	delta.Cursor = source.Cursor{Value: string(raw), IncludeArchive: cursor.IncludeArchive}
	return delta, nil
}

// details batches bounded aliases instead of one detail and two relation requests
// per issue. Overflow relation pages still use the normal paginated reader.
func (s *Source) details(ctx context.Context, c remote.Credential, ids []string) ([]issue, error) {
	var q strings.Builder
	q.WriteString("query(")
	vars := map[string]any{}
	for i, id := range ids {
		if i > 0 {
			q.WriteString(",")
		}
		fmt.Fprintf(&q, "$id%d:String!", i)
		vars[fmt.Sprintf("id%d", i)] = id
	}
	q.WriteString("){")
	for i := range ids {
		fmt.Fprintf(&q, "i%d:issue(id:$id%d){%s relations(first:%d){nodes{type relatedIssue{%s}} pageInfo{hasNextPage endCursor}} inverseRelations(first:%d){nodes{type issue{%s}} pageInfo{hasNextPage endCursor}}}", i, i, issueFields, relationBatchPageSize, entityFields, relationBatchPageSize, entityFields)
	}
	q.WriteString("}")
	var result map[string]*issue
	if err := s.query(ctx, c, q.String(), vars, &result); err != nil {
		return nil, err
	}
	rows := make([]issue, 0, len(ids))
	for i, id := range ids {
		row := result[fmt.Sprintf("i%d", i)]
		if row == nil || row.ID != id || !s.within(*row) || row.Relations == nil || row.InverseRelations == nil {
			return nil, source.Refuse("unavailable", "incomplete Linear batch")
		}
		rows = append(rows, *row)
	}
	return rows, nil
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
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n\x00") {
		return model.Item{}, source.Refuse("invalid-id", "Linear identifier required")
	}
	row, err := s.detail(ctx, c, id)
	if err != nil {
		return model.Item{}, err
	}
	item, err := s.item(ctx, c, row)
	if err != nil {
		return model.Item{}, err
	}
	item.Comments, err = s.comments(ctx, c, row.ID)
	return item, err
}
func (s *Source) detail(ctx context.Context, c remote.Credential, id string) (issue, error) {
	var r struct{ Issue *issue }
	if err := s.query(ctx, c, detailQuery, map[string]any{"id": id}, &r); err != nil {
		return issue{}, err
	}
	if r.Issue == nil {
		return issue{}, source.Refuse("not-found", "Linear issue unavailable")
	}
	if !s.within(*r.Issue) || (r.Issue.Identifier != id && r.Issue.ID != id) {
		return issue{}, source.Refuse("unavailable", "Linear issue outside source")
	}
	return *r.Issue, nil
}
func (s *Source) ref(e *entity) (model.Ref, error) {
	if e == nil || !uuid(e.ID) || e.Identifier == "" || e.Team == nil || !uuid(e.Team.ID) {
		return model.Ref{}, source.Refuse("unavailable", "incomplete Linear relation identity")
	}
	alias := s.o.ID
	if e.Team.ID != s.o.Team || s.o.Project != "" && (e.Project == nil || e.Project.ID != s.o.Project) {
		alias = "linear/" + s.o.Organization + "/" + e.Team.ID
		if e.Project != nil {
			alias += "/" + e.Project.ID
		}
	}
	return model.Ref{Source: alias, ID: e.Identifier}, nil
}
func (s *Source) item(ctx context.Context, c remote.Credential, row issue) (model.Item, error) {
	if !s.within(row) || row.State == nil || !uuid(row.State.ID) || row.Priority < 0 || row.Priority > 4 {
		return model.Item{}, source.Refuse("unavailable", "invalid Linear issue")
	}
	item := model.Item{Ref: model.Ref{Source: s.o.ID, ID: row.Identifier}, Title: row.Title, Type: "issue", Description: row.Description, Status: row.State.ID, URL: row.URL, Created: row.CreatedAt, Updated: row.UpdatedAt, Fields: map[string]any{"uuid": row.ID, "organizationID": s.o.Organization, "teamID": s.o.Team, "stateName": row.State.Name, "stateType": row.State.Type, "priority": row.Priority, "archived": row.ArchivedAt != nil || row.Trashed}}
	switch row.State.Type {
	case "backlog":
		item.Category = model.Backlog
	case "unstarted":
		item.Category = model.Todo
	case "started":
		item.Category = model.Active
	case "completed":
		item.Category = model.Done
	case "canceled", "duplicate":
		item.Category = model.Canceled
	default:
		return model.Item{}, source.Refuse("unavailable", "unknown Linear state type")
	}
	if row.ArchivedAt != nil || row.Trashed {
		item.Category = model.Backlog
	}
	item.Priority = []model.Priority{"", model.Urgent, model.High, model.Medium, model.Low}[row.Priority]
	if row.Project != nil {
		item.Fields["projectID"] = row.Project.ID
	}
	if row.Assignee != nil {
		if !uuid(row.Assignee.ID) {
			return model.Item{}, source.Refuse("unavailable", "invalid Linear assignee")
		}
		item.Assignees = []string{row.Assignee.ID}
	}
	if row.Labels.Nodes == nil || row.Labels.PageInfo == nil || row.Labels.PageInfo.HasNextPage {
		return model.Item{}, source.Refuse("unavailable", "Linear labels exceed complete read limit")
	}
	labelIDs := []string{}
	for _, l := range row.Labels.Nodes {
		if !uuid(l.ID) || l.Name == "" {
			return model.Item{}, source.Refuse("unavailable", "invalid Linear label")
		}
		item.Labels = append(item.Labels, l.Name)
		labelIDs = append(labelIDs, l.ID)
	}
	sort.Strings(item.Labels)
	sort.Strings(labelIDs)
	item.Fields["labelIDs"] = labelIDs
	if row.Parent != nil {
		ref, err := s.ref(row.Parent)
		if err != nil {
			return model.Item{}, err
		}
		item.Parent = &ref
		item.Edges = append(item.Edges, model.Edge{Kind: model.Parent, Target: ref})
	}
	for _, direction := range []string{"out", "in"} {
		seen := map[string]bool{}
		vars := map[string]any{"id": row.ID, "count": 250}
		q := relationsQuery
		if direction == "in" {
			q = inverseQuery
		}
		for page := 0; ; page++ {
			if page >= 1000 {
				return model.Item{}, source.Refuse("unavailable", "Linear relation page budget exceeded")
			}
			var r struct {
				Issue *struct {
					ID                          string
					Relations, InverseRelations *connection[relation]
				}
			}
			initial := row.Relations
			if direction == "in" {
				initial = row.InverseRelations
			}
			if page == 0 && initial != nil {
				r.Issue = &struct {
					ID                          string
					Relations, InverseRelations *connection[relation]
				}{row.ID, row.Relations, row.InverseRelations}
			} else if err := s.query(ctx, c, q, vars, &r); err != nil {
				return model.Item{}, err
			}
			if r.Issue == nil || r.Issue.ID != row.ID {
				return model.Item{}, source.Refuse("unavailable", "Linear relations unavailable")
			}
			conn := r.Issue.Relations
			if direction == "in" {
				conn = r.Issue.InverseRelations
			}
			if conn == nil || conn.Nodes == nil {
				return model.Item{}, source.Refuse("unavailable", "Linear relations missing")
			}
			for _, edge := range conn.Nodes {
				e := edge.RelatedIssue
				if direction == "in" {
					e = edge.Issue
				}
				ref, err := s.ref(e)
				if err != nil {
					return model.Item{}, err
				}
				switch edge.Type {
				case "blocks":
					if direction == "in" {
						item.BlockedBy = append(item.BlockedBy, ref)
						item.Edges = append(item.Edges, model.Edge{Kind: model.BlockedBy, Target: ref})
					} else {
						item.Blocks = append(item.Blocks, ref)
					}
				case "related", "similar", "duplicate":
					item.Related = append(item.Related, ref)
					item.Edges = append(item.Edges, model.Edge{Kind: model.Related, Target: ref})
				default:
					return model.Item{}, source.Refuse("unavailable", "unknown Linear relation type")
				}
			}
			next, err := advance(conn.PageInfo, seen)
			if err != nil {
				return model.Item{}, err
			}
			if next == "" {
				break
			}
			vars["after"] = next
		}
	}
	// Canonicalize unordered provider collections before fingerprinting. Comments
	// are loaded only for detail; their metadata is not part of this fingerprint.
	less := func(a, b model.Ref) bool { return a.Source+":"+a.ID < b.Source+":"+b.ID }
	for _, refs := range [][]model.Ref{item.BlockedBy, item.Blocks, item.Related} {
		sort.Slice(refs, func(i, j int) bool { return less(refs[i], refs[j]) })
	}
	sort.Slice(item.Edges, func(i, j int) bool {
		a, b := item.Edges[i], item.Edges[j]
		return string(a.Kind)+a.Target.Source+":"+a.Target.ID < string(b.Kind)+b.Target.Source+":"+b.Target.ID
	})
	raw, _ := json.Marshal(item)
	hash := sha256.Sum256(raw)
	item.Version = hex.EncodeToString(hash[:])
	return item, nil
}

var _ source.Source = (*Source)(nil)
