package cli_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/cli"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/mcp"
	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source/remote"
	"github.com/brettinternet/backlog/internal/testkit"
	"github.com/brettinternet/backlog/internal/tui/ptytest"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.yaml.in/yaml/v3"
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
	run("auth", "login", "cli-fake", "work")
	run("auth", "login", "cli-fake", "work", "--write")
	if len(provider.authorized) != 3 || strings.Join(provider.authorized[0], ",") != "read" || strings.Join(provider.authorized[1], ",") != "read" || strings.Join(provider.authorized[2], ",") != "read,write" {
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
		"authorize request 1 scopes=read",
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

const (
	oauthCanaryInitialAccess  = "oauth-access-canary-initial-5d2a"
	oauthCanaryInitialRefresh = "oauth-refresh-canary-initial-6c3f"
	oauthCanaryAccess         = "oauth-access-canary-rotated-9b71"
	oauthCanaryRefresh        = "oauth-refresh-canary-rotated-1e48"
)

type canaryOAuthProvider struct{}

func (canaryOAuthProvider) ID() string                         { return "github" }
func (canaryOAuthProvider) ReadScopes(oauth.Account) []string  { return []string{"read"} }
func (canaryOAuthProvider) WriteScopes(oauth.Account) []string { return []string{"write"} }
func (canaryOAuthProvider) Authorize(context.Context, oauth.Account, []string, *oauth.Tokens, oauth.Interaction) (oauth.Tokens, error) {
	return oauth.Tokens{AccessToken: oauthCanaryInitialAccess, RefreshToken: oauthCanaryInitialRefresh, ExpiresAt: time.Now().Add(-time.Minute)}, nil
}
func (canaryOAuthProvider) Refresh(context.Context, oauth.Account, oauth.Tokens) (oauth.Tokens, error) {
	return oauth.Tokens{AccessToken: oauthCanaryAccess, RefreshToken: oauthCanaryRefresh, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (canaryOAuthProvider) Verify(_ context.Context, account oauth.Account, token string) (oauth.Principal, error) {
	if token != oauthCanaryInitialAccess && token != oauthCanaryAccess {
		return oauth.Principal{}, oauth.Failure(oauth.CodeGrantRevoked, time.Time{})
	}
	return oauth.Principal{ID: "github-immutable-42", Login: account.User, Scopes: []string{"read"}}, nil
}
func (canaryOAuthProvider) Rediscover(context.Context, oauth.Account, string) error { return nil }
func (canaryOAuthProvider) Revoke(context.Context, oauth.Account, oauth.Tokens) (bool, error) {
	return true, nil
}

type githubCanaryAPI struct{}

func canaryGitHubIssue() map[string]any {
	return map[string]any{
		"id": "issue-immutable", "number": 1, "title": oauthCanaryAccess, "body": oauthCanaryRefresh,
		"state": "OPEN", "url": "https://github.fake/owner/repo/issues/1",
		"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-02T00:00:00Z",
		"repository": map[string]string{"nameWithOwner": "owner/repo"},
		"assignees":  map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}},
		"labels":     map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}},
		"blockedBy":  map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}},
		"subIssues":  map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}},
	}
}

