package github

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

const nodeQuery = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){nameWithOwner issue(number:$number){id number}}}`
const deletableQuery = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){id issue(number:$number){id number viewerCanDelete}}}`
const subIssueMutation = `mutation($parent:ID!,$child:ID!){addSubIssue(input:{issueId:$parent,subIssueId:$child}){issue{id} subIssue{id}}}`
const blockedByMutation = `mutation($id:ID!,$blocker:ID!){addBlockedBy(input:{issueId:$id,blockingIssueId:$blocker}){issue{id} blockingIssue{id}}}`
const projectAddMutation = `mutation($projectID:ID!,$contentID:ID!){addProjectV2ItemById(input:{projectId:$projectID,contentId:$contentID}){item{id}}}`
const deleteMutation = `mutation($id:ID!){deleteIssue(input:{issueId:$id}){repository{id}}}`

// issueNode resolves a parent or blocker ref to its issue node ID: an issue in
// this repository, under another GitHub source's alias, or as HOST/OWNER/REPO
// on this host. Other sources refuse.
func (s *Source) issueNode(ctx context.Context, c remote.Credential, ref model.Ref) (string, error) {
	repo := s.o.Repo
	if ref.Source != "" && ref.Source != s.o.ID {
		key := strings.ToLower(ref.Source)
		for k, alias := range s.o.SourceIDs {
			if alias == ref.Source {
				key = k
			}
		}
		var ok bool
		if repo, ok = strings.CutPrefix(key, strings.ToLower(s.o.Host)+"/"); !ok || strings.Count(repo, "/") != 1 {
			return "", source.Refuse("rejected", "GitHub parent and blockers must be GitHub issues on "+s.o.Host)
		}
	}
	n, err := validNumber(ref.ID)
	if err != nil {
		return "", err
	}
	owner, name, _ := strings.Cut(repo, "/")
	var r struct {
		Repository *struct {
			NameWithOwner string
			Issue         *struct {
				ID     string
				Number int
			}
		}
	}
	if err = s.query(ctx, c, nodeQuery, map[string]any{"owner": owner, "repo": name, "number": n}, &r); err != nil {
		return "", err
	}
	if r.Repository == nil || r.Repository.Issue == nil {
		return "", source.Refuse("not-found", "GitHub parent or blocker unavailable")
	}
	if !strings.EqualFold(r.Repository.NameWithOwner, repo) || r.Repository.Issue.Number != n || r.Repository.Issue.ID == "" {
		return "", source.Refuse("unavailable", "GitHub parent or blocker identity changed")
	}
	return r.Repository.Issue.ID, nil
}

// create has no version to preflight. Field, permission and ref checks run
// before one non-retried issue POST; a failure after dispatch has an unknown
// outcome. Parent, blockers and project status each need one more
// non-retried mutation, so a failure after the issue exists names it as
// Created and records no undo.
func (s *Source) create(ctx context.Context, item *model.Item) (model.Item, error) {
	if item == nil || strings.TrimSpace(item.Title) == "" {
		return model.Item{}, source.Refuse("rejected", "GitHub create requires a title")
	}
	if item.Milestone != "" || item.Type != "" && item.Type != "issue" || item.Priority != "" || len(item.Criteria) > 0 || len(item.DoD) > 0 || item.Sprint != "" {
		return model.Item{}, source.Refuse("rejected", "GitHub create doesn't support milestone, type, priority, criteria or sprint")
	}
	option := ""
	if item.Status != "" && item.Status != "open" {
		if s.o.Project == nil || !s.o.Project.AllowWrites || s.o.Project.Options[item.Status] == "" {
			return model.Item{}, source.Refuse("rejected", "GitHub create status must be open or a writable configured project option ID")
		}
		option = item.Status
	}
	c, err := s.authenticate(ctx)
	if err != nil {
		return model.Item{}, err
	}
	if err = s.identity(ctx, c); err != nil {
		return model.Item{}, err
	}
	if !s.writable || s.o.Account.OAuth && (!c.HasScope("write") || !s.issueWrite) {
		return model.Item{}, source.Refuse("rejected", "repository write capability unavailable")
	}
	if option != "" {
		// Discovery confirms the option still exists and sets OAuth project grants.
		if _, _, err = s.discover(ctx, c, false); err != nil {
			return model.Item{}, err
		}
		if !s.projectScope(c) {
			return model.Item{}, source.Refuse("rejected", "project OAuth scope required")
		}
	}
	parent := ""
	if item.Parent != nil {
		if parent, err = s.issueNode(ctx, c, *item.Parent); err != nil {
			return model.Item{}, err
		}
	}
	var blockers []string
	for _, ref := range item.BlockedBy {
		id, err := s.issueNode(ctx, c, ref)
		if err != nil {
			return model.Item{}, err
		}
		if !slices.Contains(blockers, id) {
			blockers = append(blockers, id)
		}
	}
	payload := map[string]any{"title": item.Title, "body": item.Description}
	if len(item.Labels) > 0 {
		payload["labels"] = item.Labels
	}
	if len(item.Assignees) > 0 {
		payload["assignees"] = item.Assignees
	}
	response, err := s.request(ctx, c, http.MethodPost, s.base()+"/repos/"+s.o.Repo+"/issues", payload)
	if err != nil {
		if unsent(err) {
			return model.Item{}, err
		}
		return model.Item{}, source.Refuse("unknown-outcome", "GitHub create unconfirmed; refresh before creating again")
	}
	var issue struct {
		Number int    `json:"number"`
		NodeID string `json:"node_id"`
	}
	if json.Unmarshal(response.Body, &issue) != nil || issue.Number < 1 || issue.NodeID == "" {
		return model.Item{}, source.Refuse("unknown-outcome", "GitHub create unconfirmed; refresh before creating again")
	}
	id := strconv.Itoa(issue.Number)
	partial := &source.Error{Reason: "unknown-outcome", Message: "GitHub issue " + id + " created but its parent, blockers or project status are unconfirmed", Created: &model.Ref{Source: s.o.ID, ID: id}}
	if parent != "" {
		var r struct {
			Add *struct{ Issue, SubIssue *struct{ ID string } } `json:"addSubIssue"`
		}
		err := s.query(ctx, c, subIssueMutation, map[string]any{"parent": parent, "child": issue.NodeID}, &r)
		if err != nil || r.Add == nil || r.Add.SubIssue == nil || r.Add.SubIssue.ID != issue.NodeID {
			return model.Item{}, partial
		}
	}
	for _, blocker := range blockers {
		var r struct {
			Add *struct{ Issue, BlockingIssue *struct{ ID string } } `json:"addBlockedBy"`
		}
		err := s.query(ctx, c, blockedByMutation, map[string]any{"id": issue.NodeID, "blocker": blocker}, &r)
		if err != nil || r.Add == nil || r.Add.Issue == nil || r.Add.Issue.ID != issue.NodeID {
			return model.Item{}, partial
		}
	}
	if option != "" {
		var added struct {
			Add *struct{ Item *struct{ ID string } } `json:"addProjectV2ItemById"`
		}
		err := s.query(ctx, c, projectAddMutation, map[string]any{"projectID": s.o.Project.ID, "contentID": issue.NodeID}, &added)
		if err != nil || added.Add == nil || added.Add.Item == nil || added.Add.Item.ID == "" {
			return model.Item{}, partial
		}
		var set struct {
			Update *struct {
				Item *struct{ ID string } `json:"projectV2Item"`
			} `json:"updateProjectV2ItemFieldValue"`
		}
		err = s.query(ctx, c, projectStatusMutation, map[string]any{"projectID": s.o.Project.ID, "itemID": added.Add.Item.ID, "fieldID": s.o.Project.FieldID, "optionID": option}, &set)
		if err != nil || set.Update == nil || set.Update.Item == nil || set.Update.Item.ID != added.Add.Item.ID {
			return model.Item{}, partial
		}
	}
	created, err := s.read(ctx, c, id)
	if err != nil {
		partial.Message = "GitHub issue " + id + " created but refresh failed"
		return model.Item{}, partial
	}
	if s.created == nil {
		s.created = map[string]string{}
	}
	s.created[id] = created.Version
	return created, nil
}

