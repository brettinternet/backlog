package gitlab

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source/remote"
)

// Opt-in only, and read-only: lists a real project's merge requests and reads
// the newest one's detail, so the query matches GitLab's schema.
func TestLiveMergeRequests(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1, BL_GITLAB_TEST_REPO, BL_GITLAB_TEST_USER, and BL_GITLAB_TEST_TOKEN")
	}
	repo, user := os.Getenv("BL_GITLAB_TEST_REPO"), os.Getenv("BL_GITLAB_TEST_USER")
	if repo == "" || user == "" || os.Getenv("BL_GITLAB_TEST_TOKEN") == "" {
		t.Fatal("BL_GITLAB_TEST_REPO, BL_GITLAB_TEST_USER, and BL_GITLAB_TEST_TOKEN are required")
	}
	host := os.Getenv("BL_GITLAB_TEST_HOST")
	if host == "" {
		host = "gitlab.com"
	}
	secrets := &remote.Secrets{}
	forge, err := NewForge(Options{Host: host, Repo: repo, Account: project.AccountConfig{Host: host, User: user, Env: "BL_GITLAB_TEST_TOKEN"}, Secrets: secrets, Credentials: &remote.Credentials{Secrets: secrets}}, filepath.Join(t.TempDir(), "live.pulls.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	prs, err := forge.PullRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d merge requests", len(prs))
	if len(prs) == 0 {
		return
	}
	if prs[0].Number < 1 || prs[0].URL == "" || prs[0].State == "" {
		t.Fatalf("newest: %+v", prs[0])
	}
	d, err := forge.Pull(ctx, prs[0].Number)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("!%d: %d files, %d commits", prs[0].Number, len(d.Files), len(d.Commits))
}
