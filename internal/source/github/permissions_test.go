package github

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/oauth"
)

// The HTTP fixture supplies live app permissions; this provider only seeds the
// existing private-store seam. Native GitHub device exchange is tested in oauth.
type permissionTestProvider struct{}

func (permissionTestProvider) ID() string                         { return "github" }
func (permissionTestProvider) ReadScopes(oauth.Account) []string  { return []string{"read"} }
func (permissionTestProvider) WriteScopes(oauth.Account) []string { return []string{"write"} }
func (permissionTestProvider) Authorize(_ context.Context, _ oauth.Account, scopes []string, _ *oauth.Tokens, _ oauth.Interaction) (oauth.Tokens, error) {
	return oauth.Tokens{AccessToken: "fake-secret", ExpiresAt: time.Now().Add(time.Hour), GrantedScopes: scopes}, nil
}
func (permissionTestProvider) Refresh(context.Context, oauth.Account, oauth.Tokens) (oauth.Tokens, error) {
	panic("unexpected refresh")
}
func (permissionTestProvider) Verify(context.Context, oauth.Account, string) (oauth.Principal, error) {
	return oauth.Principal{ID: "42", Login: "alice"}, nil
}
func (permissionTestProvider) Rediscover(context.Context, oauth.Account, string) error { return nil }
func (permissionTestProvider) Revoke(context.Context, oauth.Account, oauth.Tokens) (bool, error) {
	return false, nil
}

// A user's WRITE role and misleading classic scope header cannot override read
// app consent. Permission loss, removed repositories and personal projects must
// fail before dispatch; approved app/project writes must still work.
func TestOAuthInstallationPermissionGatedWrites(t *testing.T) {
	s, f := newFixture(t)
	ctx := context.Background()
	s.o.Account.OAuth = true
	s.client.OAuth = true
	s.o.Account.Env = ""
	s.o.Account.Provider = "github"
	s.o.Account.Name = "work"
	s.o.Project.Owner = "owner"
	account := oauth.Account{Provider: "github", Name: "work", Host: "github.com", User: "alice", OptIn: true}
	manager := oauth.NewManager(oauth.Store{StateDir: filepath.Join(t.TempDir(), "state")}, "", oauth.Registry{"github": permissionTestProvider{}}, s.o.Secrets)
	s.o.Credentials.OAuth = manager
	f.oauthApp = oauth.GitHubReadApp
	f.issuePermission = "read"
	f.projectPermission = "read"
	interaction := oauth.Interaction{In: strings.NewReader(""), Out: io.Discard}
	if err := manager.Login(ctx, account, interaction); err != nil {
		t.Fatal(err)
	}
	current, err := s.Read(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	apply := func(field string, value any) (model.Item, error) {
		return s.Apply(ctx, model.Mutation{Ref: current.Ref, Op: model.Set, Field: field, Value: value, ExpectedVersion: current.Version})
	}
	if _, err = apply("status", "closed"); err == nil || !strings.Contains(err.Error(), "bl auth login github work --write") {
		t.Fatalf("read app wrote issue or omitted --write: %v", err)
	}
	if _, err = apply("status", "active"); err == nil {
		t.Fatal("classic project header bypassed read app")
	}
	if err = manager.EnableWrites(ctx, account, interaction); err != nil {
		t.Fatal(err)
	}
	f.oauthApp = oauth.GitHubWriteApp
	f.issuePermission = "write"
	f.projectPermission = "write"
	current, err = apply("status", "active")
	if err != nil {
		t.Fatal(err)
	}
	if current.Category != model.Active {
		t.Fatal("approved project mutation not reflected")
	}
	f.projectPermission = "read"
	if _, err = apply("status", "todo"); err == nil {
		t.Fatal("revoked project write grant still allowed")
	}
	f.issuePermission = "read"
	if _, err = apply("status", "closed"); err == nil || strings.Contains(err.Error(), "--write") {
		t.Fatalf("revoked issue write grant still allowed or blamed --write: %v", err)
	}
	f.issuePermission = "write"
	current, err = apply("status", "closed")
	if err != nil {
		t.Fatal(err)
	}
	f.repositoryMissing = true
	if _, err = apply("status", "open"); err == nil {
		t.Fatal("removed repository still writable")
	}
	f.repositoryMissing = false
	f.personalProject = true
	if _, err = s.Read(ctx, "1"); err == nil {
		t.Fatal("personal project treated as app-supported")
	}
	if f.writes != 2 {
		t.Fatalf("unexpected mutation count %d", f.writes)
	}
}
