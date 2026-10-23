package github

import (
	"context"
	"net/http"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

// Apply checks a fresh composite version immediately before a single mutation.
// GitHub offers no compare-and-swap for these APIs: another writer may still
// race this preflight. Never retry a mutation, including after an uncertain error.
func (s *Source) Apply(ctx context.Context, m model.Mutation) (model.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.Ref.Source != s.o.ID || !s.Info().Capabilities.Allows(m.Op) {
		return model.Item{}, source.Refuse("rejected", "GitHub operation unavailable")
	}
	if _, err := validNumber(m.Ref.ID); err != nil {
		return model.Item{}, err
	}
	var payload any
	method, endpoint := http.MethodPatch, s.issuePath(m.Ref.ID)
	projectWrite := false
	switch m.Op {
	case model.AppendComment:
		text := m.Comment.Body
		if text == "" {
			text = m.Text
		}
		if strings.TrimSpace(text) == "" {
			return model.Item{}, source.Refuse("rejected", "comment body required")
		}
		method, endpoint, payload = http.MethodPost, endpoint+"/comments", map[string]any{"body": text}
	case model.Set:
		switch m.Field {
		case "status":
			v, ok := m.Value.(string)
			if !ok {
				return model.Item{}, source.Refuse("rejected", "status must be a string")
			}
			if v != "open" && v != "closed" {
				if s.o.Project == nil || !s.o.Project.AllowWrites || s.o.Project.Options[v] == "" {
					return model.Item{}, source.Refuse("rejected", "open, closed or a writable configured project option ID required")
				}
				projectWrite = true
				break
			}
			reason := "completed"
			if v == "open" {
				reason = "reopened"
			}
			payload = map[string]any{"state": v, "state_reason": reason}
		case "labels", "assignees":
			values, ok := stringsValue(m.Value)
			if !ok {
				return model.Item{}, source.Refuse("rejected", "expected string list")
			}
			payload = map[string]any{m.Field: values}
		default:
			return model.Item{}, source.Refuse("rejected", "GitHub field unavailable")
		}
	default:
		return model.Item{}, source.Refuse("rejected", "GitHub operation unavailable")
	}
	c, err := s.authenticate(ctx)
	if err != nil {
		return model.Item{}, err
	}
	current, err := s.read(ctx, c, m.Ref.ID)
	if err != nil {
		return model.Item{}, err
	}
	if m.ExpectedVersion == "" || current.Version != m.ExpectedVersion {
		return model.Item{}, source.Refuse("conflict", "issue, relations or project status changed")
	}
	if !s.writable {
		return model.Item{}, source.Refuse("rejected", "repository write capability unavailable")
	}
	if projectWrite {
		scope := false
		for _, v := range strings.Split(s.scopes, ",") {
			scope = scope || strings.TrimSpace(v) == "project"
		}
		if !scope {
			return model.Item{}, source.Refuse("rejected", "project OAuth scope required")
		}
		itemID, _ := current.Fields["projectItemID"].(string)
		if itemID == "" {
			return model.Item{}, source.Refuse("conflict", "issue missing from configured project")
		}
		var result struct {
			Update *struct {
				Item *struct{ ID string } `json:"projectV2Item"`
			} `json:"updateProjectV2ItemFieldValue"`
		}
		if err = s.query(ctx, c, projectStatusMutation, map[string]any{"projectID": s.o.Project.ID, "itemID": itemID, "fieldID": s.o.Project.FieldID, "optionID": m.Value}, &result); err != nil {
			return model.Item{}, err
		}
		if result.Update == nil || result.Update.Item == nil || result.Update.Item.ID != itemID {
			return model.Item{}, source.Refuse("unavailable", "project write outcome unknown; refresh before further edits")
		}
	} else {
		if _, err = s.request(ctx, c, method, endpoint, payload); err != nil {
			return model.Item{}, err
		}
	}
	// Read-back failures are not a reason to replay an already dispatched write.
	return s.read(ctx, c, m.Ref.ID)
}
func stringsValue(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return append([]string{}, values...), true
	case []any:
		out := make([]string, 0, len(values))
		for _, v := range values {
			s, ok := v.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case string:
		if values == "" {
			return []string{}, true
		}
		return strings.Split(values, ","), true
	}
	return nil, false
}
