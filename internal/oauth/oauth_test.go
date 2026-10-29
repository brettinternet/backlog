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
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
)

type fakeAuthServer struct {
	mu             sync.Mutex
	server         *httptest.Server
	principals     map[string]Principal
	refreshTokens  map[string][]string
	authorized     [][]string
	refreshCalls   int
	verifyCalls    int
	discoveryCalls int
	revokeCalls    int
	grantScopes    []string
	wrongLogin     bool
	refreshCode    string
	verifyFailure  bool
	discoveryFail  bool
	refreshStarted chan struct{}
	refreshGate    <-chan struct{}
}

type wireTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func newFakeAuthServer(t *testing.T) *fakeAuthServer {
	t.Helper()
	fixture := &fakeAuthServer{
		principals: map[string]Principal{}, refreshTokens: map[string][]string{},
		refreshStarted: make(chan struct{}, 8),
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *fakeAuthServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/authorize":
		var request struct {
			Scopes []string `json:"scopes"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.authorized = append(f.authorized, append([]string(nil), request.Scopes...))
		scopes := append([]string(nil), request.Scopes...)
		if f.grantScopes != nil {
			scopes = append([]string(nil), f.grantScopes...)
		}
		response := f.issueLocked(scopes, fmt.Sprintf("login-%d", len(f.authorized)))
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(response)
	case "/refresh":
		var request struct {
			RefreshToken string `json:"refresh_token"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.refreshCalls++
		if f.refreshCode != "" {
			code := f.refreshCode
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
			return
		}
		scopes, ok := f.refreshTokens[request.RefreshToken]
		if f.grantScopes != nil {
			scopes = append([]string(nil), f.grantScopes...)
		}
		call := f.refreshCalls
		f.mu.Unlock()
		select {
		case f.refreshStarted <- struct{}{}:
		default:
		}
		if f.refreshGate != nil {
			select {
			case <-f.refreshGate:
			case <-r.Context().Done():
				return
			}
		}
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": CodeGrantRevoked})
			return
		}
		f.mu.Lock()
		response := f.issueLocked(scopes, fmt.Sprintf("refresh-%d", call))
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(response)
	case "/verify":
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		f.verifyCalls++
		principal, ok := f.principals[token]
		verifyFailure := f.verifyFailure
		if ok && f.wrongLogin {
			principal.Login = "mallory"
		}
		f.mu.Unlock()
		if verifyFailure {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(principal)
	case "/discover":
		f.mu.Lock()
		f.discoveryCalls++
		discoveryFail := f.discoveryFail
		f.mu.Unlock()
		if discoveryFail {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "/revoke":
		f.mu.Lock()
		f.revokeCalls++
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]bool{"revoked": true})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAuthServer) issueLocked(scopes []string, suffix string) wireTokens {
	access, refresh := "oauth-canary-access-"+suffix, "oauth-canary-refresh-"+suffix
	f.principals[access] = Principal{ID: "immutable-user-42", Login: "alice", Scopes: append([]string(nil), scopes...)}
	f.refreshTokens[refresh] = append([]string(nil), scopes...)
	return wireTokens{AccessToken: access, RefreshToken: refresh, ExpiresAt: time.Now().Add(time.Hour)}
}

func (f *fakeAuthServer) expire(t *testing.T, store Store, account Account) {
	t.Helper()
	err := store.withRecord(context.Background(), tokenKey(account), func(record *storedRecord, _ func(*storedRecord) error) (*storedRecord, bool, error) {
		if record == nil {
			return nil, false, errors.New("missing token fixture")
		}
		record.ExpiresAt = time.Now().Add(-time.Minute)
		return record, true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type fakeProvider struct {
	server   string
	noRevoke bool
}

func (p fakeProvider) ID() string                 { return "fake" }
func (fakeProvider) ReadScopes(Account) []string  { return []string{"read"} }
func (fakeProvider) WriteScopes(Account) []string { return []string{"write"} }
func (p fakeProvider) Authorize(ctx context.Context, _ Account, scopes []string, _ *Tokens, interaction Interaction) (Tokens, error) {
	if _, err := fmt.Fprintln(interaction.Out, "consent requested"); err != nil {
		return Tokens{}, Failure(CodeProviderDown, time.Time{})
	}
	var response wireTokens
	if err := p.post(ctx, "/authorize", map[string]any{"scopes": scopes}, &response); err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, ExpiresAt: response.ExpiresAt}, nil
}
func (p fakeProvider) Refresh(ctx context.Context, _ Account, tokens Tokens) (Tokens, error) {
	var response wireTokens
	if err := p.post(ctx, "/refresh", map[string]string{"refresh_token": tokens.RefreshToken}, &response); err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, ExpiresAt: response.ExpiresAt}, nil
}
func (p fakeProvider) Verify(ctx context.Context, _ Account, token string) (Principal, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.server+"/verify", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Principal{}, Failure(CodeProviderDown, time.Time{})
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized {
			return Principal{}, Failure(CodeGrantRevoked, time.Time{})
		}
		return Principal{}, Failure(CodeProviderDown, time.Time{})
	}
	var principal Principal
	if err = json.NewDecoder(response.Body).Decode(&principal); err != nil {
		return Principal{}, Failure(CodeProviderDown, time.Time{})
	}
	return principal, nil
}
func (p fakeProvider) Rediscover(ctx context.Context, _ Account, token string) error {
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.server+"/discover", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Failure(CodeProviderDown, time.Time{})
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return Failure(CodeProviderDown, time.Time{})
	}
	return nil
}
func (p fakeProvider) Revoke(ctx context.Context, _ Account, tokens Tokens) (bool, error) {
	if p.noRevoke {
		return false, nil
	}
	var response any
	if err := p.post(ctx, "/revoke", map[string]string{"token": tokens.AccessToken}, &response); err != nil {
		return true, err
	}
	return true, nil
}
func (p fakeProvider) post(ctx context.Context, path string, body, output any) error {
	data, _ := json.Marshal(body)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.server+path, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Failure(CodeProviderDown, time.Time{})
	}
	defer response.Body.Close()
	var raw map[string]any
	if err = json.NewDecoder(response.Body).Decode(&raw); err != nil {
		return Failure(CodeProviderDown, time.Time{})
	}
	if code, ok := raw["error"].(string); ok {
		return Failure(code, time.Now().Add(time.Minute))
	}
	encoded, _ := json.Marshal(raw)
	if err = json.Unmarshal(encoded, output); err != nil {
		return Failure(CodeProviderDown, time.Time{})
	}
	return nil
}