func (githubCanaryAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+oauthCanaryAccess {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-OAuth-Scopes", "read")
	if r.URL.Path == "/api/v3/user/installations" {
		_, _ = io.WriteString(w, `{"total_count":1,"installations":[{"id":42,"app_slug":"bl-cli-read","account":{"login":"owner","type":"Organization"},"permissions":{"issues":"read"}}]}`)
		return
	}
	if r.URL.Path == "/api/v3/user/installations/42/repositories" {
		_, _ = io.WriteString(w, `{"total_count":1,"repositories":[{"id":1,"full_name":"owner/repo"}]}`)
		return
	}
	if r.URL.Path == "/api/v3/repos/owner/repo/issues" {
		_, _ = io.WriteString(w, "[]")
		return
	}
	if r.URL.Path != "/api/graphql" {
		http.NotFound(w, r)
		return
	}
	var request struct {
		Query string `json:"query"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		http.Error(w, "bad GraphQL request", http.StatusBadRequest)
		return
	}
	switch {
	case strings.Contains(request.Query, "viewer {login}"):
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"viewer": map[string]string{"login": "alice"}}})
	case strings.Contains(request.Query, "viewerPermission"):
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"id": "repo-immutable", "nameWithOwner": "owner/repo", "viewerPermission": "READ"}}})
	case strings.Contains(request.Query, "issue(number:$number)"):
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
			"id": "repo-immutable", "nameWithOwner": "owner/repo", "issue": canaryGitHubIssue(),
		}}})
	case strings.Contains(request.Query, "issues(first:100,after:$after"):
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
			"id": "repo-immutable", "nameWithOwner": "owner/repo",
			"issues": map[string]any{"nodes": []any{canaryGitHubIssue()}, "pageInfo": map[string]any{"hasNextPage": false}},
		}}})
	default:
		http.Error(w, "unrecognized query", http.StatusBadRequest)
	}
}

type canaryProcessCapture struct {
	Args        []string `json:"args"`
	Environment []string `json:"environment"`
	Stdin       string   `json:"stdin"`
}

func TestOAuthCanaryCaptureProcess(t *testing.T) {
	var capturePath string
	for _, arg := range os.Args[1:] {
		if strings.HasSuffix(arg, ".capture") {
			capturePath = arg
		}
	}
	if capturePath == "" {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(canaryProcessCapture{Args: os.Args, Environment: os.Environ(), Stdin: string(input)})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(capturePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthCanariesStayOutOfPublicAndAutomationSurfaces(t *testing.T) {
	if os.Getenv("BL_OAUTH_CANARY_CHILD") != "1" {
		command := exec.Command(os.Args[0], "-test.run=^TestOAuthCanariesStayOutOfPublicAndAutomationSurfaces$")
		command.Env = append(os.Environ(), "BL_OAUTH_CANARY_CHILD=1")
		output, err := command.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "OAUTH_CANARY_INTEGRATION_COMPLETE") {
			t.Fatalf("isolated OAuth canary integration: %v\n%s", err, output)
		}
		for _, canary := range []string{oauthCanaryInitialAccess, oauthCanaryInitialRefresh, oauthCanaryAccess, oauthCanaryRefresh} {
			if strings.Contains(string(output), canary) {
				t.Fatalf("child process output leaked OAuth canary %q", canary)
			}
		}
		return
	}
	testkit.Isolate(t)
	provider := canaryOAuthProvider{}
	server := httptest.NewTLSServer(githubCanaryAPI{})
	defer server.Close()
	originalTransport := http.DefaultTransport
	transport := &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
		transport.CloseIdleConnections()
	})

	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(paths.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	userConfig := "accounts:\n  github:\n    - name: work\n      host: github.fake\n      user: alice\n      oauth: true\n"
	if err = os.WriteFile(paths.ConfigFile, []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n bare = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hookCapture, actionCapture := filepath.Join(t.TempDir(), "hook.capture"), filepath.Join(t.TempDir(), "action.capture")
	probe := []string{os.Args[0], "-test.run=^TestOAuthCanaryCaptureProcess$"}
	settings := project.Settings{
		Version: 1, Project: project.ProjectConfig{ID: "example/oauth-canary"},
		Sources:  []project.SourceConfig{{ID: "issues", Type: "github", Host: "github.fake", Repo: "owner/repo", Account: "work"}},
		Statuses: []project.StatusConfig{{Key: "open", Name: "Open", Category: "todo"}},
		Ready:    project.ReadyConfig{Statuses: []string{"open"}, SatisfiedBy: []string{"done"}},
		Hooks:    []project.HookConfig{{Name: "capture", On: "item.created", Phase: "after", Run: append(append([]string(nil), probe...), hookCapture)}},
		Actions:  []project.ActionConfig{{Name: "capture", Run: append(append([]string(nil), probe...), actionCapture)}},
	}
	projectConfig, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	projectConfigPath := filepath.Join(root, "backlog.yaml")
	if err = os.MkdirAll(filepath.Dir(projectConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(projectConfigPath, projectConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = cfg.GrantTrust(); err != nil {
		t.Fatal(err)
	}
	account := oauth.Account{Provider: "github", Name: "work", Host: "github.fake", User: "alice", OptIn: true}
	manager := oauth.NewManager(oauth.Store{StateDir: paths.StateDir}, paths.CacheDir, oauth.Registry{"github": provider}, remote.ProcessSecrets)
	if err = manager.Login(context.Background(), account, oauth.Interaction{In: strings.NewReader("ok"), Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	resolved, err := manager.Resolve(context.Background(), account, func(context.Context, oauth.Tokens) (string, error) { return "alice", nil })
	if err != nil || resolved.AccessToken != oauthCanaryAccess || resolved.RefreshToken != oauthCanaryRefresh {
		t.Fatalf("OAuth rotation setup: %v", err)
	}
	canaries := []string{oauthCanaryInitialAccess, oauthCanaryInitialRefresh, oauthCanaryAccess, oauthCanaryRefresh}
	assertNoCanaries := func(surface string, data []byte) {
		t.Helper()
		for _, canary := range canaries {
			if strings.Contains(string(data), canary) {
				t.Fatalf("OAuth canary reached %s", surface)
			}
		}
	}
	assertNoCanaries("user/project config", append(append([]byte(nil), userConfig...), projectConfig...))

	for _, args := range [][]string{{"-C", root, "--json", "config", "show"}, {"-C", root, "--json", "ls"}} {
		code, out, stderr := run(args...)
		if code != 0 || stderr != "" {
			t.Fatalf("CLI %v failed: %d %q %q", args, code, out, stderr)
		}
		assertNoCanaries("CLI output", []byte(out+stderr))
	}
	items, err := engine.ReadDetail(context.Background(), cfg, "issues:1")
	if err != nil || len(items) != 1 {
		t.Fatalf("OAuth source read: %d items: %v", len(items), err)
	}
	if items[0].Title != "[REDACTED]" || items[0].Description != "[REDACTED]" {
		t.Fatalf("provider canary escaped remote boundary: %+v", items[0])
	}

	log := hooks.New(paths.StateDir, cfg.Project.ID)
	event := hooks.Event{Type: "item.created", Project: cfg.Project.ID, Source: "issues", Ref: "issues:1", Item: items[0], Origin: "cli", Time: time.Now().UTC()}
	if err = log.Update(context.Background(), func(tx *hooks.Transaction) error { _, err := tx.Append(event); return err }); err != nil {
		t.Fatal(err)
	}
	if err = hooks.RunEvent(context.Background(), cfg, event, root); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := run("-C", root, "--json", "action", "capture", "issues:1", "--yes")
	if code != 0 || stderr != "" {
		t.Fatalf("public CLI action failed: %d %q %q", code, out, stderr)
	}
	assertNoCanaries("CLI action output", []byte(out+stderr))
	for _, path := range []string{hookCapture, actionCapture} {
		captured, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("automation probe did not run (%s): %v", path, readErr)
		}
		assertNoCanaries("hook/action argv, environment, and stdin", captured)
	}
	events, err := log.Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	eventBytes, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCanaries("event log", eventBytes)
	code, out, stderr = run("-C", root, "--json", "events")
	if code != 0 || stderr != "" {
		t.Fatalf("CLI events failed: %d %q %q", code, out, stderr)
	}
	assertNoCanaries("CLI events output", []byte(out+stderr))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	mcpServer := mcp.New(cfg)
	go func() { _ = mcpServer.Run(ctx, serverTransport) }()
	client := sdk.NewClient(&sdk.Implementation{Name: "oauth-canary", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	mcpResult, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "show", Arguments: map[string]any{"id": "issues:1"}})
	if err != nil || mcpResult.IsError {
		t.Fatalf("MCP OAuth read: %#v %v", mcpResult, err)
	}
	mcpBytes, err := json.Marshal(mcpResult)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCanaries("MCP response", mcpBytes)
	_ = session.Close()

	cacheBytes := []byte{}
	if err = filepath.WalkDir(paths.CacheDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		cacheBytes = append(cacheBytes, data...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertNoCanaries("disposable cache", cacheBytes)
	fmt.Println("OAUTH_CANARY_INTEGRATION_COMPLETE")
}

func TestOAuthSourceReportsStructuredProviderDiagnostic(t *testing.T) {
	testkit.Isolate(t)
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "accounts:\n  github:\n    - name: work\n      host: github.com\n      user: alice\n      oauth: true\nsources:\n  - id: issues\n    type: github\n    repo: owner/repo\n    account: work\n"
	if err := os.WriteFile(paths.ConfigFile, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	code, out, stderr := run("-C", root, "--json", "ls")
	var envelope cli.Envelope
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || code != 6 || stderr != "" || envelope.Error == nil || envelope.Error.Reason != "authentication-required" || envelope.Error.Details["code"] != "authentication-required" {
		t.Fatalf("OAuth source diagnostic: %d %q %q: %v", code, out, stderr, err)
	}
	if strings.Contains(out, "access_token") || strings.Contains(out, "refresh_token") {
		t.Fatalf("OAuth diagnostic leaked token data: %s", out)
	}
}
