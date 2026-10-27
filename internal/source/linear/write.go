package linear

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

const commentsQuery = `query($id:String!,$after:String,$count:Int!){issue(id:$id){id comments(first:$count,after:$after){nodes{id body createdAt user{id}} pageInfo{hasNextPage endCursor}}}}`

func (s *Source) comments(ctx context.Context, c remote.Credential, id string) ([]model.Comment, error) {
	var comments []model.Comment
	seen := map[string]bool{}
	vars := map[string]any{"id": id, "count": 250}
	for page := 0; page < 1000; page++ {
		var r struct {
			Issue *struct {
				ID       string
				Comments *connection[struct {
					ID, Body  string
					CreatedAt string
					User      *struct{ ID string }
				}]
			}
		}
		if err := s.query(ctx, c, commentsQuery, vars, &r); err != nil {
			return nil, err
		}
		if r.Issue == nil || r.Issue.ID != id || r.Issue.Comments == nil || r.Issue.Comments.Nodes == nil {
			return nil, source.Refuse("unavailable", "Linear comments unavailable")
		}
		for _, comment := range r.Issue.Comments.Nodes {
			if !uuid(comment.ID) {
				return nil, source.Refuse("unavailable", "invalid Linear comment")
			}
			author := ""
			if comment.User != nil {
				author = comment.User.ID
			}
			var parsed model.Comment
			parsed.Author = author
			parsed.Body = comment.Body
			if comment.CreatedAt != "" {
				if err := parsed.Time.UnmarshalText([]byte(comment.CreatedAt)); err != nil {
					return nil, source.Refuse("unavailable", "invalid Linear comment timestamp")
				}
			}
			comments = append(comments, parsed)
		}
		next, err := advance(r.Issue.Comments.PageInfo, seen)
		if err != nil {
			return nil, err
		}
		if next == "" {
			return comments, nil
		}
		vars["after"] = next
	}
	return nil, source.Refuse("unavailable", "Linear comment page budget exceeded")
}

const statesQuery = `query($team:String!,$after:String,$count:Int!){team(id:$team){id states(first:$count,after:$after){nodes{id name type} pageInfo{hasNextPage endCursor}}}}`
const labelsQuery = `query($team:ID!,$after:String,$count:Int!){issueLabels(first:$count,after:$after,filter:{or:[{team:{id:{eq:$team}}},{team:{null:true}}]}){nodes{id name} pageInfo{hasNextPage endCursor}}}`
const updateMutation = `mutation($id:String!,$input:IssueUpdateInput!){issueUpdate(id:$id,input:$input){success issue{id}}}`
const commentMutation = `mutation($issueId:String!,$body:String!){commentCreate(input:{issueId:$issueId,body:$body}){success comment{id}}}`
const archiveMutation = `mutation($id:String!){issueArchive(id:$id){success entity{id}}}`
const unarchiveMutation = `mutation($id:String!){issueUnarchive(id:$id){success entity{id}}}`
const createMutation = `mutation($input:IssueCreateInput!){issueCreate(input:$input){success issue{id identifier}}}`
const relationMutation = `mutation($input:IssueRelationCreateInput!){issueRelationCreate(input:$input){success issueRelation{id}}}`

func stringsValue(value any) ([]string, bool) {
	switch v := value.(type) {
	case []string:
		return append([]string{}, v...), true
	case []any:
		out := []string{}
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case string:
		if v == "" {
			return []string{}, true
		}
		return strings.Split(v, ","), true
	}
	return nil, false
}

// StatusChoices reports the bound team's states.
func (s *Source) StatusChoices(ctx context.Context, _ model.Item) ([]model.StatusChoice, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.authenticate(ctx)
	if err != nil {
		return nil, false, err
	}
	states, err := s.teamStates(ctx, c)
	return states, err == nil, err
}

// teamStates lists the bound team's states with their categories.
func (s *Source) teamStates(ctx context.Context, c remote.Credential) ([]model.StatusChoice, error) {
	states := []model.StatusChoice{}
	seen := map[string]bool{}
	vars := map[string]any{"team": s.o.Team, "count": 250}
	for page := 0; page < 1000; page++ {
		var r struct {
			Team *struct {
				ID     string
				States *connection[struct{ ID, Name, Type string }]
			}
		}
		if err := s.query(ctx, c, statesQuery, vars, &r); err != nil {
			return nil, err
		}
		if r.Team == nil || r.Team.ID != s.o.Team || r.Team.States == nil || r.Team.States.Nodes == nil {
			return nil, source.Refuse("rejected", "Linear field or value unavailable")
		}
		for _, state := range r.Team.States.Nodes {
			if category, ok := stateCategory(state.Type); ok && uuid(state.ID) {
				states = append(states, model.StatusChoice{Key: state.ID, Category: category})
			}
		}
		next, err := advance(r.Team.States.PageInfo, seen)
		if err != nil {
			return nil, err
		}
		if next == "" {
			return states, nil
		}
		vars["after"] = next
	}
	return nil, source.Refuse("rejected", "Linear field or value unavailable")
}

