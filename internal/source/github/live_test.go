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
	"github.com/brettinternet/backlog/internal/source"
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
	full, err := s.Load(ctx, source.Cursor{})
	if err != nil || full.Incremental {
		t.Fatalf("reconciliation: %v", err)
	}
	// A change window from the reconciled cursor must see each edit.
	awaitWindow := func(ready func(model.Item) bool) {
		t.Helper()
		deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		for deadline.Err() == nil {
			delta, err := s.Load(deadline, full.Cursor)
			if err != nil || !delta.Incremental {
				t.Fatalf("change window: %v", err)
			}
			for _, i := range delta.Items {
				if i.Ref == item.Ref && ready(i) {
					return
				}
			}
		}
		t.Fatal("change window missed the edit")
	}
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
	awaitWindow(func(i model.Item) bool { return i.Category == model.Active })
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
	awaitWindow(func(i model.Item) bool {
		return i.Category == model.Done && i.Title == "bl-provider-test BL-119 renamed" && len(i.Labels) == 1 && i.Labels[0] == label
	})
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
	if err = setStatus("open"); err != nil {
		t.Fatal(err)
	}
	await(func(i model.Item) bool {
		return i.Status == "open" && i.Category == model.Todo && labels(other)(i)
	})
	if err = setStatus("closed"); err != nil {
		t.Fatal(err)
	}
	await(func(i model.Item) bool { return i.Category == model.Done })
	if err = setStatus(ready); !reason(err, "rejected") {
		t.Fatalf("label status on closed issue: %v", err)
	}
}

// Opt-in only, with TestLiveRoundTrip's inputs. Reads the disposable project
// through a project-only source: an OWNER/REPO#N ID, a write to the item's
// repository and project, and a change window that sees a comment-only edit,
// which leaves the project item's updatedAt alone.
func TestLiveProjectOnly(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 and explicit GitHub test inputs")
	}
	required := func(key string) string { return liveRequired(t, key) }
	repo, user := required("BL_GITHUB_TEST_REPO"), required("BL_GITHUB_TEST_USER")
	number, err := strconv.Atoi(required("BL_GITHUB_TEST_PROJECT_NUMBER"))
	if err != nil || number < 1 {
		t.Fatal("invalid project number")
	}
	projectID, fieldID := required("BL_GITHUB_TEST_PROJECT_ID"), required("BL_GITHUB_TEST_FIELD_ID")
	todo, active := required("BL_GITHUB_TEST_TODO_OPTION"), required("BL_GITHUB_TEST_ACTIVE_OPTION")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
	gh(&created, "api", "--method", "POST", "repos/"+repo+"/issues", "-f", "title=bl-provider-test BL-129 project-only", "-f", "body=Synthetic provider test; deleted by test cleanup.")
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
		t.Logf("deleted issue node=%s (also removes its comments and project item)", created.ID)
	})
	var entry struct{ ID string }
	gh(&entry, "project", "item-add", strconv.Itoa(number), "--owner", user, "--url", created.URL, "--format", "json")
	if entry.ID == "" {
		t.Fatal("missing created project item")
	}
	t.Logf("created project item %s", entry.ID)
	gh(nil, "project", "item-edit", "--id", entry.ID, "--project-id", projectID, "--field-id", fieldID, "--single-select-option-id", todo)
	// Shifting the clock by the change overlap starts the window at the
	// reconciliation's real start, after every project item edit above.
	shift := changeOverlap
	secrets := &remote.Secrets{}
	s, err := New(Options{ID: "gh", Account: project.AccountConfig{Host: "github.com", User: user, Credential: []string{"gh", "auth", "token", "--hostname", "github.com", "--user", user}}, Project: &project.GitHubProjectConfig{ID: projectID, Owner: user, Number: number, FieldID: fieldID, Options: map[string]string{todo: "todo", active: "active"}, AllowWrites: true}, Secrets: secrets, Credentials: &remote.Credentials{Secrets: secrets}, Now: func() time.Time { return time.Now().Add(shift) }})
	if err != nil {
		t.Fatal(err)
	}
	if s.Info().Capabilities.Allows(model.Create) {
		t.Fatal("project-only source offers create")
	}
	id := fmt.Sprintf("%s#%d", repo, created.Number)
	var item model.Item
	await := func(ready func(model.Item) bool) {
		t.Helper()
		deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		previous := ""
		for item.Ref.ID == "" || !ready(item) || item.Version != previous {
			if deadline.Err() != nil {
				t.Fatalf("provider read-back did not converge: status=%s title=%s", item.Status, item.Title)
			}
			// A comment's effects reach reads unevenly; space the reads so
			// two equal versions mean settled, not one lagging replica.
			if previous = item.Version; previous != "" {
				time.Sleep(2 * time.Second)
			}
			if item, err = s.Read(deadline, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	await(func(i model.Item) bool { return i.Category == model.Todo })
	if item.Ref != (model.Ref{Source: "gh", ID: id}) {
		t.Fatalf("project-only ref: %+v", item.Ref)
	}
	time.Sleep(5 * time.Second)
	full, err := s.Load(ctx, source.Cursor{})
	if err != nil || full.Incremental || !slices.ContainsFunc(full.Items, func(i model.Item) bool { return i.Ref.ID == id }) {
		t.Fatalf("reconciliation: %v", err)
	}
	time.Sleep(5 * time.Second)
	gh(nil, "issue", "comment", strconv.Itoa(created.Number), "--repo", repo, "--body", "bl-provider-test BL-129 comment")
	t.Log("created comment")
	deadline, stop := context.WithTimeout(ctx, 60*time.Second)
	defer stop()
	for found := false; !found; {
		if deadline.Err() != nil {
			t.Fatal("change window missed the comment-only edit")
		}
		delta, err := s.Load(deadline, full.Cursor)
		if err != nil || !delta.Incremental {
			t.Fatalf("change window: %v", err)
		}
		found = slices.ContainsFunc(delta.Items, func(i model.Item) bool { return i.Ref.ID == id && i.Updated.After(item.Updated) })
	}
	await(func(model.Item) bool { return true })
	if item, err = s.Apply(ctx, model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "title", Value: "bl-provider-test BL-129 renamed"}); err != nil {
		t.Fatal(err)
	}
	await(func(i model.Item) bool { return i.Title == "bl-provider-test BL-129 renamed" })
	if item, err = s.Apply(ctx, model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "status", Value: active}); err != nil {
		t.Fatal(err)
	}
	await(func(i model.Item) bool { return i.Category == model.Active })
}
