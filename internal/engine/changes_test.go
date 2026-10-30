package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

// Catches a GitLab origin read without its one GitLab account, or as a
// GitHub forge, a forge picked when the host's account is ambiguous, and a
// GitLab forge borrowing a GitHub account.
func TestOriginForge(t *testing.T) {
	testkit.Isolate(t)
	common := t.TempDir()
	if err := os.WriteFile(filepath.Join(common, "config"), []byte("[remote \"origin\"]\n\turl = git@gitlab.example.com:grp/sub/app.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := project.Config{Discovery: project.Discovery{CommonDir: common}, Accounts: map[string][]project.AccountConfig{
		"github": {{Name: "hub", Host: "gitlab.example.com", User: "alice", Env: "TOKEN"}},
		"gitlab": {{Name: "work", Host: "gitlab.example.com", User: "alice", Env: "TOKEN"}},
	}}
	names := func(cfg project.Config) []string {
		var out []string
		for _, f := range forges(cfg) {
			out = append(out, f.Name()+" "+f.Web())
		}
		return out
	}
	if got := names(cfg); len(got) != 0 {
		t.Fatalf("both a GitHub and a GitLab account: %v", got)
	}
	delete(cfg.Accounts, "github")
	if got := names(cfg); len(got) != 1 || got[0] != "gitlab grp/sub/app https://gitlab.example.com/grp/sub/app/-" {
		t.Fatalf("one GitLab account: %v", got)
	}
	cfg.Accounts["gitlab"] = append(cfg.Accounts["gitlab"], project.AccountConfig{Name: "other", Host: "gitlab.example.com", User: "bob", Env: "TOKEN"})
	if got := names(cfg); len(got) != 0 {
		t.Fatalf("two GitLab accounts: %v", got)
	}
	cfg.Accounts = map[string][]project.AccountConfig{"github": {{Name: "hub", Host: "gitlab.com", User: "alice", Env: "TOKEN"}}}
	if _, err := forgeAccount(cfg, project.ForgeConfig{Type: "gitlab", Host: "gitlab.com", Repo: "grp/app"}); err == nil {
		t.Fatal("a GitLab forge used a GitHub account")
	}
}
