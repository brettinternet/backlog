// Package jira implements Jira Cloud REST v3; Data Center is not supported.
package jira

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
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
	ID, Site, JQL, CloudID                 string
	HardLinkTypes, UnsuccessfulResolutions []string
	Account                                project.AccountConfig
	AllowWrites                            bool
	HTTP                                   *http.Client
	Scheduler                              *remote.Scheduler
	Credentials                            *remote.Credentials
	Secrets                                *remote.Secrets
}
type Source struct {
	o      Options
	base   string
	client remote.Client
	mu     sync.Mutex
}

// CanonicalSite is also the claim identity: neither JQL nor project keys enter it.
func CanonicalSite(site string) (string, error) {
	u, err := url.Parse(site)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".atlassian.net") {
		return "", source.Refuse("rejected", "Jira requires an HTTPS Atlassian Cloud site origin")
	}
	return "https://" + strings.ToLower(u.Hostname()), nil
}
func New(o Options) (*Source, error) {
	site, err := CanonicalSite(o.Site)
	if err != nil {
		return nil, err
	}
	if o.ID == "" || strings.TrimSpace(o.JQL) == "" || o.Account.Email == "" || strings.ContainsAny(o.Account.Email, ":\r\n") || o.Account.OAuth || o.Secrets == nil || o.Credentials == nil {
		return nil, source.Refuse("unavailable", "Jira requires JQL, account email and API-token helper")
	}
	o.Site = site
	base := site
	if o.CloudID != "" {
		for _, c := range o.CloudID {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return nil, source.Refuse("rejected", "invalid Jira cloudId")
			}
		}
		base = "https://api.atlassian.com/ex/jira/" + o.CloudID
	}
	u, _ := url.Parse(base)
	return &Source{o: o, base: base, client: remote.Client{Host: u.Host, Account: o.Account.User, BasicUser: o.Account.Email, HTTP: o.HTTP, Scheduler: o.Scheduler, Secrets: o.Secrets}}, nil
}
func (s *Source) Info() source.Info {
	return source.Info{ID: s.o.ID, Kind: "jira", Display: s.o.Site, Capabilities: model.Capabilities{model.Set: s.o.AllowWrites, model.AppendComment: s.o.AllowWrites, model.Batch: s.o.AllowWrites}}
}
func (s *Source) request(ctx context.Context, c remote.Credential, method, path string, body, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return source.Refuse("rejected", "invalid Jira request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+"/rest/api/3/"+path, &buf)
	if err != nil {
		return source.Refuse("rejected", "invalid Jira request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-cache") // Every preflight must observe fresh content.
	r, err := s.client.Do(ctx, req, c)
	if err != nil {
		return err
	}
	if out != nil && json.Unmarshal(r.Body, out) != nil {
		return source.Refuse("unavailable", "invalid Jira response")
	}
	return nil
}
func (s *Source) authenticate(ctx context.Context) (remote.Credential, error) {
	return s.o.Credentials.Resolve(ctx, s.o.Account, func(ctx context.Context, c remote.Credential) (string, error) {
		if s.o.CloudID != "" {
			var info struct{ BaseURL string }
			if err := s.request(ctx, c, http.MethodGet, "serverInfo", nil, &info); err != nil {
				return "", err
			}
			site, err := CanonicalSite(info.BaseURL)
			if err != nil || site != s.o.Site {
				return "", source.Refuse("unavailable", "Jira cloudId does not match configured site")
			}
		}
		var r struct {
			AccountID string
			Active    bool
		}
		if err := s.request(ctx, c, http.MethodGet, "myself", nil, &r); err != nil {
			return "", err
		}
		if !r.Active {
			return "", source.Refuse("unavailable", "inactive Jira account")
		}
		return r.AccountID, nil
	})
}
func numeric(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return id[0] != '0'
}

type named struct{ ID, Name string }
type status struct {
	ID, Name       string
	StatusCategory struct{ Key string }
}
type issue struct {
	ID, Key string
	Fields  struct {
		Summary, Created, Updated string
		Description               json.RawMessage
		Status                    *status
		Resolution                *named
		Assignee                  *struct{ AccountID, DisplayName string }
		Labels                    []string
		IssueLinks                []struct {
			ID                        string
			Type                      named
			InwardIssue, OutwardIssue *struct{ ID, Key string }
		}
	}
}

const fields = "summary,description,status,resolution,assignee,labels,created,updated,issuelinks"

func (s *Source) Load(ctx context.Context, _ source.Cursor) (source.Delta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.authenticate(ctx)
	if err != nil {
		return source.Delta{}, err
	}
	d := source.Delta{Coverage: source.Partial, Findings: []source.Finding{{Code: "visible-scope", Severity: "warning", Message: "Jira search covers visible indexed issues only; omissions and estimates are not deletion evidence"}}}
	token := ""
	seen := map[string]bool{}
	rows := map[string]issue{}
	for page := 0; page < 1000; page++ {
		body := map[string]any{"jql": s.o.JQL, "fields": strings.Split(fields, ","), "maxResults": 100}
		if token != "" {
			body["nextPageToken"] = token
		}
		var r struct {
			Issues        []issue
			IsLast        *bool
			NextPageToken string
			Total         *int
		}
		if err = s.request(ctx, c, http.MethodPost, "search/jql", body, &r); err != nil {
			return source.Delta{}, err
		}
		if r.Issues == nil || r.IsLast == nil {
			return source.Delta{}, source.Refuse("unavailable", "missing Jira pagination evidence")
		}
		for _, row := range r.Issues {
			if !numeric(row.ID) {
				return source.Delta{}, source.Refuse("unavailable", "invalid Jira issue identity")
			}
			rows[row.ID] = row
		}
		if *r.IsLast {
			ids := make([]string, 0, len(rows))
			for id := range rows {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				item, err := s.item(ctx, c, rows[id])
				if err != nil {
					return source.Delta{}, err
				}
				d.Items = append(d.Items, item)
			}
			return d, nil
		}
		token = r.NextPageToken
		if token == "" || seen[token] {
			return source.Delta{}, source.Refuse("unavailable", "missing or repeated Jira page token")
		}
		seen[token] = true
	}
	return source.Delta{}, source.Refuse("unavailable", "Jira page budget exceeded")
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
	if !numeric(id) {
		return model.Item{}, source.Refuse("invalid-id", "use the numeric Jira issue ID")
	}
	var row issue
	if err := s.request(ctx, c, http.MethodGet, "issue/"+id+"?fields="+fields, nil, &row); err != nil {
		return model.Item{}, err
	}
	if row.ID != id {
		return model.Item{}, source.Refuse("unavailable", "Jira identity changed")
	}
	return s.item(ctx, c, row)
}

// ADF is rendered to readable text but the original document remains in Fields
// and the version fingerprint. Comments are written as plain-text ADF, not HTML.
func adfText(raw json.RawMessage) string {
	var node struct {
		Type, Text string
		Content    []json.RawMessage
	}
	if json.Unmarshal(raw, &node) != nil {
		return ""
	}
	out := node.Text
	for _, child := range node.Content {
		out += adfText(child)
	}
	if node.Type == "paragraph" || node.Type == "heading" || node.Type == "hardBreak" {
		out += "\n"
	}
	return out
}

type transition struct {
	ID, Name string
	To       *status
	Fields   map[string]struct {
		Required        bool
		HasDefaultValue bool
	}
}

func (s *Source) transitions(ctx context.Context, c remote.Credential, id string) (map[string]any, error) {
	var r struct{ Transitions []transition }
	if err := s.request(ctx, c, http.MethodGet, "issue/"+id+"/transitions?expand=transitions.fields", nil, &r); err != nil {
		return nil, err
	}
	if r.Transitions == nil {
		return nil, source.Refuse("unavailable", "missing Jira transitions")
	}
	choices := map[string]any{}
	ambiguous := map[string]bool{}
	for _, t := range r.Transitions {
		if !numeric(t.ID) || t.To == nil || t.To.Name == "" || t.Fields == nil {
			continue
		}
		required := false
		for _, f := range t.Fields {
			required = required || f.Required && !f.HasDefaultValue
		}
		if required {
			continue
		}
		name := t.To.Name
		if _, exists := choices[name]; exists {
			ambiguous[name] = true
		}
		category, ok := map[string]model.StatusCategory{"new": model.Todo, "indeterminate": model.Active, "done": model.Done}[t.To.StatusCategory.Key]
		if !ok {
			continue
		}
		choices[name] = map[string]any{"id": t.ID, "category": string(category)}
	}
	for name := range ambiguous {
		delete(choices, name)
	}
	return choices, nil
}
func (s *Source) item(ctx context.Context, c remote.Credential, row issue) (model.Item, error) {
	if !numeric(row.ID) || row.Key == "" || row.Fields.Status == nil || row.Fields.Status.Name == "" || row.Fields.Updated == "" {
		return model.Item{}, source.Refuse("unavailable", "incomplete Jira issue")
	}
	f := row.Fields
	item := model.Item{Ref: model.Ref{Source: s.o.ID, ID: row.ID}, Title: f.Summary, Type: "issue", Status: f.Status.Name, Description: strings.TrimSpace(adfText(f.Description)), URL: s.o.Site + "/browse/" + url.PathEscape(row.Key), Labels: slices.Clone(f.Labels), Fields: map[string]any{"id": row.ID, "key": row.Key, "site": s.o.Site, "status": f.Status, "resolution": f.Resolution, "descriptionADF": f.Description}}
	switch f.Status.StatusCategory.Key {
	case "new":
		item.Category = model.Todo
	case "indeterminate":
		item.Category = model.Active
	case "done":
		item.Category = model.Done
	default:
		return model.Item{}, source.Refuse("unavailable", "unknown Jira status category")
	}
	if item.Category == model.Done && f.Resolution != nil {
		unsuccessful := []string{"won't do", "won't fix", "duplicate", "cannot reproduce", "declined", "canceled", "cancelled"}
		for _, name := range append(unsuccessful, s.o.UnsuccessfulResolutions...) {
			if strings.EqualFold(name, f.Resolution.Name) || name == f.Resolution.ID {
				item.Category = model.Canceled
			}
		}
	}
	if f.Assignee != nil {
		if f.Assignee.AccountID == "" {
			return model.Item{}, source.Refuse("unavailable", "invalid Jira assignee")
		}
		item.Assignees = []string{f.Assignee.AccountID}
		if f.Assignee.DisplayName != "" {
			item.Names = map[string]string{f.Assignee.AccountID: f.Assignee.DisplayName}
		}
	}
	if err := timestamp(&item.Updated, f.Updated); err != nil {
		return model.Item{}, source.Refuse("unavailable", "invalid Jira timestamp")
	}
	if f.Created != "" {
		if err := timestamp(&item.Created, f.Created); err != nil {
			return model.Item{}, source.Refuse("unavailable", "invalid Jira timestamp")
		}
	}
	sort.Strings(item.Labels)
	sort.Slice(f.IssueLinks, func(i, j int) bool { return f.IssueLinks[i].ID < f.IssueLinks[j].ID })
	item.Fields["links"] = f.IssueLinks
	for _, link := range f.IssueLinks {
		if !slices.Contains(s.o.HardLinkTypes, link.Type.ID) && !slices.Contains(s.o.HardLinkTypes, link.Type.Name) {
			continue
		}
		if link.InwardIssue != nil {
			if !numeric(link.InwardIssue.ID) {
				return model.Item{}, source.Refuse("unavailable", "missing Jira link identity")
			}
			ref := model.Ref{Source: s.o.ID, ID: link.InwardIssue.ID}
			item.BlockedBy = append(item.BlockedBy, ref)
			item.Edges = append(item.Edges, model.Edge{Kind: model.BlockedBy, Target: ref})
		}
		if link.OutwardIssue != nil {
			if !numeric(link.OutwardIssue.ID) {
				return model.Item{}, source.Refuse("unavailable", "missing Jira link identity")
			}
			item.Blocks = append(item.Blocks, model.Ref{Source: s.o.ID, ID: link.OutwardIssue.ID})
		}
	}
	// Include complete comment state in versions, not a possibly truncated embedded page.
	rawComments := []json.RawMessage{}
	for start, page := 0, 0; page < 1000; page++ {
		var r struct {
			StartAt, Total int
			Comments       []json.RawMessage
		}
		if err := s.request(ctx, c, http.MethodGet, fmt.Sprintf("issue/%s/comment?startAt=%d&maxResults=100", row.ID, start), nil, &r); err != nil {
			return model.Item{}, err
		}
		if r.Comments == nil || r.StartAt != start || r.Total < start+len(r.Comments) {
			return model.Item{}, source.Refuse("unavailable", "incomplete Jira comments")
		}
		rawComments = append(rawComments, r.Comments...)
		for _, raw := range r.Comments {
			var comment struct {
				Body    json.RawMessage
				Created string
				Author  struct{ AccountID string }
			}
			if json.Unmarshal(raw, &comment) != nil {
				return model.Item{}, source.Refuse("unavailable", "invalid Jira comment")
			}
			parsed := model.Comment{Author: comment.Author.AccountID, Body: strings.TrimSpace(adfText(comment.Body))}
			if err := timestamp(&parsed.Time, comment.Created); err != nil {
				return model.Item{}, source.Refuse("unavailable", "invalid Jira comment time")
			}
			item.Comments = append(item.Comments, parsed)
		}
		start += len(r.Comments)
		if start == r.Total {
			break
		}
		if len(r.Comments) == 0 || page == 999 {
			return model.Item{}, source.Refuse("unavailable", "Jira comment page budget exceeded")
		}
	}
	item.Fields["commentsADF"] = rawComments
	choices, err := s.transitions(ctx, c, row.ID)
	if err != nil {
		return model.Item{}, err
	}
	item.Fields["jiraTransitions"] = choices
	raw, _ := json.Marshal(item)
	sum := sha256.Sum256(raw)
	item.Version = hex.EncodeToString(sum[:])
	return item, nil
}
func timestamp(out *time.Time, value string) error {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			*out = parsed
			return nil
		}
	}
	return fmt.Errorf("invalid Jira timestamp")
}

var _ source.Source = (*Source)(nil)
