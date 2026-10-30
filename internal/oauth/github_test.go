package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type githubAuthFixture struct {
	mu                     sync.Mutex
	slug, errorCode, login string
	polls, refreshes       int
	grantWriteToRead       bool
	uninstalled            bool
}

func githubTestProvider(t *testing.T) (*githubProvider, *githubAuthFixture) {
	t.Helper()
	f := &githubAuthFixture{login: "alice"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		emit := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/device":
			_ = r.ParseForm()
			if r.Form.Has("client_secret") || r.Form.Has("scope") {
				t.Error("device flow must not request classic scopes or secrets")
			}
			f.slug = GitHubReadApp
			if r.Form.Get("client_id") == githubWriteClientID {
				f.slug = GitHubWriteApp
			} else if r.Form.Get("client_id") != githubReadClientID {
				t.Error("unknown public app")
			}
			f.polls = 0
			emit(map[string]any{"device_code": "github-device-canary", "user_code": "ABCD-1234", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5})
		case "/token":
			_ = r.ParseForm()
			if r.Form.Has("client_secret") || r.Header.Get("Authorization") != "" || r.Form.Has("scope") {
				t.Error("secret or scopes sent to exchange")
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				f.refreshes++
				if r.Form.Get("refresh_token") != "github-refresh-canary-0" {
					t.Error("refresh rotation replayed consumed token")
				}
				expected := githubReadClientID
				if f.slug == GitHubWriteApp {
					expected = githubWriteClientID
				}
				if r.Form.Get("client_id") != expected {
					t.Error("refresh used wrong app")
				}
			} else {
				if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.Form.Get("device_code") != "github-device-canary" {
					t.Error("wrong device grant")
				}
				f.polls++
				if f.errorCode != "" {
					emit(map[string]any{"error": f.errorCode})
					return
				}
				if f.polls == 1 {
					emit(map[string]any{"error": "authorization_pending"})
					return
				}
				if f.polls == 2 {
					emit(map[string]any{"error": "slow_down", "interval": 10})
					return
				}
			}
			emit(map[string]any{"access_token": fmt.Sprintf("github-access-canary-%d", f.refreshes), "refresh_token": fmt.Sprintf("github-refresh-canary-%d", f.refreshes), "expires_in": 3600, "token_type": "bearer", "scope": ""})
		case "/user":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer github-access-canary-") {
				t.Error("missing user token")
			}
			emit(map[string]any{"id": 42, "login": f.login})
		case "/user/installations":
			if f.uninstalled {
				emit(map[string]any{"total_count": 0, "installations": []any{}})
				return
			}
			permission := "read"
			if f.slug == GitHubWriteApp || f.grantWriteToRead {
				permission = "write"
			}
			emit(map[string]any{"total_count": 1, "installations": []any{map[string]any{"id": 7, "app_slug": f.slug, "account": map[string]string{"login": "owner", "type": "Organization"}, "permissions": map[string]string{"issues": permission, "organization_projects": permission}}}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	p := newGitHubProvider()
	p.client = server.Client()
	p.deviceURL = server.URL + "/device"
	p.tokenURL = server.URL + "/token"
	p.apiURL = server.URL
	p.wait = func(context.Context, time.Duration) error { return nil }
	return p, f
}

// Plain login must use the provider-read-only app, upgrade must use the write
// app, and concurrent restarted managers must persist one rotated token pair.
func TestGitHubDeviceLoginUpgradeAndRotation(t *testing.T) {
	t.Parallel()
	p, f := githubTestProvider(t)
	var delays []time.Duration
	p.wait = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	ctx := context.Background()
	account := Account{Provider: "github", Name: "work", Host: "github.com", User: "alice", OptIn: true}
	store := Store{StateDir: filepath.Join(t.TempDir(), "state")}
	manager := NewManager(store, "", Registry{"github": p}, nil)
	var output bytes.Buffer
	interaction := Interaction{In: strings.NewReader(""), Out: &output}
	if err := manager.Login(ctx, account, interaction); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(delays, []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second}) {
		t.Fatalf("poll intervals %v", delays)
	}
	if strings.Contains(output.String(), "github-device-canary") || strings.Contains(output.String(), "github-access-canary") || strings.Contains(output.String(), "github-refresh-canary") {
		t.Fatal("token leaked to interactive output")
	}
	if !strings.Contains(output.String(), GitHubReadApp) {
		t.Fatal("missing read app installation guidance")
	}
	tokens, err := manager.Resolve(ctx, account, func(context.Context, Tokens) (string, error) { return "alice", nil })
	if err != nil || hasScopes(tokens.GrantedScopes, []string{"write"}) {
		t.Fatalf("read consent %v", err)
	}
	if err = manager.EnableWrites(ctx, account, interaction); err != nil {
		t.Fatal(err)
	}
	p.wait = func(context.Context, time.Duration) error { return nil }
	if err = store.withRecord(ctx, tokenKey(account), func(r *storedRecord, _ func(*storedRecord) error) (*storedRecord, bool, error) {
		r.ExpiresAt = time.Now().Add(-time.Minute)
		return r, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			copy := *p
			m := NewManager(store, "", Registry{"github": &copy}, nil)
			got, err := m.Resolve(ctx, account, func(context.Context, Tokens) (string, error) { return "alice", nil })
			if err != nil {
				t.Error(err)
				return
			}
			if got.RefreshToken != "github-refresh-canary-1" || !hasScopes(got.GrantedScopes, []string{"read", "write"}) {
				t.Error("rotation lost app consent or token")
			}
		})
	}
	wg.Wait()
	f.mu.Lock()
	refreshes := f.refreshes
	f.mu.Unlock()
	if refreshes != 1 {
		t.Fatalf("refresh calls %d", refreshes)
	}
	result, err := manager.Logout(ctx, account)
	if err != nil || !result.ManualRevocation || result.RemoteRevoked {
		t.Fatalf("logout %+v %v", result, err)
	}
	if _, err = manager.Resolve(ctx, account, func(context.Context, Tokens) (string, error) { return "alice", nil }); err == nil {
		t.Fatal("logout retained credentials")
	}
}

