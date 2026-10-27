package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

// draftContent reads a DraftIssue into the issue fields it shares. A draft
// has no number, repository, labels, comments or relations.
const draftContent = `... on DraftIssue{id title body createdAt updatedAt assignees(first:100){nodes{login} pageInfo{hasNextPage endCursor}}}`
const draftItemQuery = `query($itemID:ID!){node(id:$itemID){... on ProjectV2Item{id updatedAt isArchived project{id} content{__typename ... on Issue{number repository{nameWithOwner}} ` + draftContent + `}}}}`
const draftUpdateMutation = `mutation($input:UpdateProjectV2DraftIssueInput!){updateProjectV2DraftIssue(input:$input){draftIssue{id}}}`
const draftAddMutation = `mutation($input:AddProjectV2DraftIssueInput!){addProjectV2DraftIssue(input:$input){projectItem{id}}}`
const draftConvertMutation = `mutation($itemID:ID!,$repositoryID:ID!){convertProjectV2DraftIssueItemToIssue(input:{itemId:$itemID,repositoryId:$repositoryID}){item{id content{__typename ... on Issue{id number repository{nameWithOwner}}}}}}`
const itemDeleteMutation = `mutation($projectID:ID!,$itemID:ID!){deleteProjectV2Item(input:{projectId:$projectID,itemId:$itemID}){deletedItemId}}`
const userQuery = `query($login:String!){user(login:$login){id login}}`

// draftID reports whether id names a draft: its project item's node ID, which
// survives edits and status changes until the draft becomes an issue.
func draftID(id string) bool {
	rest, ok := strings.CutPrefix(id, "PVTI_")
	return ok && rest != "" && !strings.ContainsFunc(rest, func(r rune) bool {
		return r != '_' && r != '-' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z')
	})
}

// draft builds a draft item, always in the draft status (backlog), so it is
// open but never ready and has no claim key.
func (s *Source) draft(p projectItem) (model.Item, error) {
	d := p.Content
	if !draftID(p.ID) || p.UpdatedAt.IsZero() || d == nil || d.TypeName != "DraftIssue" || d.ID == "" || d.UpdatedAt.IsZero() {
		return model.Item{}, source.Refuse("unavailable", "invalid project draft")
	}
	if d.Assignees.PageInfo == nil || d.Assignees.PageInfo.HasNextPage {
		return model.Item{}, source.Refuse("unavailable", "draft assignees exceed complete read limit")
	}
	item := model.Item{Ref: model.Ref{Source: s.o.ID, ID: p.ID}, Title: d.Title, Type: "draft", Description: d.Body, Status: "draft", Category: model.Backlog, Created: d.CreatedAt, Updated: d.UpdatedAt, Fields: map[string]any{"id": d.ID, "projectItemID": p.ID, "projectUpdatedAt": p.UpdatedAt}}
	for _, v := range d.Assignees.Nodes {
		item.Assignees = append(item.Assignees, v.Login)
	}
	raw, _ := json.Marshal(item)
	hash := sha256.Sum256(raw)
	item.Version = hex.EncodeToString(hash[:])
	return item, nil
}

// addDrafts appends a repository source's project drafts to delta.
func (s *Source) addDrafts(delta source.Delta, drafts []projectItem) (source.Delta, error) {
	for _, p := range drafts {
		item, err := s.draft(p)
		if err != nil {
			return source.Delta{}, err
		}
		delta.Items = append(delta.Items, item)
	}
	return delta, nil
}