type testSecrets struct {
	mu     sync.Mutex
	values []string
}

func (s *testSecrets) Register(value string) {
	s.mu.Lock()
	s.values = append(s.values, value)
	s.mu.Unlock()
}
func (s *testSecrets) Redact(value string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, secret := range s.values {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func fixtureManager(t *testing.T, fixture *fakeAuthServer, state, cacheRoot string) (*Manager, Account, *testSecrets) {
	t.Helper()
	registry, err := NewRegistry(fakeProvider{server: fixture.server.URL})
	if err != nil {
		t.Fatal(err)
	}
	secrets := &testSecrets{}
	manager := NewManager(Store{StateDir: state}, cacheRoot, registry, secrets)
	account := Account{Provider: "fake", Name: "work", Host: "example.test", User: "alice", OptIn: true}
	return manager, account, secrets
}

func TestOAuthLifecycleScopesProtectedStorageAndCachePurge(t *testing.T) {
	fixture := newFakeAuthServer(t)
	state, cacheRoot := filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "cache")
	manager, account, secrets := fixtureManager(t, fixture, state, cacheRoot)
	var transcript strings.Builder
	interaction := Interaction{In: strings.NewReader("consent"), Out: &transcript}
	if err := manager.Login(context.Background(), account, interaction); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	if !reflect.DeepEqual(fixture.authorized, [][]string{{"read"}}) || fixture.discoveryCalls != 1 {
		fixture.mu.Unlock()
		t.Fatalf("read scopes/capability discovery: %v %d", fixture.authorized, fixture.discoveryCalls)
	}
	fixture.mu.Unlock()
	if strings.Contains(transcript.String(), "oauth-canary-") {
		t.Fatal("login wrote tokens to the interaction stream")
	}
	readTokens, err := manager.Resolve(context.Background(), account, func(_ context.Context, tokens Tokens) (string, error) {
		if !strings.Contains(tokens.AccessToken, "login-1") {
			return "", Failure(CodePrincipalMismatch, time.Time{})
		}
		return "alice", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", readTokens, readTokens), readTokens.AccessToken) {
		t.Fatal("formatted token leaked")
	}
	encoded, err := json.Marshal(readTokens)
	if err != nil || strings.Contains(string(encoded), "oauth-canary-") {
		t.Fatalf("token JSON leaked: %s %v", encoded, err)
	}
	if err = manager.EnableWrites(context.Background(), account, interaction); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	if !reflect.DeepEqual(fixture.authorized, [][]string{{"read"}, {"read", "write"}}) || fixture.discoveryCalls != 3 {
		fixture.mu.Unlock()
		t.Fatalf("upgrade scopes/capability discovery: %v %d", fixture.authorized, fixture.discoveryCalls)
	}
	fixture.mu.Unlock()
	writeTokens, err := manager.Resolve(context.Background(), account, func(_ context.Context, tokens Tokens) (string, error) {
		if !strings.Contains(tokens.AccessToken, "login-2") {
			return "", fmt.Errorf("provider reflected %s", tokens.AccessToken)
		}
		return "alice", nil
	})
	if err != nil || !strings.Contains(secrets.Redact(writeTokens.AccessToken+" "+writeTokens.RefreshToken), "[REDACTED]") {
		t.Fatalf("token resolve/redaction: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(state, "oauth"))
	if err != nil {
		t.Fatal(err)
	}
	var tokenPath string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			tokenPath = filepath.Join(state, "oauth", entry.Name())
		}
	}
	if tokenPath == "" {
		t.Fatal("private token file missing")
	}
	fileInfo, err := os.Stat(tokenPath)
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode: %v %v", fileInfo, err)
	}
	dirInfo, err := os.Stat(filepath.Join(state, "oauth"))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("token directory mode: %v %v", dirInfo, err)
	}

	principal := PrincipalKey(account.Provider, account.Host, account.User)
	principalCache := cache.NewPrincipal(cacheRoot, "project-a", "source-a", "identity", principal)
	otherCache := cache.NewPrincipal(cacheRoot, "project-b", "source-b", "identity", "another-principal")
	for _, store := range []cache.Store{principalCache, otherCache} {
		if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.Path, []byte("cached row"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := manager.Logout(context.Background(), account)
	if err != nil || !result.RemoteRevoked || result.ManualRevocation {
		t.Fatalf("logout/revoke: %+v %v", result, err)
	}
	fixture.mu.Lock()
	revokes := fixture.revokeCalls
	fixture.mu.Unlock()
	if revokes != 1 {
		t.Fatalf("remote revocations=%d", revokes)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("token file remained: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(principalCache.Path)); !os.IsNotExist(err) {
		t.Fatalf("principal cache remained: %v", err)
	}
	if _, err := os.Stat(otherCache.Path); err != nil {
		t.Fatalf("other principal cache was removed: %v", err)
	}
}

func TestOAuthErrorsAndPrincipalVerification(t *testing.T) {
	cases := []struct {
		name, code string
		wrongLogin bool
	}{
		{"revoked", CodeGrantRevoked, false},
		{"scope", CodeInsufficientScope, false},
		{"sso", CodeSSORequired, false},
		{"rate", CodeRateLimited, false},
		{"provider-down", CodeProviderDown, false},
		{"principal-mismatch", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFakeAuthServer(t)
			manager, account, _ := fixtureManager(t, fixture, filepath.Join(t.TempDir(), "state"), "")
			interaction := Interaction{In: strings.NewReader("ok"), Out: io.Discard}
			if err := manager.Login(context.Background(), account, interaction); err != nil {
				t.Fatal(err)
			}
			fixture.expire(t, manager.store, account)
			fixture.mu.Lock()
			fixture.refreshCode, fixture.wrongLogin = tc.code, tc.wrongLogin
			fixture.mu.Unlock()
			_, err := manager.Resolve(context.Background(), account, func(context.Context, Tokens) (string, error) { return "alice", nil })
			var diagnostic *Diagnostic
			want := tc.code
			if tc.wrongLogin {
				want = CodePrincipalMismatch
			}
			if !errors.As(err, &diagnostic) || diagnostic.Code != want {
				t.Fatalf("diagnostic %v, want %s", err, want)
			}
			if strings.Contains(err.Error(), "oauth-canary-") {
				t.Fatalf("diagnostic leaked a token: %v", err)
			}
		})
	}

	t.Run("already expired", func(t *testing.T) {
		fixture := newFakeAuthServer(t)
		manager, account, _ := fixtureManager(t, fixture, filepath.Join(t.TempDir(), "state"), "")
		if err := manager.Login(context.Background(), account, Interaction{In: strings.NewReader("ok"), Out: io.Discard}); err != nil {
			t.Fatal(err)
		}
		if err := manager.store.withRecord(context.Background(), tokenKey(account), func(record *storedRecord, _ func(*storedRecord) error) (*storedRecord, bool, error) {
			record.ExpiresAt, record.RefreshToken = time.Now().Add(-time.Minute), ""
			return record, true, nil
		}); err != nil {
			t.Fatal(err)
		}
		_, err := manager.Resolve(context.Background(), account, func(context.Context, Tokens) (string, error) { return "alice", nil })
		var diagnostic *Diagnostic
		if !errors.As(err, &diagnostic) || diagnostic.Code != CodeTokenExpired {
			t.Fatalf("expired token: %v", err)
		}
	})

	t.Run("unattended, scope denied and non-opt-in", func(t *testing.T) {
		fixture := newFakeAuthServer(t)
		manager, account, _ := fixtureManager(t, fixture, filepath.Join(t.TempDir(), "state"), "")
		var diagnostic *Diagnostic
		if err := manager.Login(context.Background(), account, Interaction{}); !errors.As(err, &diagnostic) || diagnostic.Code != CodeInteractionRequired {
			t.Fatalf("unattended login: %v", err)
		}
		fixture.grantScopes = []string{}
		if err := manager.Login(context.Background(), account, Interaction{In: strings.NewReader("ok"), Out: io.Discard}); !errors.As(err, &diagnostic) || diagnostic.Code != CodeInsufficientScope {
			t.Fatalf("scope denial: %v", err)
		}
		account.OptIn = false
		if err := manager.Login(context.Background(), account, Interaction{In: strings.NewReader("ok"), Out: io.Discard}); !errors.As(err, &diagnostic) || diagnostic.Code != CodeNotOptedIn {
			t.Fatalf("non-opt-in: %v", err)
		}
	})
}

func TestOAuthRotatedPairRemainsPendingUntilEveryCheckPasses(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(*fakeAuthServer)
		fix  func(*fakeAuthServer, *storedRecord)
		want string
	}{
		{
			name: "principal mismatch",
			fail: func(f *fakeAuthServer) { f.wrongLogin = true },
			fix:  func(f *fakeAuthServer, _ *storedRecord) { f.wrongLogin = false },
			want: CodePrincipalMismatch,
		},
		{
			name: "insufficient scopes",
			fail: func(f *fakeAuthServer) { f.grantScopes = []string{} },
			fix: func(f *fakeAuthServer, record *storedRecord) {
				f.grantScopes = nil
				principal := f.principals[record.AccessToken]
				principal.Scopes = []string{"read"}
				f.principals[record.AccessToken] = principal
			},
			want: CodeInsufficientScope,
		},
		{
			name: "verification failure",
			fail: func(f *fakeAuthServer) { f.verifyFailure = true },
			fix:  func(f *fakeAuthServer, _ *storedRecord) { f.verifyFailure = false },
			want: CodeProviderDown,
		},
		{
			name: "capability rediscovery failure",
			fail: func(f *fakeAuthServer) { f.discoveryFail = true },
			fix:  func(f *fakeAuthServer, _ *storedRecord) { f.discoveryFail = false },
			want: CodeProviderDown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFakeAuthServer(t)
			state := filepath.Join(t.TempDir(), "state")
			manager, account, _ := fixtureManager(t, fixture, state, "")
			if err := manager.Login(context.Background(), account, Interaction{In: strings.NewReader("ok"), Out: io.Discard}); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Resolve(context.Background(), account, func(context.Context, Tokens) (string, error) { return "alice", nil }); err != nil {
				t.Fatal(err)
			}
			fixture.expire(t, manager.store, account)
			fixture.mu.Lock()
			tc.fail(fixture)
			fixture.mu.Unlock()

			_, err := manager.Resolve(context.Background(), account, func(context.Context, Tokens) (string, error) { return "alice", nil })
			var diagnostic *Diagnostic
			if !errors.As(err, &diagnostic) || diagnostic.Code != tc.want {
				t.Fatalf("first refresh validation: %v, want %s", err, tc.want)
			}
			rotated := readStoredRecord(t, manager.store, account)
			if !rotated.ValidationPending || rotated.PrincipalID != "immutable-user-42" || !reflect.DeepEqual(rotated.Scopes, []string{"read"}) || !strings.Contains(rotated.RefreshToken, "refresh-") {
				t.Fatalf("failed validation changed accepted identity, scopes, or rotated pair: %s", &rotated)
			}

			// A fresh Manager models another process: the persisted pending bit
			// must prevent the unexpired rotated access token's fast path.
			retryManager, _, _ := fixtureManager(t, fixture, state, "")
			_, err = retryManager.Resolve(context.Background(), account, func(context.Context, Tokens) (string, error) { return "alice", nil })
			if !errors.As(err, &diagnostic) || diagnostic.Code != tc.want {
				t.Fatalf("new Manager bypassed pending validation: %v, want %s", err, tc.want)
			}
			fixture.mu.Lock()
			refreshCalls := fixture.refreshCalls
			tc.fix(fixture, &rotated)
			fixture.mu.Unlock()
			if refreshCalls != 1 {
				t.Fatalf("retry rotated again: refresh calls=%d", refreshCalls)
			}

			resolved, err := retryManager.Resolve(context.Background(), account, func(context.Context, Tokens) (string, error) { return "alice", nil })
			if err != nil {
				t.Fatalf("valid retry: %v", err)
			}
			if resolved.AccessToken != rotated.AccessToken || resolved.RefreshToken != rotated.RefreshToken {
				t.Fatalf("valid retry lost rotated pair: got %s, stored %s", resolved, &rotated)
			}
			stored := readStoredRecord(t, retryManager.store, account)
			if stored.ValidationPending || stored.AccessToken != rotated.AccessToken || stored.RefreshToken != rotated.RefreshToken || stored.PrincipalID != "immutable-user-42" || !reflect.DeepEqual(stored.Scopes, []string{"read"}) {
				t.Fatalf("successful retry did not validate retained pair: %s", &stored)
			}
			fixture.mu.Lock()
			refreshCalls = fixture.refreshCalls
			fixture.mu.Unlock()
			if refreshCalls != 1 {
				t.Fatalf("valid retry rotated again: refresh calls=%d", refreshCalls)
			}
		})
	}
}