// resolveField validates provider catalogs before the fresh version preflight,
// leaving only one non-retried mutation after that read (Linear has no CAS).
func (s *Source) resolveField(ctx context.Context, c remote.Credential, m model.Mutation) (map[string]any, error) {
	reject := func() (map[string]any, error) {
		return nil, source.Refuse("rejected", "Linear field or value unavailable")
	}
	switch m.Field {
	case "status":
		id, ok := m.Value.(string)
		if !ok || !uuid(id) {
			return reject()
		}
		states, err := s.teamStates(ctx, c)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(states, func(state model.StatusChoice) bool { return state.Key == id }) {
			return reject()
		}
		return map[string]any{"stateId": id}, nil
	case "assignees":
		v, ok := stringsValue(m.Value)
		if !ok || len(v) > 1 {
			return reject()
		}
		if len(v) == 0 {
			return map[string]any{"assigneeId": nil}, nil
		}
		if !uuid(v[0]) {
			return reject()
		}
		return map[string]any{"assigneeId": v[0]}, nil
	case "priority":
		var priority model.Priority
		switch v := m.Value.(type) {
		case string:
			priority = model.Priority(v)
		case model.Priority:
			priority = v
		default:
			return reject()
		}
		n, ok := map[model.Priority]int{"": 0, model.Urgent: 1, model.High: 2, model.Medium: 3, model.Low: 4}[priority]
		if !ok {
			return reject()
		}
		return map[string]any{"priority": n}, nil
	case "sprint":
		// The engine resolved the cycle from the catalog; Linear rejects a
		// cycle from another team.
		id, ok := m.Value.(string)
		if !ok || id != "" && !uuid(id) || m.ExpectedSprint == nil {
			return reject()
		}
		if id == "" {
			return map[string]any{"cycleId": nil}, nil
		}
		return map[string]any{"cycleId": id}, nil
	case "labels":
		names, ok := stringsValue(m.Value)
		if !ok {
			return reject()
		}
		wanted := map[string]bool{}
		for _, name := range names {
			if name == "" || wanted[name] {
				return reject()
			}
			wanted[name] = true
		}
		ids := []string{}
		matches := map[string]string{}
		seen := map[string]bool{}
		vars := map[string]any{"team": s.o.Team, "count": 250}
		for page := 0; page < 1000; page++ {
			var r struct {
				IssueLabels *connection[struct{ ID, Name string }]
			}
			if err := s.query(ctx, c, labelsQuery, vars, &r); err != nil {
				return nil, err
			}
			if r.IssueLabels == nil || r.IssueLabels.Nodes == nil {
				return reject()
			}
			for _, label := range r.IssueLabels.Nodes {
				if wanted[label.Name] {
					if !uuid(label.ID) || matches[label.Name] != "" && matches[label.Name] != label.ID {
						return reject()
					}
					matches[label.Name] = label.ID
				}
			}
			next, err := advance(r.IssueLabels.PageInfo, seen)
			if err != nil {
				return nil, err
			}
			if next == "" {
				for _, name := range names {
					if matches[name] == "" {
						return reject()
					}
					ids = append(ids, matches[name])
				}
				return map[string]any{"labelIds": ids}, nil
			}
			vars["after"] = next
		}
		return reject()
	}
	return reject()
}
func (s *Source) Apply(ctx context.Context, m model.Mutation) (model.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.Ref.Source != s.o.ID || !s.o.AllowWrites || (m.Op != model.Create && m.Op != model.Set && m.Op != model.AppendComment && m.Op != model.Archive && m.Op != model.Unarchive) {
		return model.Item{}, source.Refuse("rejected", "Linear write capability unavailable")
	}
	text := m.Comment.Body
	if text == "" {
		text = m.Text
	}
	if m.Op == model.AppendComment && strings.TrimSpace(text) == "" {
		return model.Item{}, source.Refuse("rejected", "comment body required")
	}
	if m.Op == model.Set && m.Field != "status" && m.Field != "assignees" && m.Field != "priority" && m.Field != "labels" && m.Field != "sprint" {
		return model.Item{}, source.Refuse("rejected", "Linear field unavailable")
	}
	c, err := s.authenticate(ctx)
	if err != nil {
		return model.Item{}, err
	}
	if !s.Info().Capabilities.Allows(m.Op) {
		return model.Item{}, source.Refuse("rejected", "Linear write scope required")
	}
	if !s.active {
		return model.Item{}, source.Refuse("rejected", "active Linear principal required")
	}
	if m.Op == model.Create {
		return s.create(ctx, c, m.Item)
	}
	if m.Field == "sprint" {
		// Versions exclude the cycle, so the preflight must read it.
		s.sprints.Store(true)
	}
	var input map[string]any
	if m.Op == model.Set {
		input, err = s.resolveField(ctx, c, m)
		if err != nil {
			return model.Item{}, err
		}
	}
	row, current, err := s.readRow(ctx, c, m.Ref.ID)
	if err != nil {
		return model.Item{}, err
	}
	if m.ExpectedVersion == "" || m.ExpectedVersion != current.Version {
		return model.Item{}, source.Refuse("conflict", "Linear issue or relations changed")
	}
	if m.Field == "sprint" && current.Sprint != *m.ExpectedSprint {
		return model.Item{}, source.Refuse("conflict", "Linear cycle changed")
	}
	if row.Trashed {
		return model.Item{}, source.Refuse("rejected", "trashed Linear issue cannot be written")
	}
	archived := row.ArchivedAt != nil
	if m.Op == model.Archive && archived || m.Op == model.Unarchive && !archived {
		return current, nil
	}
	if archived && m.Op != model.Unarchive {
		return model.Item{}, source.Refuse("rejected", "archived Linear issue cannot be written")
	}
	id := row.ID
	if m.Op == model.Archive || m.Op == model.Unarchive {
		var r struct {
			IssueArchive, IssueUnarchive *struct {
				Success bool
				Entity  *struct{ ID string }
			}
		}
		q := archiveMutation
		if m.Op == model.Unarchive {
			q = unarchiveMutation
		}
		err = s.query(ctx, c, q, map[string]any{"id": id}, &r)
		payload := r.IssueArchive
		if m.Op == model.Unarchive {
			payload = r.IssueUnarchive
		}
		if err == nil && (payload == nil || !payload.Success || payload.Entity == nil || payload.Entity.ID != id) {
			err = source.Refuse("unavailable", "Linear archive outcome unknown; refresh before further edits")
		}
	} else if m.Op == model.AppendComment {
		var r struct {
			CommentCreate *struct {
				Success bool
				Comment *struct{ ID string }
			}
		}
		err = s.query(ctx, c, commentMutation, map[string]any{"issueId": id, "body": text}, &r)
		if err == nil && (r.CommentCreate == nil || !r.CommentCreate.Success || r.CommentCreate.Comment == nil || !uuid(r.CommentCreate.Comment.ID)) {
			err = source.Refuse("unavailable", "Linear comment outcome unknown; refresh before further edits")
		}
	} else {
		var r struct {
			IssueUpdate *struct {
				Success bool
				Issue   *struct{ ID string }
			}
		}
		err = s.query(ctx, c, updateMutation, map[string]any{"id": id, "input": input}, &r)
		if err == nil && (r.IssueUpdate == nil || !r.IssueUpdate.Success || r.IssueUpdate.Issue == nil || r.IssueUpdate.Issue.ID != id) {
			err = source.Refuse("unavailable", "Linear write outcome unknown; refresh before further edits")
		}
	}
	if err != nil {
		return model.Item{}, err
	}
	return s.read(ctx, c, m.Ref.ID)
}