// draftRead reads one draft in the bound project. Discovery rechecks the
// binding and, with OAuth, the project grant a write needs. A draft that
// became an issue is gone under its draft ID.
func (s *Source) draftRead(ctx context.Context, c remote.Credential, id string) (model.Item, error) {
	if s.o.Project == nil {
		return model.Item{}, source.Refuse("invalid-id", "drafts need a githubProject")
	}
	if _, _, err := s.discover(ctx, c, false); err != nil {
		return model.Item{}, err
	}
	var data struct{ Node *projectItem }
	if err := s.query(ctx, c, draftItemQuery, map[string]any{"itemID": id}, &data, "node"); err != nil {
		return model.Item{}, err
	}
	p := data.Node
	switch {
	case p == nil || p.ID != id || p.Project == nil || p.Project.ID != s.o.Project.ID || p.IsArchived:
		return model.Item{}, source.Refuse("not-found", "draft not in the configured project")
	case p.Content != nil && p.Content.TypeName == "Issue":
		return model.Item{}, source.Refuse("not-found", fmt.Sprintf("draft is now issue %s#%d", p.Content.Repository.NameWithOwner, p.Content.Number))
	}
	return s.draft(*p)
}

// applyDraft edits, converts or deletes a draft after the same fresh version
// preflight as an issue write (D15). Every draft write is a project write.
func (s *Source) applyDraft(ctx context.Context, m model.Mutation) (model.Item, error) {
	if s.o.Project == nil || !s.o.Project.AllowWrites {
		return model.Item{}, source.Refuse("rejected", "draft writes need githubProject.allowWrites")
	}
	input := map[string]any{}
	status, entry := "", ""
	var logins []string // the new assignees, nil when they don't change
	switch {
	case m.Op == model.Delete:
	case m.Op == model.ReplaceSection && m.Section == "Description":
		input["body"] = m.Text
	case m.Op == model.Set && m.Field == "title":
		v, ok := m.Value.(string)
		if !ok || strings.TrimSpace(v) == "" {
			return model.Item{}, source.Refuse("rejected", "title required")
		}
		input["title"] = v
	case m.Op == model.Set && m.Field == "assignees":
		values, ok := stringsValue(m.Value)
		if !ok {
			return model.Item{}, source.Refuse("rejected", "expected string list")
		}
		logins = values
	case (m.Op == model.Add || m.Op == model.Remove) && m.Field == "assignees":
		v, ok := m.Value.(string)
		if !ok || strings.TrimSpace(v) == "" {
			return model.Item{}, source.Refuse("rejected", "one assignee required")
		}
		entry = v
	case m.Op == model.Set && m.Field == "status":
		v, ok := m.Value.(string)
		if !ok || v != "draft" && v != "open" && v != "closed" && s.o.Project.Options[v] == "" {
			return model.Item{}, source.Refuse("rejected", "draft, open, closed or a configured project option ID required")
		}
		if v != "draft" && s.projectOnly() {
			return model.Item{}, source.Refuse("rejected", "source "+s.o.ID+" has no repo to convert drafts in; set repo")
		}
		status = v
	default:
		return model.Item{}, source.Refuse("rejected", "GitHub drafts take only title, description, assignees and status; set a status to convert one to an issue for labels, comments and relations")
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
		return model.Item{}, source.Refuse("conflict", "draft changed")
	}
	if !s.projectScope(c) {
		return model.Item{}, source.Refuse("rejected", "project OAuth scope required")
	}
	switch {
	case m.Op == model.Delete:
		return s.deleteDraft(ctx, c, current)
	case status == "draft":
		return current, nil
	case status != "":
		return s.promote(ctx, c, current, status)
	case entry != "":
		// Logins are case-insensitive; an add or remove that changes nothing sends nothing.
		if slices.ContainsFunc(current.Assignees, func(v string) bool { return strings.EqualFold(v, entry) }) == (m.Op == model.Add) {
			return current, nil
		}
		logins = slices.DeleteFunc(slices.Clone(current.Assignees), func(v string) bool { return strings.EqualFold(v, entry) })
		if m.Op == model.Add {
			logins = append(logins, entry)
		}
	}
	if logins != nil {
		if input["assigneeIds"], err = s.userIDs(ctx, c, logins); err != nil {
			return model.Item{}, err
		}
	}
	draftIssueID := current.Fields["id"]
	input["draftIssueId"] = draftIssueID
	var result struct {
		Update *struct{ DraftIssue *struct{ ID string } } `json:"updateProjectV2DraftIssue"`
	}
	err = s.query(ctx, c, draftUpdateMutation, map[string]any{"input": input}, &result)
	if err == nil && (result.Update == nil || result.Update.DraftIssue == nil || result.Update.DraftIssue.ID != draftIssueID) {
		err = errors.New("draft missing from mutation result")
	}
	if err != nil {
		if unsent(err) {
			return model.Item{}, err
		}
		return model.Item{}, source.Refuse("unknown-outcome", "GitHub draft write unconfirmed; refresh before further edits")
	}
	item, err := s.read(ctx, c, m.Ref.ID)
	if err != nil {
		return model.Item{}, source.Refuse("unknown-outcome", "GitHub draft write sent but refresh failed; refresh before further edits")
	}
	return item, nil
}

