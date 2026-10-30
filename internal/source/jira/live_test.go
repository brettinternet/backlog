package jira_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source/jira"
	"github.com/brettinternet/backlog/internal/source/remote"
)

// Opt-in. Creates one synthetic issue in the given project, transitions it
// once through the adapter, reads its changelog, and deletes it.
func TestLiveActivity(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1, BL_JIRA_TEST_SITE, BL_JIRA_TEST_EMAIL, and BL_JIRA_TEST_PROJECT")
	}
	site, email, key := strings.TrimSuffix(os.Getenv("BL_JIRA_TEST_SITE"), "/"), os.Getenv("BL_JIRA_TEST_EMAIL"), os.Getenv("BL_JIRA_TEST_PROJECT")
	if site == "" || email == "" || key == "" {
		t.Fatal("BL_JIRA_TEST_SITE, BL_JIRA_TEST_EMAIL, and BL_JIRA_TEST_PROJECT required")
	}
	helper := []string{"security", "find-generic-password", "-s", "jira-api", "-w"}
	if raw := os.Getenv("BL_JIRA_TEST_HELPER"); raw != "" {
		if json.Unmarshal([]byte(raw), &helper) != nil || len(helper) == 0 {
			t.Fatal("invalid helper argv JSON")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Bootstrap discovers the accountId and creates and deletes the issue,
	// which the adapter doesn't do. The token never leaves memory.
	token, err := exec.CommandContext(ctx, helper[0], helper[1:]...).Output()
	if err != nil {
		t.Fatal("credential helper failed (output withheld)")
	}
	token = bytes.TrimSpace(token)
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	api := func(ctx context.Context, method, path string, body, out any) int {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req, _ := http.NewRequestWithContext(ctx, method, site+"/rest/api/3/"+path, reader)
		req.SetBasicAuth(email, string(token))
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s failed", method, path)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if out != nil && json.Unmarshal(data, out) != nil {
			t.Fatalf("%s %s: invalid response (%d)", method, path, response.StatusCode)
		}
		return response.StatusCode
	}
	var me struct{ AccountID string }
	if api(ctx, http.MethodGet, "myself", nil, &me) != 200 || me.AccountID == "" {
		t.Fatal("identity discovery refused")
	}
	var created struct{ ID, Key string }
	if code := api(ctx, http.MethodPost, "issue", map[string]any{"fields": map[string]any{"project": map[string]any{"key": key}, "summary": "bl-provider-test BL-140 activity", "issuetype": map[string]any{"name": "Task"}}}, &created); code != 201 || created.ID == "" {
		t.Fatalf("create refused: %d", code)
	}
	t.Cleanup(func() {
		defer clear(token)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if code := api(cleanupCtx, http.MethodDelete, "issue/"+created.ID, nil, nil); code != 204 {
			t.Errorf("cleanup failed for %s: %d", created.Key, code)
		} else if code := api(cleanupCtx, http.MethodGet, "issue/"+created.ID, nil, nil); code != 404 {
			t.Errorf("deleted issue %s still readable: %d", created.Key, code)
		} else {
			t.Logf("deleted issue %s (404 verified)", created.Key)
		}
	})
	t.Logf("created issue %s", created.Key)
	secrets := &remote.Secrets{}
	s, err := jira.New(jira.Options{ID: "jira", Site: site, JQL: "project = " + key + " ORDER BY id ASC", AllowWrites: true, Account: project.AccountConfig{User: me.AccountID, Email: email, Credential: helper}, Secrets: secrets, Credentials: &remote.Credentials{Secrets: secrets}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.Read(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	choices, _, err := s.StatusChoices(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	target := ""
	for _, choice := range choices {
		if choice.Key != item.Status && target == "" {
			target = choice.Key
		}
	}
	if target == "" {
		t.Fatal("no transition offered")
	}
	if item, err = s.Apply(ctx, model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "status", Value: target}); err != nil {
		t.Fatal(err)
	}
	activity, truncated, err := s.Activity(ctx, item)
	if err != nil || truncated {
		t.Fatalf("activity read failed: truncated=%v err=%v", truncated, err)
	}
	for _, a := range activity {
		if strings.HasPrefix(a.Summary, "status ") || strings.Contains(a.Summary, "; status ") {
			t.Logf("%d entries; %q", len(activity), a.Summary)
			return
		}
	}
	t.Fatalf("no status change in %d entries", len(activity))
}
