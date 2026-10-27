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
	required := func(key string) string { return liveRequired(t, key) }
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
	gh := liveGH(t, ctx)
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
		for !ready(item) {
			if deadline.Err() != nil {
				t.Fatalf("provider read-back did not converge: status=%s fields=%v", item.Status, item.Fields)
			}
			item, err = s.Read(deadline, strconv.Itoa(created.Number))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	await(func(i model.Item) bool { return i.Category == model.Todo })
	apply := func(field string, value any) {
		t.Helper()
		var err error
		item, err = s.Apply(ctx, model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: field, Value: value})
		if err != nil {
			t.Fatalf("set %s: %v", field, err)
		}
	}
	apply("status", active)
	await(func(i model.Item) bool { return i.Category == model.Active })
	apply("labels", []string{label})
	await(func(i model.Item) bool { return len(i.Labels) == 1 && i.Labels[0] == label })
	apply("assignees", []string{user})
	await(func(i model.Item) bool { return len(i.Assignees) == 1 && i.Assignees[0] == user })
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
}

func liveRequired(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s required", key)
	}
	return v
}
func liveGH(t *testing.T, ctx context.Context) func(out any, args ...string) {
	return func(out any, args ...string) {
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
}

// Opt-in only. Creates uniquely named bl-test-* labels and one issue, and
// deletes exactly those during cleanup; it never edits preexisting labels.
func TestLiveLabelStatus(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 and explicit GitHub test inputs")
	}
	repo, user := liveRequired(t, "BL_GITHUB_TEST_REPO"), liveRequired(t, "BL_GITHUB_TEST_USER")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	gh := liveGH(t, ctx)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	ready, active, other := "bl-test-ready-"+suffix, "bl-test-active-"+suffix, "bl-test-other-"+suffix
	for _, label := range []string{ready, active, other} {
		gh(nil, "label", "create", label, "--repo", repo, "--description", "bl provider test; deleted by test cleanup")
		t.Logf("created label %s", label)
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := exec.CommandContext(cleanupCtx, "gh", "label", "delete", label, "--repo", repo, "--yes").Run(); err != nil {
				t.Errorf("label cleanup failed: %v", err)
				return
			}
			t.Logf("deleted label %s", label)
		})
	}
	var created struct {
		ID     string `json:"node_id"`
		Number int
	}
	gh(&created, "api", "--method", "POST", "repos/"+repo+"/issues", "-f", "title=bl-provider-test BL-117 label status", "-f", "body=Synthetic provider roundtrip; deleted by test cleanup.", "-f", "labels[]="+ready, "-f", "labels[]="+other)
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
		t.Logf("deleted issue node=%s", created.ID)
	})
	secrets := &remote.Secrets{}
	s, err := New(Options{ID: "gh", Repo: repo, Account: project.AccountConfig{Host: "github.com", User: user, Credential: []string{"gh", "auth", "token", "--hostname", "github.com", "--user", user}}, StatusLabels: map[string]string{ready: "todo", active: "active"}, Secrets: secrets, Credentials: &remote.Credentials{Secrets: secrets}})
	if err != nil {
		t.Fatal(err)
	}
	var item model.Item
	await := func(converged func(model.Item) bool) {
		t.Helper()
		deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		for item.Ref.ID == "" || !converged(item) {
			if deadline.Err() != nil {
				t.Fatalf("provider read-back did not converge: status=%s labels=%v", item.Status, item.Labels)
			}
			item, err = s.Read(deadline, strconv.Itoa(created.Number))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	labels := func(want ...string) func(model.Item) bool {
		return func(i model.Item) bool {
			got := slices.Clone(i.Labels)
			slices.Sort(got)
			slices.Sort(want)
			return slices.Equal(got, want)
		}
	}
	setStatus := func(value string) error {
		next, err := s.Apply(ctx, model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "status", Value: value})
		if err == nil {
			item = next
		}
		return err
	}
	await(func(i model.Item) bool {
		return i.Status == ready && i.Category == model.Todo && labels(ready, other)(i)
	})
	if err = setStatus(active); err != nil {
		t.Fatal(err)
	}
	await(func(i model.Item) bool {
		return i.Status == active && i.Category == model.Active && labels(active, other)(i)
	})
	if err = setStatus("unknown"); err != nil {
		t.Fatal(err)
	}
	await(func(i model.Item) bool {
		return i.Status == "unknown" && i.Category == model.Backlog && labels(other)(i)
	})
	if err = setStatus("closed"); err != nil {
		t.Fatal(err)
	}
	await(func(i model.Item) bool { return i.Category == model.Done })
	if err = setStatus(ready); !reason(err, "rejected") {
		t.Fatalf("label status on closed issue: %v", err)
	}
}