// BL-147 was misled by an empty installation list reported as a missing
// --write: the diagnostic must survive sanitizing and name the App to install.
func TestGitHubLoginNamesMissingInstallation(t *testing.T) {
	t.Parallel()
	p, f := githubTestProvider(t)
	f.uninstalled = true
	account := Account{Provider: "github", Name: "work", Host: "github.com", User: "alice", OptIn: true}
	manager := NewManager(Store{StateDir: filepath.Join(t.TempDir(), "state")}, "", Registry{"github": p}, nil)
	err := manager.Login(context.Background(), account, Interaction{In: strings.NewReader(""), Out: io.Discard})
	var d *Diagnostic
	if !errors.As(err, &d) || d.Code != CodeAppNotInstalled || !strings.Contains(d.Message, "https://github.com/apps/bl-cli-read/installations/new") || strings.Contains(d.Message, "--write") {
		t.Fatalf("%v", err)
	}
}

// Provider denials, expiry and principal swaps cannot create a usable grant;
// changing the read app registration to write must fail closed.
func TestGitHubDeviceDenialAndPermissionBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, code, want string }{
		{"denied", "access_denied", CodeInsufficientScope}, {"expired", "expired_token", CodeTokenExpired}, {"revoked", "bad_refresh_token", CodeGrantRevoked}, {"disabled", "device_flow_disabled", CodeProviderDown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, f := githubTestProvider(t)
			f.errorCode = tc.code
			_, err := p.Authorize(context.Background(), Account{}, []string{"read"}, nil, Interaction{In: strings.NewReader(""), Out: io.Discard})
			if d, ok := err.(*Diagnostic); !ok || d.Code != tc.want {
				t.Fatalf("%v", err)
			}
		})
	}
	p, f := githubTestProvider(t)
	f.grantWriteToRead = true
	_, err := p.Authorize(context.Background(), Account{}, []string{"read"}, nil, Interaction{In: strings.NewReader(""), Out: io.Discard})
	if d, ok := err.(*Diagnostic); !ok || d.Code != CodeInsufficientScope {
		t.Fatalf("read app gained writes: %v", err)
	}
	f.mu.Lock()
	f.grantWriteToRead = false
	f.login = "mallory"
	f.mu.Unlock()
	_, err = p.Verify(context.Background(), Account{User: "alice"}, "github-access-canary-0")
	if d, ok := err.(*Diagnostic); !ok || d.Code != CodePrincipalMismatch {
		t.Fatalf("principal swap: %v", err)
	}
	if _, err = p.Authorize(context.Background(), Account{}, []string{"read"}, nil, Interaction{}); err == nil {
		t.Fatal("unattended login allowed")
	}
	if _, err = p.Authorize(context.Background(), Account{Host: "enterprise.example"}, []string{"read"}, nil, Interaction{}); err == nil {
		t.Fatal("unqualified Enterprise login allowed")
	}
}

// Omitted expiry is supported without inventing refresh; malformed token
// responses and classic scopes cannot turn an app grant into an unsafe token.
func TestGitHubTokenResponsesAndSafeFailures(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tokens, err := decodeGitHubTokens([]byte(`{"access_token":"canary","token_type":"bearer","scope":""}`), now, []string{"read"})
	if err != nil || !tokens.ExpiresAt.IsZero() || tokens.RefreshToken != "" {
		t.Fatalf("non-expiring token %v", err)
	}
	for _, raw := range []string{`{}`, `{"access_token":"canary","token_type":"bearer","scope":"repo"}`, `{"access_token":"canary","token_type":"bearer","expires_in":3600}`, `{"access_token":"canary\n","token_type":"bearer"}`} {
		if _, err := decodeGitHubTokens([]byte(raw), now, []string{"read"}); err == nil {
			t.Fatal("unsafe response accepted")
		}
	}
	for _, tc := range []struct {
		status     int
		body, code string
	}{
		{401, `{"message":"github-access-canary"}`, CodeGrantRevoked}, {403, `{"message":"SAML github-access-canary"}`, CodeSSORequired}, {403, `{}`, CodeInsufficientScope}, {429, `{}`, CodeRateLimited}, {503, `{}`, CodeProviderDown},
	} {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 429 {
					w.Header().Set("Retry-After", "7")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			p := newGitHubProvider()
			p.client = server.Client()
			p.now = func() time.Time { return now }
			_, err := p.request(context.Background(), http.MethodPost, server.URL, url.Values{}.Encode(), "")
			d, ok := err.(*Diagnostic)
			if !ok || d.Code != tc.code || strings.Contains(err.Error(), "canary") {
				t.Fatalf("unsafe diagnostic %v", err)
			}
			if tc.code == CodeRateLimited && !d.RetryAt.Equal(now.Add(7*time.Second)) {
				t.Fatal("retry time lost")
			}
		})
	}
}

func FuzzGitHubOAuthJSON(f *testing.F) {
	f.Add([]byte(`{"access_token":"a","token_type":"bearer"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = GitHubInstallations(context.Background(), func(context.Context, string) ([]byte, error) { return raw, nil })
		tokens, err := decodeGitHubTokens(raw, time.Unix(0, 0), []string{"read"})
		if err == nil && (!validTokens(tokens) || tokens.AccessToken == "") {
			t.Fatal("invalid tokens accepted")
		}
	})
}