// delete undoes a create. It deletes the issue only when it's still at the
// version create returned and GitHub says the viewer can delete it (admin,
// allowed by the organization and the credential). Otherwise it closes the
// issue as not planned. A deleted issue returns an item without a version.
func (s *Source) delete(ctx context.Context, m model.Mutation) (model.Item, error) {
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
	if !s.writable || s.o.Account.OAuth && (!c.HasScope("write") || !s.issueWrite) {
		return model.Item{}, source.Refuse("rejected", "repository write capability unavailable")
	}
	created := s.created[m.Ref.ID]
	delete(s.created, m.Ref.ID)
	if created == current.Version {
		vars := s.variables()
		vars["number"], _ = validNumber(m.Ref.ID)
		var r struct {
			Repository *struct {
				ID    string
				Issue *struct {
					ID              string
					Number          int
					ViewerCanDelete bool
				}
			}
		}
		if err = s.query(ctx, c, deletableQuery, vars, &r); err != nil {
			return model.Item{}, err
		}
		if r.Repository == nil || r.Repository.ID != s.repositoryID || r.Repository.Issue == nil || r.Repository.Issue.ID != current.Fields["id"] {
			return model.Item{}, source.Refuse("unavailable", "issue identity changed")
		}
		if r.Repository.Issue.ViewerCanDelete {
			var deleted struct {
				Delete *struct{ Repository *struct{ ID string } } `json:"deleteIssue"`
			}
			err = s.query(ctx, c, deleteMutation, map[string]any{"id": r.Repository.Issue.ID}, &deleted)
			if err == nil && (deleted.Delete == nil || deleted.Delete.Repository == nil || deleted.Delete.Repository.ID != s.repositoryID) {
				err = source.Refuse("unavailable", "deleted repository missing from mutation result")
			}
			if err != nil {
				if unsent(err) {
					return model.Item{}, err
				}
				return model.Item{}, source.Refuse("unknown-outcome", "GitHub delete unconfirmed; refresh before further edits")
			}
			return model.Item{Ref: m.Ref}, nil
		}
	}
	if _, err = s.request(ctx, c, http.MethodPatch, s.issuePath(m.Ref.ID), map[string]any{"state": "closed", "state_reason": "not_planned"}); err != nil {
		if unsent(err) {
			return model.Item{}, err
		}
		return model.Item{}, source.Refuse("unknown-outcome", "GitHub close unconfirmed; refresh before further edits")
	}
	item, err := s.read(ctx, c, m.Ref.ID)
	if err != nil {
		return model.Item{}, source.Refuse("unknown-outcome", "GitHub close sent but refresh failed; refresh before further edits")
	}
	return item, nil
}
