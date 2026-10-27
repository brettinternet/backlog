package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

// Only the explicitly authorized Test team is accepted. The test creates and
// deletes its own synthetic issues, relation and comment, never existing data.
func TestLiveRoundTrip(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 and BL_LINEAR_TEST_TEAM=Test")
	}
	if os.Getenv("BL_LINEAR_TEST_TEAM") != "Test" {
		t.Fatal("explicit BL_LINEAR_TEST_TEAM=Test authorization required")
	}
	helper := []string{"security", "find-generic-password", "-s", "linear-api", "-w"}
	if raw := os.Getenv("BL_LINEAR_TEST_HELPER"); raw != "" {
		if json.Unmarshal([]byte(raw), &helper) != nil || len(helper) == 0 {
			t.Fatal("invalid helper argv JSON")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// Bootstrap discovers non-secret immutable identity. The credential never
	// leaves memory or enters diagnostics. All adapter operations use Credentials.
	token, err := exec.CommandContext(ctx, helper[0], helper[1:]...).Output()
	if err != nil {
		t.Fatal("credential helper failed (output withheld)")
	}
	token = bytes.TrimSpace(token)
	if len(token) == 0 || len(token) > 4096 || bytes.ContainsAny(token, "\r\n\x00") {
		t.Fatal("invalid helper output")
	}
	httpClient := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	bootstrap := func(query string, out any) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"query": query})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.linear.app/graphql", bytes.NewReader(raw))
		req.Header.Set("Authorization", string(token))
		req.Header.Set("Content-Type", "application/json")
		response, err := httpClient.Do(req)
		if err != nil {
			t.Fatal("identity discovery request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		if err != nil || response.StatusCode != 200 || decode(data, out) != nil {
			t.Fatal("identity discovery refused (provider output withheld)")
		}
	}
	var identity struct {
		Viewer struct {
			ID           string
			Organization struct{ ID string }
		}
		Teams connection[struct{ ID, Name string }]
	}
	bootstrap(`query{viewer{id organization{id}} teams(first:250){nodes{id name} pageInfo{hasNextPage endCursor}}}`, &identity)
	team := ""
	for _, candidate := range identity.Teams.Nodes {
		if candidate.Name == "Test" {
			if team != "" {
				t.Fatal("ambiguous Test team")
			}
			team = candidate.ID
		}
	}
	if team == "" || identity.Teams.PageInfo == nil || identity.Teams.PageInfo.HasNextPage {
		t.Fatal("Test team not uniquely discoverable")
	}
	clear(token)
	secrets := &remote.Secrets{}
	s, err := New(Options{ID: "linear-test", Organization: identity.Viewer.Organization.ID, Team: team, Account: project.AccountConfig{User: identity.Viewer.ID, Credential: helper}, AllowWrites: true, Secrets: secrets, Credentials: &remote.Credentials{Secrets: secrets}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.authenticate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	call := func(query string, vars, out any) {
		t.Helper()
		if err := s.query(ctx, c, query, vars, out); err != nil {
			t.Fatalf("live operation failed: %v", err)
		}
	}
	var states struct {
		Team struct {
			States connection[struct{ ID, Type string }]
		}
	}
	call(statesQuery, map[string]any{"team": team, "count": 250}, &states)
	todo, active, done := "", "", ""
	for _, state := range states.Team.States.Nodes {
		switch state.Type {
		case "unstarted":
			todo = state.ID
		case "started":
			active = state.ID
		case "completed":
			done = state.ID
		}
	}
	if todo == "" || active == "" || done == "" {
		t.Fatal("Test team requires todo/active/done states")
	}
	var labels struct {
		IssueLabels connection[struct{ ID, Name string }]
	}
	call(labelsQuery, map[string]any{"team": team, "count": 250}, &labels)
	if len(labels.IssueLabels.Nodes) == 0 {
		t.Fatal("Test team needs an existing label for the label round trip")
	}
	label := labels.IssueLabels.Nodes[0].Name
	deleteAfter := func(created struct{ ID, Identifier string }) {
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			var out struct{ IssueDelete struct{ Success bool } }
			err := s.query(cleanupCtx, c, `mutation($id:String!){issueDelete(id:$id){success}}`, map[string]any{"id": created.ID}, &out)
			if err != nil || !out.IssueDelete.Success {
				t.Errorf("cleanup failed for %s uuid=%s", created.Identifier, created.ID)
				return
			}
			t.Logf("deleted issue %s uuid=%s (cascades its comments/relations)", created.Identifier, created.ID)
			var verify struct {
				Issue *struct {
					ID      string
					Trashed bool
				}
			}
			err = s.query(cleanupCtx, c, `query($id:String!){issue(id:$id){id trashed}}`, map[string]any{"id": created.ID}, &verify)
			if err != nil {
				t.Logf("post-delete detail unavailable for %s; deletion confirmed by success receipt", created.Identifier)
			} else if verify.Issue != nil && !verify.Issue.Trashed {
				t.Errorf("deleted issue still live: %s", created.Identifier)
			} else {
				t.Logf("verified issue absent or trashed: %s", created.Identifier)
			}
		})
	}
	create := func(title string) struct{ ID, Identifier string } {
		t.Helper()
		var r struct {
			IssueCreate struct {
				Success bool
				Issue   struct{ ID, Identifier string }
			}
		}
		call(`mutation($input:IssueCreateInput!){issueCreate(input:$input){success issue{id identifier}}}`, map[string]any{"input": map[string]any{"teamId": team, "title": title, "stateId": todo}}, &r)
		created := r.IssueCreate.Issue
		if created.ID != "" {
			deleteAfter(created)
		}
		if !r.IssueCreate.Success || !uuid(created.ID) || created.Identifier == "" {
			t.Fatal("invalid created issue receipt")
		}
		t.Logf("created issue %s uuid=%s", created.Identifier, created.ID)
		return created
	}
	// The roundtrip issue comes from the source's own create (BL-112), with
	// a cycle when the Test team has an open one.
	catalog, err := s.Load(ctx, source.Cursor{Sprints: true})
	if err != nil {
		t.Fatal(err)
	}
	template := model.Item{Title: "bl-provider-test BL-39 roundtrip", Description: "bl-provider-test body", Status: todo, Assignees: []string{identity.Viewer.ID}, Priority: model.Medium, Labels: []string{label}}
	for _, sprint := range catalog.Sprints {
		if sprint.State != model.SprintClosed {
			template.Sprint = sprint.Key
		}
	}
	item, err := s.Apply(ctx, model.Mutation{Ref: model.Ref{Source: s.o.ID}, Op: model.Create, Item: &template})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	first := struct{ ID, Identifier string }{fmt.Sprint(item.Fields["uuid"]), item.Ref.ID}
	deleteAfter(first)
	t.Logf("created issue %s uuid=%s through the source (cycle %q)", first.Identifier, first.ID, template.Sprint)
	if item.Title != template.Title || item.Description != template.Description || item.Status != todo || len(item.Assignees) != 1 || item.Assignees[0] != identity.Viewer.ID || item.Priority != model.Medium || len(item.Labels) != 1 || item.Labels[0] != label || item.Sprint != template.Sprint {
		t.Fatalf("create roundtrip failed: %+v", item)
	}
	blocker := create("bl-provider-test BL-39 blocker")
	apply := func(field string, value any) {
		t.Helper()
		item, err = s.Apply(ctx, model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: field, Value: value})
		if err != nil {
			t.Fatalf("set %s failed: %v", field, err)
		}
	}
	apply("status", active)
	if item.Category != model.Active {
		t.Fatal("state roundtrip failed")
	}
	apply("assignees", []string{identity.Viewer.ID})
	if len(item.Assignees) != 1 || item.Assignees[0] != identity.Viewer.ID {
		t.Fatal("assignee roundtrip failed")
	}
	apply("priority", "high")
	if item.Priority != model.High {
		t.Fatal("priority roundtrip failed")
	}
	apply("labels", []string{label})
	if len(item.Labels) != 1 || item.Labels[0] != label {
		t.Fatal("label roundtrip failed")
	}
	item, err = s.Apply(ctx, model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.AppendComment, Text: "bl-provider-test BL-39 comment"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, comment := range item.Comments {
		found = found || comment.Body == "bl-provider-test BL-39 comment"
	}
	if !found {
		t.Fatal("comment roundtrip failed")
	}
	var comments struct {
		Issue struct {
			Comments connection[struct{ ID, Body string }]
		}
	}
	call(commentsQuery, map[string]any{"id": first.ID, "count": 250}, &comments)
	for _, comment := range comments.Issue.Comments.Nodes {
		if strings.Contains(comment.Body, "bl-provider-test BL-39") {
			t.Logf("created comment %s", comment.ID)
			commentID := comment.ID
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				var out struct{ CommentDelete struct{ Success bool } }
				if err := s.query(cleanupCtx, c, `mutation($id:String!){commentDelete(id:$id){success}}`, map[string]any{"id": commentID}, &out); err != nil || !out.CommentDelete.Success {
					t.Errorf("comment cleanup failed: %s", commentID)
				} else {
					t.Logf("deleted comment %s", commentID)
				}
			})
		}
	}
	var relation struct {
		IssueRelationCreate struct {
			Success       bool
			IssueRelation struct{ ID string }
		}
	}
	call(`mutation($input:IssueRelationCreateInput!){issueRelationCreate(input:$input){success issueRelation{id}}}`, map[string]any{"input": map[string]any{"issueId": blocker.ID, "relatedIssueId": first.ID, "type": "blocks"}}, &relation)
	relationID := relation.IssueRelationCreate.IssueRelation.ID
	if relationID != "" {
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			var out struct{ IssueRelationDelete struct{ Success bool } }
			if err := s.query(cleanupCtx, c, `mutation($id:String!){issueRelationDelete(id:$id){success}}`, map[string]any{"id": relationID}, &out); err != nil || !out.IssueRelationDelete.Success {
				t.Errorf("relation cleanup failed: %s", relationID)
			} else {
				t.Logf("deleted relation %s", relationID)
			}
		})
	}
	if !relation.IssueRelationCreate.Success || relationID == "" {
		t.Fatal("relation creation failed")
	}
	t.Logf("created relation %s", relationID)
	delta, err := s.Load(ctx, source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, row := range delta.Items {
		if row.Ref == item.Ref {
			for _, ref := range row.BlockedBy {
				found = found || ref.ID == blocker.Identifier
			}
		}
	}
	if !found {
		t.Fatal("independent relation reconciliation failed")
	}
	if _, err = s.Apply(ctx, model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "priority", Value: "low"}); err == nil {
		t.Fatal("stale relation version accepted")
	}
	item, err = s.Read(ctx, first.Identifier)
	if err != nil {
		t.Fatal(err)
	}
	apply("assignees", []string{})
	apply("labels", []string{})
	apply("status", done)
	if item.Category != model.Done {
		t.Fatal("completion roundtrip failed")
	}
	for _, op := range []model.Operation{model.Archive, model.Unarchive} {
		item, err = s.Apply(ctx, model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: op})
		if err != nil || item.Fields["archived"] != (op == model.Archive) {
			t.Fatalf("%s roundtrip failed: %v", op, err)
		}
	}
	// BL-113: create sets the parent and links the blocker.
	childTemplate := model.Item{Title: "bl-provider-test BL-113 child", Parent: &model.Ref{ID: first.Identifier}, BlockedBy: []model.Ref{{Source: s.o.ID, ID: blocker.Identifier}}}
	child, err := s.Apply(ctx, model.Mutation{Ref: model.Ref{Source: s.o.ID}, Op: model.Create, Item: &childTemplate})
	var refusal *source.Error
	if err == nil {
		deleteAfter(struct{ ID, Identifier string }{fmt.Sprint(child.Fields["uuid"]), child.Ref.ID})
	} else if errors.As(err, &refusal) && refusal.Created != nil {
		var r struct{ Issue *entity }
		call(entityQuery, map[string]any{"id": refusal.Created.ID}, &r)
		deleteAfter(struct{ ID, Identifier string }{r.Issue.ID, r.Issue.Identifier})
	}
	if err != nil {
		t.Fatalf("linked create failed: %v", err)
	}
	t.Logf("created issue %s through the source with parent %s and blocker %s", child.Ref.ID, first.Identifier, blocker.Identifier)
	if child.Parent == nil || child.Parent.ID != first.Identifier || len(child.BlockedBy) != 1 || child.BlockedBy[0].ID != blocker.Identifier {
		t.Fatalf("linked create roundtrip failed: parent=%v blockedBy=%v", child.Parent, child.BlockedBy)
	}
	t.Logf("roundtrip passed in Test team=%s org=%s; relation fingerprint conflict rejected; archive and unarchive round-tripped", team, identity.Viewer.Organization.ID)
}