func TestOAuthPendingSourcePrincipalFailureCanRecoverWithoutAnotherRefresh(t *testing.T) {
	fixture := newFakeAuthServer(t)
	state := filepath.Join(t.TempDir(), "state")
	manager, account, _ := fixtureManager(t, fixture, state, "")
	if err := manager.Login(context.Background(), account, Interaction{In: strings.NewReader("ok"), Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(context.Background(), account, func(context.Context, Tokens) (string, error) { return "alice", nil }); err != nil {
		t.Fatal(err)
	}
	fixture.expire(t, manager.store, account)
	_, err := manager.Resolve(context.Background(), account, func(context.Context, Tokens) (string, error) { return "mallory", nil })
	var diagnostic *Diagnostic
	if !errors.As(err, &diagnostic) || diagnostic.Code != CodePrincipalMismatch {
		t.Fatalf("wrong source principal: %v", err)
	}
	rotated := readStoredRecord(t, manager.store, account)
	if !rotated.ValidationPending {
		t.Fatal("source-principal failure cleared durable pending validation")
	}
	retryManager, _, _ := fixtureManager(t, fixture, state, "")
	if _, err = retryManager.Resolve(context.Background(), account, func(context.Context, Tokens) (string, error) { return "alice", nil }); err != nil {
		t.Fatalf("correct principal could not recover pending pair: %v", err)
	}
	stored := readStoredRecord(t, retryManager.store, account)
	if stored.ValidationPending || stored.AccessToken != rotated.AccessToken || stored.RefreshToken != rotated.RefreshToken {
		t.Fatalf("valid retry did not retain pair: %s", &stored)
	}
	fixture.mu.Lock()
	refreshCalls := fixture.refreshCalls
	fixture.mu.Unlock()
	if refreshCalls != 1 {
		t.Fatalf("retry rotated again: refresh calls=%d", refreshCalls)
	}
}

func readStoredRecord(t *testing.T, store Store, account Account) storedRecord {
	t.Helper()
	var result storedRecord
	if err := store.withRecord(context.Background(), tokenKey(account), func(current *storedRecord, _ func(*storedRecord) error) (*storedRecord, bool, error) {
		if current == nil {
			return nil, false, errors.New("missing OAuth token record")
		}
		result = *current
		return current, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOAuthLogoutRemainsAvailableAfterOptOut(t *testing.T) {
	fixture := newFakeAuthServer(t)
	state := filepath.Join(t.TempDir(), "state")
	manager, account, _ := fixtureManager(t, fixture, state, "")
	if err := manager.Login(context.Background(), account, Interaction{In: strings.NewReader("ok"), Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	account.OptIn, account.HasLegacyCredential = false, true
	result, err := manager.Logout(context.Background(), account)
	if err != nil || !result.RemoteRevoked {
		t.Fatalf("logout after switching credential modes: %+v %v", result, err)
	}
	for _, entry := range mustReadDir(t, filepath.Join(state, "oauth")) {
		if strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("local tokens survived logout: %s", entry.Name())
		}
	}
}

func TestOAuthLogoutWithoutRevokeStillPurges(t *testing.T) {
	fixture := newFakeAuthServer(t)
	state, cacheRoot := filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "cache")
	manager, account, _ := fixtureManager(t, fixture, state, cacheRoot)
	if err := manager.Login(context.Background(), account, Interaction{In: strings.NewReader("ok"), Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	manager.providers["fake"] = fakeProvider{server: fixture.server.URL, noRevoke: true}
	store := cache.NewPrincipal(cacheRoot, "project", "source", "identity", PrincipalKey(account.Provider, account.Host, account.User))
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path, []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Logout(context.Background(), account)
	if err != nil || result.RemoteRevoked || !result.ManualRevocation {
		t.Fatalf("manual logout: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Dir(store.Path)); !os.IsNotExist(err) {
		t.Fatalf("cache survived: %v", err)
	}
	for _, entry := range mustReadDir(t, filepath.Join(state, "oauth")) {
		if strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("local tokens survived logout: %s", entry.Name())
		}
	}
}

func mustReadDir(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestOAuthCrossProcessRefreshRotationSerialized(t *testing.T) {
	if os.Getenv("BL_OAUTH_CHILD") == "1" {
		state, server := os.Getenv("BL_OAUTH_STATE"), os.Getenv("BL_OAUTH_SERVER")
		if err := os.WriteFile(os.Getenv("BL_OAUTH_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForFile(t, os.Getenv("BL_OAUTH_START"))
		registry, err := NewRegistry(fakeProvider{server: server})
		if err != nil {
			t.Fatal(err)
		}
		manager := NewManager(Store{StateDir: state}, "", registry, nil)
		account := Account{Provider: "fake", Name: "work", Host: "example.test", User: "alice", OptIn: true}
		_, err = manager.Resolve(context.Background(), account, func(_ context.Context, tokens Tokens) (string, error) {
			if !strings.HasPrefix(tokens.AccessToken, "oauth-canary-access-refresh-") {
				return "", Failure(CodePrincipalMismatch, time.Time{})
			}
			return "alice", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "resolved")
		return
	}

	fixture := newFakeAuthServer(t)
	gate := make(chan struct{})
	fixture.refreshGate = gate
	state := filepath.Join(t.TempDir(), "state")
	manager, account, _ := fixtureManager(t, fixture, state, "")
	if err := manager.Login(context.Background(), account, Interaction{In: strings.NewReader("ok"), Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	fixture.expire(t, manager.store, account)
	start := filepath.Join(t.TempDir(), "start")
	ready := []string{start + "-one", start + "-two"}
	commands := make([]*exec.Cmd, 2)
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestOAuthCrossProcessRefreshRotationSerialized$")
		commands[i].Stdout, commands[i].Stderr = &bytes.Buffer{}, &bytes.Buffer{}
		commands[i].Env = append(os.Environ(), "BL_OAUTH_CHILD=1", "BL_OAUTH_STATE="+state, "BL_OAUTH_SERVER="+fixture.server.URL, "BL_OAUTH_READY="+ready[i], "BL_OAUTH_START="+start)
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range ready {
		waitForFile(t, path)
	}
	if err := os.WriteFile(start, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fixture.refreshStarted:
	case <-time.After(5 * time.Second):
		close(gate)
		t.Fatal("no refresh reached fake server")
	}
	close(gate)
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("refresh process %d: %v", i, err)
		}
		stdout := command.Stdout.(*bytes.Buffer).String()
		stderr := command.Stderr.(*bytes.Buffer).String()
		if strings.Contains(stdout+stderr, "oauth-canary-") {
			t.Fatalf("process output leaked token: %s%s", stdout, stderr)
		}
	}
	fixture.mu.Lock()
	calls, discoveries := fixture.refreshCalls, fixture.discoveryCalls
	fixture.mu.Unlock()
	if calls != 1 {
		t.Fatalf("refresh requests=%d, want 1", calls)
	}
	if discoveries != 2 {
		t.Fatalf("capability rediscoveries=%d, want login and refresh", discoveries)
	}
	var record *storedRecord
	if err := manager.store.withRecord(context.Background(), tokenKey(account), func(current *storedRecord, _ func(*storedRecord) error) (*storedRecord, bool, error) {
		record = current
		return current, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if record == nil || !strings.HasPrefix(record.AccessToken, "oauth-canary-access-refresh-") || !strings.HasPrefix(record.RefreshToken, "oauth-canary-refresh-") {
		t.Fatalf("rotated pair was not retained: %s", record)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	ticker, timeout := time.NewTicker(5*time.Millisecond), time.NewTimer(5*time.Second)
	defer ticker.Stop()
	defer timeout.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("timed out waiting for %s", filepath.Base(path))
		}
	}
}

func TestOAuthStatusReportsWhenLoginIsNeeded(t *testing.T) {
	fixture := newFakeAuthServer(t)
	state := filepath.Join(t.TempDir(), "state")
	manager, account, _ := fixtureManager(t, fixture, state, "")
	if got := manager.Status(account); got.Problem == nil || got.Problem.Code != CodeAuthenticationRequired {
		t.Fatalf("before login: %+v", got)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created the store: %v", err)
	}
	interaction := Interaction{In: strings.NewReader("consent"), Out: io.Discard}
	if err := manager.Login(context.Background(), account, interaction); err != nil {
		t.Fatal(err)
	}
	if got := manager.Status(account); got.Problem != nil || got.Writes || !got.Expires.IsZero() {
		t.Fatalf("read grant: %+v", got)
	}
	if err := manager.EnableWrites(context.Background(), account, interaction); err != nil {
		t.Fatal(err)
	}
	if got := manager.Status(account); got.Problem != nil || !got.Writes {
		t.Fatalf("write grant: %+v", got)
	}
	err := manager.store.withRecord(context.Background(), tokenKey(account), func(record *storedRecord, _ func(*storedRecord) error) (*storedRecord, bool, error) {
		record.RefreshToken, record.ExpiresAt = "", time.Now().Add(-time.Minute)
		return record, true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Status(account); got.Problem == nil || got.Problem.Code != CodeTokenExpired {
		t.Fatalf("expired without refresh: %+v", got)
	}
	other := account
	other.User = "mallory"
	if got := manager.Status(other); got.Problem == nil || got.Problem.Code != CodePrincipalMismatch {
		t.Fatalf("changed configured user: %+v", got)
	}
}
