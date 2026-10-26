package jira

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

func assignees(value any) ([]string, bool) {
	switch v := value.(type) {
	case []string:
		return v, true
	case string:
		if v == "" {
			return nil, true
		}
		return strings.Split(v, ","), true
	case []any:
		out := []string{}
		for _, entry := range v {
			s, ok := entry.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}
func (s *Source) Apply(ctx context.Context, m model.Mutation) (model.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.Op == model.Batch {
		if len(m.Changes) != 1 {
			return model.Item{}, source.Refuse("rejected", "Jira supports one mutation per request, not atomic batches")
		}
		change := m.Changes[0]
		change.Ref = m.Ref
		change.ExpectedVersion = m.ExpectedVersion
		change.Confirmed = m.Confirmed
		m = change
	}
	if m.Ref.Source != s.o.ID || !s.o.AllowWrites || !numeric(m.Ref.ID) || (m.Op != model.Set && m.Op != model.AppendComment) {
		return model.Item{}, source.Refuse("rejected", "Jira write unavailable")
	}
	if m.Op == model.Set && m.Field != "status" && m.Field != "assignees" && m.Field != "sprint" {
		return model.Item{}, source.Refuse("rejected", "Jira supports only status, assignee and sprint replacement")
	}
	text := m.Comment.Body
	if text == "" {
		text = m.Text
	}
	if m.Op == model.AppendComment && strings.TrimSpace(text) == "" {
		return model.Item{}, source.Refuse("rejected", "comment body required")
	}
	var replacement []string
	if m.Op == model.Set && m.Field == "assignees" {
		var ok bool
		replacement, ok = assignees(m.Value)
		if !ok || len(replacement) > 1 || len(replacement) == 1 && strings.TrimSpace(replacement[0]) == "" {
			return model.Item{}, source.Refuse("rejected", "Jira accepts zero or one assignee accountId")
		}
		if !m.Confirmed {
			return model.Item{}, source.Refuse("rejected", "Jira assignee replacement requires --yes or MCP confirm")
		}
	}
	var sprintID string
	if m.Op == model.Set && m.Field == "sprint" {
		key, ok := m.Value.(string)
		id, found := strings.CutPrefix(key, strings.TrimSuffix(s.sprintKey(0), "0"))
		if !ok || key != "" && (!found || !numeric(id)) || m.ExpectedSprint == nil {
			return model.Item{}, source.Refuse("rejected", "Jira sprint must be a sprint key from this site")
		}
		sprintID = id
	}
	c, err := s.authenticate(ctx)
	if err != nil {
		return model.Item{}, err
	}
	if m.Field == "sprint" {
		// Versions exclude the sprint, so the preflight must read it.
		if err = s.discoverSprintField(ctx, c); err != nil {
			return model.Item{}, err
		}
		if s.sprintField == "" {
			return model.Item{}, source.Refuse("rejected", "Jira sprint field unavailable")
		}
	}
	current, err := s.read(ctx, c, m.Ref.ID)
	if err != nil {
		return model.Item{}, err
	}
	if m.ExpectedVersion == "" || m.ExpectedVersion != current.Version {
		return model.Item{}, source.Refuse("conflict", "Jira issue, links, comments or transitions changed")
	}
	if m.Field == "sprint" && current.Sprint != *m.ExpectedSprint {
		return model.Item{}, source.Refuse("conflict", "Jira sprint changed")
	}
	method, path := http.MethodPost, "issue/"+m.Ref.ID+"/comment"
	var body any
	if m.Op == model.AppendComment {
		paragraphs := []any{}
		for _, line := range strings.Split(text, "\n") {
			content := []any{}
			if line != "" {
				content = append(content, map[string]any{"type": "text", "text": line})
			}
			paragraphs = append(paragraphs, map[string]any{"type": "paragraph", "content": content})
		}
		body = map[string]any{"body": map[string]any{"type": "doc", "version": 1, "content": paragraphs}}
	} else if m.Field == "assignees" {
		method, path = http.MethodPut, "issue/"+m.Ref.ID+"/assignee"
		var id any
		if len(replacement) == 1 {
			id = replacement[0]
		}
		body = map[string]any{"accountId": id}
	} else if m.Field == "sprint" {
		// The Agile API is on the same host, so it needs no further trust.
		path = "/rest/agile/1.0/backlog/issue"
		if sprintID != "" {
			path = "/rest/agile/1.0/sprint/" + sprintID + "/issue"
		}
		body = map[string]any{"issues": []string{m.Ref.ID}}
	} else {
		target, ok := m.Value.(string)
		choices, _ := current.Fields["jiraTransitions"].(map[string]any)
		transition, found := choices[target]
		if !ok || !found {
			return model.Item{}, source.Refuse("rejected", "status has no discovered field-free transition for this issue")
		}
		path = "issue/" + m.Ref.ID + "/transitions"
		details, _ := transition.(map[string]any)
		body = map[string]any{"transition": map[string]any{"id": details["id"]}}
	}
	// Jira exposes no established CAS token. Never retry a potentially applied write.
	if err = s.request(ctx, c, method, path, body, nil); err != nil {
		// Scheduler refusals and rate-limit responses prove the write was not applied.
		var limited remote.ScheduleDiagnostic
		if errors.As(err, &limited) {
			return model.Item{}, err
		}
		return model.Item{}, source.Refuse("unknown-outcome", "Jira write unconfirmed; refresh before further edits")
	}
	result, err := s.read(ctx, c, m.Ref.ID)
	if err != nil {
		return model.Item{}, source.Refuse("unknown-outcome", "Jira write accepted but refresh failed")
	}
	return result, nil
}
