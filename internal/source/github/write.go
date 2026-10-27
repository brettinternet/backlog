package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
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
	if m.Op == model.Create {
		return s.create(ctx, m.Item)
	}
	if _, err := validNumber(m.Ref.ID); err != nil {
		return model.Item{}, err
	}
	if m.Op == model.Delete {
		return s.delete(ctx, m)
	}
	var payload any
	method, endpoint := http.MethodPatch, s.issuePath(m.Ref.ID)
	projectWrite := false
	entry := ""
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
	case model.ReplaceSection:
		if m.Section != "Description" {
			return model.Item{}, source.Refuse("rejected", "GitHub section unavailable; only the description is editable")
		}
		payload = map[string]any{"body": m.Text}
	case model.Add, model.Remove:
		v, ok := m.Value.(string)
		if !ok || strings.TrimSpace(v) == "" || m.Field != "labels" && m.Field != "assignees" {
			return model.Item{}, source.Refuse("rejected", "one label or assignee required")
		}
		// GitHub's add and remove endpoints leave entries added concurrently by
		// others in place, unlike a whole-list replace.
		entry = v
		switch {
		case m.Op == model.Add:
			method, endpoint, payload = http.MethodPost, endpoint+"/"+m.Field, map[string]any{m.Field: []string{v}}
		case m.Field == "labels":
			method, endpoint = http.MethodDelete, endpoint+"/labels/"+url.PathEscape(v)
		default:
			method, endpoint, payload = http.MethodDelete, endpoint+"/assignees", map[string]any{"assignees": []string{v}}
		}
	case model.Set:
		switch m.Field {
		case "title":
			v, ok := m.Value.(string)
			if !ok || strings.TrimSpace(v) == "" {
				return model.Item{}, source.Refuse("rejected", "title required")
			}
			payload = map[string]any{"title": v}
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
	if !s.writable || s.o.Account.OAuth && (!c.HasScope("write") || !projectWrite && !s.issueWrite) {
		return model.Item{}, source.Refuse("rejected", "repository write capability unavailable")
	}
	if entry != "" {
		list := current.Labels
		if m.Field == "assignees" {
			list = current.Assignees
		}
		// Names and logins are case-insensitive. Removing an absent entry would
		// fail after dispatch, so an add or remove that changes nothing sends nothing.
		if slices.ContainsFunc(list, func(v string) bool { return strings.EqualFold(v, entry) }) == (m.Op == model.Add) {
			return current, nil
		}
	}
	if projectWrite {
		if !s.projectScope(c) {
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
		err = s.query(ctx, c, projectStatusMutation, map[string]any{"projectID": s.o.Project.ID, "itemID": itemID, "fieldID": s.o.Project.FieldID, "optionID": m.Value}, &result)
		if err == nil && (result.Update == nil || result.Update.Item == nil || result.Update.Item.ID != itemID) {
			err = errors.New("project item missing from mutation result")
		}
	} else {
		_, err = s.request(ctx, c, method, endpoint, payload)
	}
	if err != nil {
		if unsent(err) {
			return model.Item{}, err
		}
		return model.Item{}, source.Refuse("unknown-outcome", "GitHub write unconfirmed; refresh before further edits")
	}
	// Read-back failures are not a reason to replay an already dispatched write.
	item, err := s.read(ctx, c, m.Ref.ID)
	if err != nil {
		return model.Item{}, source.Refuse("unknown-outcome", "GitHub write sent but refresh failed; refresh before further edits")
	}
	return item, nil
}

// unsent reports an error that proves the request wasn't applied: a local
// scheduler refusal or a rate limit.
func unsent(err error) bool {
	var limited remote.ScheduleDiagnostic
	var diagnostic *oauth.Diagnostic
	return errors.As(err, &limited) || errors.As(err, &diagnostic) && diagnostic.Code == oauth.CodeRateLimited
}

// projectScope reports whether the credential may write Projects v2 fields.
func (s *Source) projectScope(c remote.Credential) bool {
	if s.o.Account.OAuth {
		return c.HasScope("write") && s.projectWritable
	}
	for _, v := range strings.Split(s.scopes, ",") {
		if strings.TrimSpace(v) == "project" {
			return true
		}
	}
	return false
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
