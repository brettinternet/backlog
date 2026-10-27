package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source/remote"
)

// Opt-in only. Inputs must identify a newly created disposable project whose
// title begins bl-provider-test-. The test never edits preexisting issues/items.
func TestLiveRoundTrip(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 and explicit GitHub test inputs")
	}
	required := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			t.Fatalf("%s required", key)
		}
		return v
	}
	repo, user := required("BL_GITHUB_TEST_REPO"), required("BL_GITHUB_TEST_USER")
	number, err := strconv.Atoi(required("BL_GITHUB_TEST_PROJECT_NUMBER"))
	if err != nil || number < 1 {
		t.Fatal("invalid project number")
	}
	if repo == "brettinternet/backlog" && (number == 3 || number == 4) {
		t.Fatal("protected project")
	}
	projectID, fieldID := required("BL_GITHUB_TEST_PROJECT_ID"), required("BL_GITHUB_TEST_FIELD_ID")
	todo, active := required("BL_GITHUB_TEST_TODO_OPTION"), required("BL_GITHUB_TEST_ACTIVE_OPTION")
	label := required("BL_GITHUB_TEST_LABEL")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	gh := func(out any, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "gh", args...)
		raw, err := cmd.Output()
		if err != nil {
			t.Fatalf("gh %s failed (provider output withheld): %v", strings.Join(args[:min(2, len(args))], " "), err)
		}
		if out != nil && json.Unmarshal(raw, out) != nil {
			t.Fatal("invalid gh JSON")
		}
	}
	var approved struct{ ID, Title string }
	gh(&approved, "project", "view", strconv.Itoa(number), "--owner", user, "--format", "json")
	if approved.ID != projectID || !strings.HasPrefix(approved.Title, "bl-provider-test-") {
		t.Fatal("project is not the explicit disposable test project")
	}
	var created struct {
		ID     string `json:"node_id"`
		Number int
		URL    string `json:"html_url"`
	}
	gh(&created, "api", "--method", "POST", "repos/"+repo+"/issues", "-f", "title=bl-provider-test BL-38 roundtrip", "-f", "body=Synthetic provider roundtrip; deleted by test cleanup.")
	if created.ID == "" || created.Number < 1 {
		t.Fatal("created issue identity missing")
	}
	t.Logf("created issue %s#%d node=%s", repo, created.Number, created.ID)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cleanupCtx, "gh", "api", "graphql", "-f", `query=mutation($id:ID!){deleteIssue(input:{issueId:$id}){clientMutationId}}`, "-f", "id="+created.ID)
		if err := cmd.Run(); err != nil {
			t.Errorf("issue cleanup failed: %v", err)
			return
		}
		t.Logf("deleted issue node=%s (also removes its comments)", created.ID)
	})
	var entry struct{ ID string }
	gh(&entry, "project", "item-add", strconv.Itoa(number), "--owner", user, "--url", created.URL, "--format", "json")
	if entry.ID == "" {
		t.Fatal("missing created project item")
	}
	t.Logf("created project item %s", entry.ID)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := exec.CommandContext(cleanupCtx, "gh", "project", "item-delete", strconv.Itoa(number), "--owner", user, "--id", entry.ID).Run(); err != nil {
			t.Errorf("project entry cleanup failed: %v", err)
			return
		}
		t.Logf("deleted project item %s", entry.ID)
	})
	gh(nil, "project", "item-edit", "--id", entry.ID, "--project-id", projectID, "--field-id", fieldID, "--single-select-option-id", todo)
	secrets := &remote.Secrets{}
	s, err := New(Options{ID: "gh", Repo: repo, Account: project.AccountConfig{Host: "github.com", User: user, Credential: []string{"gh", "auth", "token", "--hostname", "github.com", "--user", user}}, Project: &project.GitHubProjectConfig{ID: projectID, Owner: user, Number: number, FieldID: fieldID, Options: map[string]string{todo: "todo", active: "active"}, AllowWrites: true}, Secrets: secrets, Credentials: &remote.Credentials{Secrets: secrets}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.Read(ctx, strconv.Itoa(created.Number))
	if err != nil {
		t.Fatal(err)
	}
	await := func(ready func(model.Item) bool) {
		t.Helper()
		deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		// The next write's preflight needs a settled version, not only the
		// expected value: the composite version can keep moving after it appears.
		previous := ""
		for !ready(item) || item.Version != previous {
			if deadline.Err() != nil {
				t.Fatalf("provider read-back did not converge: status=%s fields=%v", item.Status, item.Fields)
			}
			previous = item.Version
			item, err = s.Read(deadline, strconv.Itoa(created.Number))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	await(func(i model.Item) bool { return i.Category == model.Todo })
	change := func(m model.Mutation) {
		t.Helper()
		var err error
		m.Ref, m.ExpectedVersion = item.Ref, item.Version
		item, err = s.Apply(ctx, m)
		if err != nil {
			t.Fatalf("%s %s%s: %v", m.Op, m.Field, m.Section, err)
		}
	}
	apply := func(field string, value any) {
		t.Helper()
		change(model.Mutation{Op: model.Set, Field: field, Value: value})
	}
	apply("status", active)
	await(func(i model.Item) bool { return i.Category == model.Active })
	apply("labels", []string{label})
	await(func(i model.Item) bool { return len(i.Labels) == 1 && i.Labels[0] == label })
	apply("assignees", []string{user})
	await(func(i model.Item) bool { return len(i.Assignees) == 1 && i.Assignees[0] == user })
	change(model.Mutation{Op: model.Remove, Field: "labels", Value: label})
	await(func(i model.Item) bool { return len(i.Labels) == 0 })
	change(model.Mutation{Op: model.Add, Field: "labels", Value: label})
	await(func(i model.Item) bool { return len(i.Labels) == 1 && i.Labels[0] == label })
	change(model.Mutation{Op: model.Remove, Field: "assignees", Value: user})
	await(func(i model.Item) bool { return len(i.Assignees) == 0 })
	change(model.Mutation{Op: model.Add, Field: "assignees", Value: user})
	await(func(i model.Item) bool { return len(i.Assignees) == 1 && i.Assignees[0] == user })
	apply("title", "bl-provider-test BL-119 renamed")
	await(func(i model.Item) bool { return i.Title == "bl-provider-test BL-119 renamed" })
	change(model.Mutation{Op: model.ReplaceSection, Section: "Description", Text: "Synthetic provider roundtrip; edited body."})
	await(func(i model.Item) bool { return i.Description == "Synthetic provider roundtrip; edited body." })
	item, err = s.Apply(ctx, model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.AppendComment, Text: "bl-provider-test BL-38 comment"})
	if err != nil {
		t.Fatal(err)
	}
	var comments []struct {
		ID   int64
		Body string
	}
	gh(&comments, "api", fmt.Sprintf("repos/%s/issues/%d/comments", repo, created.Number))
	if len(comments) != 1 || comments[0].Body != "bl-provider-test BL-38 comment" {
		t.Fatal("comment roundtrip failed")
	}
	t.Logf("created comment %d", comments[0].ID)
	apply("status", "closed")
	await(func(i model.Item) bool { return i.Category == model.Done })
	gh(nil, "api", "--method", "DELETE", fmt.Sprintf("repos/%s/issues/comments/%d", repo, comments[0].ID))
	t.Logf("deleted comment %d", comments[0].ID)

	// Create a child of the roundtrip issue, blocked by it, in the project.
	parent := item.Ref
	child, err := s.Apply(ctx, model.Mutation{Ref: model.Ref{Source: "gh"}, Op: model.Create, Item: &model.Item{Title: "bl-provider-test BL-118 create", Description: "Synthetic provider create; undone by test.", Labels: []string{label}, Assignees: []string{user}, Status: active, Parent: &parent, BlockedBy: []model.Ref{parent}}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	childNode, _ := child.Fields["id"].(string)
	t.Logf("created issue %s#%s node=%s", repo, child.Ref.ID, childNode)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		// Undo may already have deleted it; a failure here is then expected.
		if exec.CommandContext(cleanupCtx, "gh", "api", "graphql", "-f", `query=mutation($id:ID!){deleteIssue(input:{issueId:$id}){clientMutationId}}`, "-f", "id="+childNode).Run() == nil {
			t.Logf("deleted issue node=%s", childNode)
		}
	})
	// Projects v2 can lag the create's read-back. Undo also needs a settled
	// version; once it moves past create's, undo closes instead of deleting.
	deadline, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	for previous := ""; child.Category != model.Active || child.Version != previous; {
		if deadline.Err() != nil {
			t.Fatalf("create read-back: status=%s labels=%v assignees=%v parent=%v blockedBy=%v", child.Status, child.Labels, child.Assignees, child.Parent, child.BlockedBy)
		}
		previous = child.Version
		if child, err = s.Read(deadline, child.Ref.ID); err != nil {
			t.Fatal(err)
		}
	}
	if child.Title != "bl-provider-test BL-118 create" || !slices.Equal(child.Labels, []string{label}) || !slices.Equal(child.Assignees, []string{user}) || child.Parent == nil || *child.Parent != parent || !slices.Equal(child.BlockedBy, []model.Ref{parent}) {
		t.Fatalf("create read-back: labels=%v assignees=%v parent=%v blockedBy=%v", child.Labels, child.Assignees, child.Parent, child.BlockedBy)
	}
	undone, err := s.Apply(ctx, model.Mutation{Ref: child.Ref, ExpectedVersion: child.Version, Op: model.Delete})
	if err != nil {
		t.Fatalf("undo create: %v", err)
	}
	if undone.Version == "" {
		t.Log("undo deleted the created issue")
	} else if undone.Category != model.Canceled {
		t.Fatalf("undo neither deleted nor closed as not planned: %s", undone.Status)
	} else {
		t.Log("undo closed the created issue as not planned")
	}

	// A plain create undone at once is unchanged, so the repository admin deletes it.
	plain, err := s.Apply(ctx, model.Mutation{Ref: model.Ref{Source: "gh"}, Op: model.Create, Item: &model.Item{Title: "bl-provider-test BL-118 delete", Description: "Synthetic provider create; deleted by undo."}})
	if err != nil {
		t.Fatalf("plain create: %v", err)
	}
	plainNode, _ := plain.Fields["id"].(string)
	t.Logf("created issue %s#%s node=%s", repo, plain.Ref.ID, plainNode)
	deleted, err := s.Apply(ctx, model.Mutation{Ref: plain.Ref, ExpectedVersion: plain.Version, Op: model.Delete})
	if err != nil || deleted.Version != "" {
		cmd := exec.CommandContext(context.Background(), "gh", "api", "graphql", "-f", `query=mutation($id:ID!){deleteIssue(input:{issueId:$id}){clientMutationId}}`, "-f", "id="+plainNode)
		t.Fatalf("undo didn't delete the unchanged issue: status=%s err=%v (cleanup: %v)", deleted.Status, err, cmd.Run())
	}
	var lookup struct {
		Data struct{ Node *struct{ ID string } }
	}
	cmd := exec.CommandContext(ctx, "gh", "api", "graphql", "-f", `query=query($id:ID!){node(id:$id){id}}`, "-f", "id="+plainNode)
	raw, _ := cmd.Output() // NOT_FOUND exits 1 with the null node on stdout.
	if json.Unmarshal(raw, &lookup) != nil || lookup.Data.Node != nil {
		t.Fatal("deleted issue still resolves")
	}
	t.Logf("undo deleted issue node=%s", plainNode)
}
