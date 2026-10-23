package linear

import (
	"context"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
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
				return reject()
			}
			for _, state := range r.Team.States.Nodes {
				if state.ID == id {
					return map[string]any{"stateId": id}, nil
				}
			}
			next, err := advance(r.Team.States.PageInfo, seen)
			if err != nil {
				return nil, err
			}
			if next == "" {
				break
			}
			vars["after"] = next
		}
		return reject()
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
	if m.Ref.Source != s.o.ID || !s.Info().Capabilities.Allows(m.Op) {
		return model.Item{}, source.Refuse("rejected", "Linear write capability unavailable")
	}
	text := m.Comment.Body
	if text == "" {
		text = m.Text
	}
	if m.Op == model.AppendComment && strings.TrimSpace(text) == "" {
		return model.Item{}, source.Refuse("rejected", "comment body required")
	}
	if m.Op == model.Set && m.Field != "status" && m.Field != "assignees" && m.Field != "priority" && m.Field != "labels" {
		return model.Item{}, source.Refuse("rejected", "Linear field unavailable")
	}
	c, err := s.authenticate(ctx)
	if err != nil {
		return model.Item{}, err
	}
	if !s.active {
		return model.Item{}, source.Refuse("rejected", "active Linear principal required")
	}
	var input map[string]any
	if m.Op == model.Set {
		input, err = s.resolveField(ctx, c, m)
		if err != nil {
			return model.Item{}, err
		}
	}
	current, err := s.read(ctx, c, m.Ref.ID)
	if err != nil {
		return model.Item{}, err
	}
	if m.ExpectedVersion == "" || m.ExpectedVersion != current.Version {
		return model.Item{}, source.Refuse("conflict", "Linear issue or relations changed")
	}
	if current.Fields["archived"] == true {
		return model.Item{}, source.Refuse("rejected", "archived or trashed Linear issue cannot be written")
	}
	id, _ := current.Fields["uuid"].(string)
	if m.Op == model.AppendComment {
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