const entityQuery = `query($id:String!){issue(id:$id){` + entityFields + `}}`

// issueID resolves a parent or blocker ref to the UUID of the Linear issue it
// names, in this source or under its out-of-scope alias. Other sources refuse.
func (s *Source) issueID(ctx context.Context, c remote.Credential, ref model.Ref) (string, error) {
	if ref.Source == "" {
		ref.Source = s.o.ID
	}
	if ref.Source != s.o.ID && !strings.HasPrefix(ref.Source, "linear/"+s.o.Organization+"/") || ref.ID == "" || len(ref.ID) > 128 || strings.ContainsAny(ref.ID, "\r\n\x00") {
		return "", source.Refuse("rejected", "Linear parent and blockers must be Linear issues")
	}
	var r struct{ Issue *entity }
	if err := s.query(ctx, c, entityQuery, map[string]any{"id": ref.ID}, &r); err != nil {
		return "", err
	}
	if r.Issue == nil {
		return "", source.Refuse("not-found", "Linear parent or blocker unavailable")
	}
	resolved, err := s.ref(r.Issue)
	if err != nil {
		return "", err
	}
	if resolved != ref {
		return "", source.Refuse("rejected", "Linear parent or blocker outside its source")
	}
	return r.Issue.ID, nil
}

// create has no version to preflight. Catalog and ref checks run before the
// single non-retried issueCreate; any failure after dispatch has an unknown
// outcome. Blocker relations need one more non-retried mutation each, so a
// failure after the issue exists reports it as Created.
func (s *Source) create(ctx context.Context, c remote.Credential, item *model.Item) (model.Item, error) {
	if item == nil || strings.TrimSpace(item.Title) == "" {
		return model.Item{}, source.Refuse("rejected", "Linear create requires a title")
	}
	input := map[string]any{"teamId": s.o.Team, "title": item.Title}
	if item.Parent != nil {
		id, err := s.issueID(ctx, c, *item.Parent)
		if err != nil {
			return model.Item{}, err
		}
		input["parentId"] = id
	}
	var blockers []string
	for _, ref := range item.BlockedBy {
		id, err := s.issueID(ctx, c, ref)
		if err != nil {
			return model.Item{}, err
		}
		if !slices.Contains(blockers, id) {
			blockers = append(blockers, id)
		}
	}
	if item.Description != "" {
		input["description"] = item.Description
	}
	if s.o.Project != "" {
		input["projectId"] = s.o.Project
	}
	fields := []model.Mutation{{Field: "assignees", Value: item.Assignees}, {Field: "priority", Value: item.Priority}}
	if item.Status != "" {
		fields = append(fields, model.Mutation{Field: "status", Value: item.Status})
	}
	if len(item.Labels) > 0 {
		fields = append(fields, model.Mutation{Field: "labels", Value: item.Labels})
	}
	if item.Sprint != "" {
		// Read the cycle back so the result carries it.
		s.sprints.Store(true)
		fields = append(fields, model.Mutation{Field: "sprint", Value: item.Sprint, ExpectedSprint: new("")})
	}
	for _, f := range fields {
		resolved, err := s.resolveField(ctx, c, f)
		if err != nil {
			return model.Item{}, err
		}
		for k, v := range resolved {
			if v != nil {
				input[k] = v
			}
		}
	}
	var r struct {
		IssueCreate *struct {
			Success bool
			Issue   *struct{ ID, Identifier string }
		}
	}
	if err := s.query(ctx, c, createMutation, map[string]any{"input": input}, &r); err != nil {
		// A scheduler refusal or rate limit proves nothing was sent.
		var limited remote.ScheduleDiagnostic
		if errors.As(err, &limited) {
			return model.Item{}, err
		}
		var diagnostic *oauth.Diagnostic
		if errors.As(err, &diagnostic) && diagnostic.Code == oauth.CodeRateLimited {
			return model.Item{}, err
		}
		return model.Item{}, source.Refuse("unknown-outcome", "Linear create unconfirmed; refresh before creating again")
	}
	if r.IssueCreate == nil || !r.IssueCreate.Success || r.IssueCreate.Issue == nil || !uuid(r.IssueCreate.Issue.ID) || r.IssueCreate.Issue.Identifier == "" {
		return model.Item{}, source.Refuse("unknown-outcome", "Linear create unconfirmed; refresh before creating again")
	}
	partial := &source.Error{Reason: "unknown-outcome", Message: "Linear issue " + r.IssueCreate.Issue.Identifier + " created but blocker relations unconfirmed", Created: &model.Ref{Source: s.o.ID, ID: r.IssueCreate.Issue.Identifier}}
	for _, blocker := range blockers {
		var rel struct {
			IssueRelationCreate *struct {
				Success       bool
				IssueRelation *struct{ ID string }
			}
		}
		err := s.query(ctx, c, relationMutation, map[string]any{"input": map[string]any{"type": "blocks", "issueId": blocker, "relatedIssueId": r.IssueCreate.Issue.ID}}, &rel)
		if err != nil || rel.IssueRelationCreate == nil || !rel.IssueRelationCreate.Success || rel.IssueRelationCreate.IssueRelation == nil || !uuid(rel.IssueRelationCreate.IssueRelation.ID) {
			return model.Item{}, partial
		}
	}
	created, err := s.read(ctx, c, r.IssueCreate.Issue.Identifier)
	if err != nil {
		partial.Message = "Linear issue " + r.IssueCreate.Issue.Identifier + " created but refresh failed"
		return model.Item{}, partial
	}
	return created, nil
}