// promote converts a draft to an issue in the configured repo, then applies
// status: the conversion and at most one follow-up, each sent once. The issue
// keeps the project item and reloads under its own ID.
func (s *Source) promote(ctx context.Context, c remote.Credential, current model.Item, status string) (model.Item, error) {
	if err := s.identity(ctx, c); err != nil {
		return model.Item{}, err
	}
	if !s.writable || s.o.Account.OAuth && (!c.HasScope("write") || !s.issueWrite) {
		return model.Item{}, source.Refuse("rejected", "repository write capability unavailable")
	}
	draft := current.Ref.ID
	var result struct {
		Convert *struct{ Item *projectItem } `json:"convertProjectV2DraftIssueItemToIssue"`
	}
	err := s.query(ctx, c, draftConvertMutation, map[string]any{"itemID": draft, "repositoryID": s.repositoryID}, &result)
	var converted *projectItem
	if err == nil {
		if result.Convert != nil {
			converted = result.Convert.Item
		}
		if converted == nil || converted.ID == "" || converted.Content == nil || converted.Content.TypeName != "Issue" || converted.Content.Number < 1 || !strings.EqualFold(converted.Content.Repository.NameWithOwner, s.o.Repo) {
			err = errors.New("issue missing from conversion result")
		}
	}
	if err != nil {
		if unsent(err) {
			return model.Item{}, err
		}
		return model.Item{}, source.Refuse("unknown-outcome", "GitHub conversion of draft "+draft+" unconfirmed; refresh before further edits")
	}
	n := converted.Content.Number
	id := s.itemID(s.o.Repo, n)
	switch {
	case s.o.Project.Options[status] != "":
		var set struct {
			Update *struct {
				Item *struct{ ID string } `json:"projectV2Item"`
			} `json:"updateProjectV2ItemFieldValue"`
		}
		err = s.query(ctx, c, projectStatusMutation, map[string]any{"projectID": s.o.Project.ID, "itemID": converted.ID, "fieldID": s.o.Project.FieldID, "optionID": status}, &set)
		if err == nil && (set.Update == nil || set.Update.Item == nil || set.Update.Item.ID != converted.ID) {
			err = errors.New("project item missing from mutation result")
		}
	case status == "closed":
		_, err = s.request(ctx, c, http.MethodPatch, s.issuePath(s.o.Repo, n), map[string]any{"state": "closed", "state_reason": "completed"})
	}
	if err != nil {
		// The issue exists now: name it, since the draft ID no longer resolves.
		return model.Item{}, &source.Error{Reason: "unknown-outcome", Message: "GitHub draft " + draft + " is now issue " + id + " but its status is unconfirmed; refresh before further edits", Created: &model.Ref{Source: s.o.ID, ID: id}}
	}
	item, err := s.read(ctx, c, id)
	if err != nil {
		return model.Item{}, &source.Error{Reason: "unknown-outcome", Message: "GitHub draft " + draft + " is now issue " + id + " but refresh failed; refresh before further edits", Created: &model.Ref{Source: s.o.ID, ID: id}}
	}
	return item, nil
}

