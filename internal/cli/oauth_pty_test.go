// The CLI auth lifecycle drives interactive prompts through a PTY.

//go:build unix

package cli_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/cli"
	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
	"github.com/brettinternet/backlog/internal/tui/ptytest"
)

type cliTestOAuthProvider struct {
	n          int
	authorized [][]string
	principals map[string]oauth.Principal
}

func (*cliTestOAuthProvider) ID() string                         { return "cli-fake" }
func (*cliTestOAuthProvider) ReadScopes(oauth.Account) []string  { return []string{"read"} }
func (*cliTestOAuthProvider) WriteScopes(oauth.Account) []string { return []string{"write"} }
func (p *cliTestOAuthProvider) Authorize(_ context.Context, account oauth.Account, scopes []string, _ *oauth.Tokens, interaction oauth.Interaction) (oauth.Tokens, error) {
	if strings.HasPrefix(account.Name, "error-") {
		return oauth.Tokens{}, oauth.Failure(strings.TrimPrefix(account.Name, "error-"), time.Time{})
	}
	fmt.Fprintf(interaction.Out, "authorize request %d scopes=%s\n", p.n+1, strings.Join(scopes, ","))
	consent, err := bufio.NewReader(interaction.In).ReadString('\n')
	if err != nil || strings.TrimSpace(consent) != "yes" {
		return oauth.Tokens{}, oauth.Failure(oauth.CodeInteractionRequired, time.Time{})
	}
	p.n++
	p.authorized = append(p.authorized, append([]string(nil), scopes...))
	access := fmt.Sprintf("oauth-cli-canary-access-%d", p.n)
	refresh := fmt.Sprintf("oauth-cli-canary-refresh-%d", p.n)
	if p.principals == nil {
		p.principals = map[string]oauth.Principal{}
	}
	p.principals[access] = oauth.Principal{ID: "cli-immutable-user", Login: account.User, Scopes: append([]string(nil), scopes...)}
	return oauth.Tokens{AccessToken: access, RefreshToken: refresh, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (*cliTestOAuthProvider) Refresh(context.Context, oauth.Account, oauth.Tokens) (oauth.Tokens, error) {
	return oauth.Tokens{}, oauth.Failure(oauth.CodeProviderDown, time.Time{})
}
func (p *cliTestOAuthProvider) Verify(_ context.Context, account oauth.Account, access string) (oauth.Principal, error) {
	principal, ok := p.principals[access]
	if !ok || principal.Login != account.User {
		return oauth.Principal{}, oauth.Failure(oauth.CodePrincipalMismatch, time.Time{})
	}
	return principal, nil
}
func (p *cliTestOAuthProvider) Rediscover(context.Context, oauth.Account, string) error { return nil }
func (*cliTestOAuthProvider) Revoke(context.Context, oauth.Account, oauth.Tokens) (bool, error) {
	return true, nil
}

func TestOAuthCLIAuthLifecycleHelper(t *testing.T) {
	if os.Getenv("BL_OAUTH_CLI_CHILD") != "1" {
		return
	}
	t.Chdir(t.TempDir())
	provider := &cliTestOAuthProvider{}
	if err := oauth.RegisterDefault(provider); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		code := cli.Run(context.Background(), append([]string{"bl"}, args...), cli.BuildInfo{Version: "test"}, os.Stdin, os.Stdout, os.Stderr)
		if code != 0 {
			t.Fatalf("CLI command %v exited %d", args, code)
		}
	}
	// A fresh --write is one write grant; after plain login, --write upgrades it.
	run("auth", "login", "cli-fake", "work", "--write")
	run("auth", "login", "cli-fake", "work")
	run("auth", "login", "cli-fake", "work", "--write")
	if len(provider.authorized) != 3 || strings.Join(provider.authorized[0], ",") != "read,write" || strings.Join(provider.authorized[1], ",") != "read" || strings.Join(provider.authorized[2], ",") != "read,write" {
		t.Fatalf("authorization scopes: %v", provider.authorized)
	}
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	cacheStore := cache.NewPrincipal(paths.CacheDir, "cli-oauth-test", "issues", "identity", oauth.PrincipalKey("cli-fake", "", "alice"))
	if err = os.MkdirAll(filepath.Dir(cacheStore.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cacheStore.Path, []byte("cached row"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("auth", "logout", "cli-fake", "work")
	for _, code := range []string{
		oauth.CodeAuthenticationRequired, oauth.CodeProviderUnavailable, oauth.CodeTokenExpired,
		oauth.CodeGrantRevoked, oauth.CodeInsufficientScope, oauth.CodeSSORequired,
		oauth.CodeRateLimited, oauth.CodeProviderDown, oauth.CodePrincipalMismatch,
		oauth.CodeStorageUnavailable,
	} {
		if got := cli.Run(context.Background(), []string{"bl", "--json", "auth", "login", "cli-fake", "error-" + code}, cli.BuildInfo{}, os.Stdin, os.Stdout, os.Stderr); got != 6 {
			t.Fatalf("structured CLI error %s exit=%d, want 6", code, got)
		}
	}
	if got := cli.Run(context.Background(), []string{"bl", "--json", "auth", "login", "cli-fake", "not-opted-in"}, cli.BuildInfo{}, os.Stdin, os.Stdout, os.Stderr); got != 5 {
		t.Fatalf("non-opt-in CLI error exit=%d, want 5", got)
	}
	if _, err = os.Stat(filepath.Dir(cacheStore.Path)); !os.IsNotExist(err) {
		t.Fatalf("logout did not purge account cache: %v", err)
	}
	for _, entry := range mustDir(t, filepath.Join(paths.StateDir, "oauth")) {
		if strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("logout retained token file %s", entry.Name())
		}
	}
	fmt.Fprintln(os.Stdout, "OAUTH_CLI_LIFECYCLE_COMPLETE")
}

func TestOAuthPublicCLIAuthLifecycle(t *testing.T) {
	testkit.Isolate(t)
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(paths.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	var config strings.Builder
	config.WriteString("accounts:\n  cli-fake:\n    - name: work\n      user: alice\n      oauth: true\n")
	for _, code := range []string{
		oauth.CodeAuthenticationRequired, oauth.CodeProviderUnavailable, oauth.CodeTokenExpired,
		oauth.CodeGrantRevoked, oauth.CodeInsufficientScope, oauth.CodeSSORequired,
		oauth.CodeRateLimited, oauth.CodeProviderDown, oauth.CodePrincipalMismatch,
		oauth.CodeStorageUnavailable,
	} {
		fmt.Fprintf(&config, "    - name: error-%s\n      user: alice\n      oauth: true\n", code)
	}
	config.WriteString("    - name: not-opted-in\n      user: alice\n      oauth: false\n")
	if err = os.WriteFile(paths.ConfigFile, []byte(config.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestOAuthCLIAuthLifecycleHelper$")
	command.Env = append(os.Environ(), "BL_OAUTH_CLI_CHILD=1", "TERM=xterm-256color")
	session, err := ptytest.Start(command, 100, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, prompt := range []string{
		"authorize request 1 scopes=read,write",
		"authorize request 2 scopes=read",
		"authorize request 3 scopes=read,write",
	} {
		if err = session.WaitFor(prompt, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		if err = session.Write("yes\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err = session.WaitFor("OAUTH_CLI_LIFECYCLE_COMPLETE", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	transcript := session.Output()
	for _, code := range []string{
		oauth.CodeAuthenticationRequired, oauth.CodeProviderUnavailable, oauth.CodeTokenExpired,
		oauth.CodeGrantRevoked, oauth.CodeInsufficientScope, oauth.CodeSSORequired,
		oauth.CodeRateLimited, oauth.CodeProviderDown, oauth.CodePrincipalMismatch,
		oauth.CodeStorageUnavailable, oauth.CodeNotOptedIn,
	} {
		if !strings.Contains(transcript, `"reason":"`+code+`"`) {
			t.Fatalf("CLI omitted structured diagnostic %s: %s", code, transcript)
		}
	}
	for _, secret := range []string{"oauth-cli-canary-access-", "oauth-cli-canary-refresh-"} {
		if strings.Contains(transcript, secret) {
			t.Fatalf("CLI lifecycle printed token prefix %q", secret)
		}
	}
	configBytes, err := os.ReadFile(paths.ConfigFile)
	if err != nil || strings.Contains(string(configBytes), "oauth-cli-canary-") {
		t.Fatalf("CLI lifecycle wrote token to config: %s %v", configBytes, err)
	}
}

func mustDir(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