// createDraft adds a draft to the bound project with one non-retried
// mutation. A draft takes only a title, body and assignees.
func (s *Source) createDraft(ctx context.Context, item *model.Item) (model.Item, error) {
	if s.o.Project == nil || !s.o.Project.AllowWrites {
		return model.Item{}, source.Refuse("rejected", "drafts need githubProject.allowWrites")
	}
	if len(item.Labels) > 0 || item.Parent != nil || len(item.BlockedBy) > 0 {
		return model.Item{}, source.Refuse("rejected", "GitHub drafts take only a title, body and assignees; labels, parent and blockers need an issue")
	}
	c, err := s.authenticate(ctx)
	if err != nil {
		return model.Item{}, err
	}
	if _, _, err = s.discover(ctx, c, false); err != nil {
		return model.Item{}, err
	}
	if !s.projectScope(c) {
		return model.Item{}, source.Refuse("rejected", "project OAuth scope required")
	}
	assignees, err := s.userIDs(ctx, c, item.Assignees)
	if err != nil {
		return model.Item{}, err
	}
	var result struct {
		Add *struct{ ProjectItem *struct{ ID string } } `json:"addProjectV2DraftIssue"`
	}
	err = s.query(ctx, c, draftAddMutation, map[string]any{"input": map[string]any{"projectId": s.o.Project.ID, "title": item.Title, "body": item.Description, "assigneeIds": assignees}}, &result)
	if err == nil && (result.Add == nil || result.Add.ProjectItem == nil || !draftID(result.Add.ProjectItem.ID)) {
		err = errors.New("project item missing from mutation result")
	}
	if err != nil {
		if unsent(err) {
			return model.Item{}, err
		}
		return model.Item{}, source.Refuse("unknown-outcome", "GitHub draft create unconfirmed; refresh before creating again")
	}
	id := result.Add.ProjectItem.ID
	created, err := s.read(ctx, c, id)
	if err != nil {
		return model.Item{}, &source.Error{Reason: "unknown-outcome", Message: "GitHub draft " + id + " created but refresh failed", Created: &model.Ref{Source: s.o.ID, ID: id}}
	}
	if s.created == nil {
		s.created = map[string]string{}
	}
	s.created[id] = created.Version
	return created, nil
}

// deleteDraft undoes a draft create by deleting its project item, and with it
// the draft, only while the draft is at the version create returned.
func (s *Source) deleteDraft(ctx context.Context, c remote.Credential, current model.Item) (model.Item, error) {
	id := current.Ref.ID
	created := s.created[id]
	delete(s.created, id)
	if created != current.Version {
		return model.Item{}, source.Refuse("conflict", "draft changed since it was created; delete it on GitHub")
	}
	var result struct {
		Delete *struct {
			DeletedItemID string `json:"deletedItemId"`
		} `json:"deleteProjectV2Item"`
	}
	err := s.query(ctx, c, itemDeleteMutation, map[string]any{"projectID": s.o.Project.ID, "itemID": id}, &result)
	if err == nil && (result.Delete == nil || result.Delete.DeletedItemID != id) {
		err = errors.New("deleted item missing from mutation result")
	}
	if err != nil {
		if unsent(err) {
			return model.Item{}, err
		}
		return model.Item{}, source.Refuse("unknown-outcome", "GitHub draft delete unconfirmed; refresh before further edits")
	}
	return model.Item{Ref: current.Ref}, nil
}

// userIDs resolves draft assignee logins to user node IDs, one read each,
// before anything is sent.
func (s *Source) userIDs(ctx context.Context, c remote.Credential, logins []string) ([]string, error) {
	ids := []string{}
	for _, login := range logins {
		var result struct{ User *struct{ ID, Login string } }
		if err := s.query(ctx, c, userQuery, map[string]any{"login": login}, &result, "user"); err != nil {
			return nil, err
		}
		if result.User == nil || result.User.ID == "" || !strings.EqualFold(result.User.Login, login) {
			return nil, source.Refuse("not-found", "GitHub user "+login+" not found")
		}
		ids = append(ids, result.User.ID)
	}
	return ids, nil
}
