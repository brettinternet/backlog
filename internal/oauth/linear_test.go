package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type linearConsent struct{ urls chan string }

func (w linearConsent) Write(b []byte) (int, error) {
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "https://linear.app/oauth/authorize?") {
			w.urls <- line
		}
	}
	return len(b), nil
}

type linearAuthFixture struct {
	mu                            sync.Mutex
	query                         url.Values
	scope                         string
	refreshes, exchanges, revokes int
	wrongPrincipal                bool
	forceScope                    string
}

func linearTestProvider(t *testing.T) (*linearProvider, *linearAuthFixture) {
	t.Helper()
	f := &linearAuthFixture{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Has("client_secret") || r.Header.Get("Authorization") != "" || r.Form.Get("client_id") != linearClientID {
				t.Error("exchange must use only public client ID")
			}
			if r.Form.Get("grant_type") == "authorization_code" {
				f.exchanges++
				hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if base64.RawURLEncoding.EncodeToString(hash[:]) != f.query.Get("code_challenge") || r.Form.Get("redirect_uri") != f.query.Get("redirect_uri") || r.Form.Get("code") != "test-code" {
					t.Error("PKCE/code/redirect binding lost")
				}
				f.scope = f.query.Get("scope")
				if f.forceScope != "" {
					f.scope = f.forceScope
				}
			} else if r.Form.Get("grant_type") == "refresh_token" {
				f.refreshes++
				if r.Form.Get("refresh_token") != "linear-canary-refresh-0" {
					t.Error("concurrent refresh used a rotated token")
				}
			} else {
				t.Error("unexpected grant")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("linear-canary-access-%d", f.refreshes), "refresh_token": fmt.Sprintf("linear-canary-refresh-%d", f.refreshes), "expires_in": 3600, "token_type": "Bearer", "scope": f.scope})
		case "/graphql":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer linear-canary-access-") {
				t.Error("viewer must use Bearer")
			}
			id := "viewer-42"
			if f.wrongPrincipal {
				id = "mallory"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"viewer": map[string]any{"id": id, "active": true}}})
		case "/revoke":
			_ = r.ParseForm()
			f.revokes++
			if r.Form.Get("token_type_hint") != "refresh_token" || r.Form.Get("token") != "linear-canary-refresh-1" || r.Form.Has("client_secret") {
				t.Error("revoke must use rotated refresh token in form")
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	p := newLinearProvider()
	p.client = server.Client()
	p.tokenURL = server.URL + "/token"
	p.graphURL = server.URL + "/graphql"
	p.revokeURL = server.URL + "/revoke"
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p.redirect = "http://" + listener.Addr().String() + "/oauth/callback"
	_ = listener.Close()
	return p, f
}
func completeLinearConsent(t *testing.T, ctx context.Context, p *linearProvider, f *linearAuthFixture, operation func(Interaction) error) {
	t.Helper()
	urls := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- operation(Interaction{In: strings.NewReader(""), Out: linearConsent{urls}}) }()
	var raw string
	select {
	case raw = <-urls:
	case err := <-done:
		t.Fatalf("login before consent: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("actor") != "user" || q.Get("prompt") != "consent" || q.Get("code_challenge_method") != "S256" || len(q.Get("state")) < 43 {
		t.Fatalf("unsafe authorization parameters: %v", q)
	}
	f.mu.Lock()
	f.query = q
	f.mu.Unlock()
	callback := p.redirect + "?code=test-code&state=" + url.QueryEscape(q.Get("state"))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, callback, nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("callback status %d", response.StatusCode)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// A restarted process must retain actual scope evidence with the rotated pair;
// concurrent resolves must not consume the old refresh token more than once.
func TestLinearPKCELifecycleAndConcurrentRotation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, f := linearTestProvider(t)
	store := Store{StateDir: filepath.Join(t.TempDir(), "state")}
	account := Account{Provider: "linear", Name: "work", User: "viewer-42", OptIn: true}
	manager := NewManager(store, "", Registry{"linear": p}, nil)
	completeLinearConsent(t, ctx, p, f, func(i Interaction) error { return manager.Login(ctx, account, i) })
	completeLinearConsent(t, ctx, p, f, func(i Interaction) error { return manager.EnableWrites(ctx, account, i) })
	err := store.withRecord(ctx, tokenKey(account), func(r *storedRecord, _ func(*storedRecord) error) (*storedRecord, bool, error) {
		r.ExpiresAt = time.Now().Add(-time.Minute)
		return r, true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			// Each manager has a fresh provider: no process-local scope map can help.
			copy := *p
			m := NewManager(store, "", Registry{"linear": &copy}, nil)
			tokens, err := m.Resolve(ctx, account, func(context.Context, Tokens) (string, error) { return account.User, nil })
			if err != nil {
				t.Error(err)
				return
			}
			if tokens.RefreshToken != "linear-canary-refresh-1" || !hasScopes(tokens.GrantedScopes, []string{"read", "write"}) {
				t.Error("rotated token/scope evidence lost")
			}
		})
	}
	wg.Wait()
	f.mu.Lock()
	refreshes := f.refreshes
	f.mu.Unlock()
	if refreshes != 1 {
		t.Fatalf("refresh calls=%d", refreshes)
	}
	result, err := manager.Logout(ctx, account)
	if err != nil || !result.RemoteRevoked {
		t.Fatalf("logout %+v %v", result, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revokes != 1 || f.exchanges != 2 {
		t.Fatalf("counts revoke=%d exchange=%d", f.revokes, f.exchanges)
	}
}

// Forged/ambiguous state cannot complete login or send an authorization code
// to the token endpoint. Valid denial must end the listener without a token.
func TestLinearPKCERejectsForgedCallbacksAndDenial(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, f := linearTestProvider(t)
	urls := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := p.Authorize(ctx, Account{}, []string{"read"}, nil, Interaction{Out: linearConsent{urls}, In: strings.NewReader("")})
		done <- err
	}()
	var raw string
	select {
	case raw = <-urls:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	u, _ := url.Parse(raw)
	state := u.Query().Get("state")
	for _, query := range []string{"state=forged&code=test-code", "state=" + state + "&state=forged&code=test-code", "state=" + state + "&code=a&code=b"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.redirect+"?"+query, nil)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("forged callback status %d", response.StatusCode)
		}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.redirect+"?state="+state+"&error=access_denied", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case err = <-done:
		var d *Diagnostic
		if !errors.As(err, &d) || d.Code != CodeInsufficientScope {
			t.Fatalf("denial %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exchanges != 0 {
		t.Fatal("forged callback exchanged code")
	}
}

func TestLinearLoginRejectsWrongPrincipalAndBroaderGrant(t *testing.T) {
	t.Parallel()
	for _, code := range []string{CodePrincipalMismatch, CodeInsufficientScope} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			p, f := linearTestProvider(t)
			if code == CodePrincipalMismatch {
				f.wrongPrincipal = true
			} else {
				f.forceScope = "read write"
			}
			account := Account{Provider: "linear", Name: "work", User: "viewer-42", OptIn: true}
			manager := NewManager(Store{StateDir: filepath.Join(t.TempDir(), "state")}, "", Registry{"linear": p}, nil)
			completeLinearConsent(t, ctx, p, f, func(i Interaction) error {
				err := manager.Login(ctx, account, i)
				var d *Diagnostic
				if !errors.As(err, &d) || d.Code != code {
					t.Errorf("login: got %v want %s", err, code)
				}
				return nil
			})
			_, err := manager.Resolve(ctx, account, func(context.Context, Tokens) (string, error) { return account.User, nil })
			var d *Diagnostic
			if !errors.As(err, &d) || d.Code != CodeAuthenticationRequired {
				t.Fatalf("failed consent persisted a grant: %v", err)
			}
		})
	}
}

func FuzzLinearTokenResponse(f *testing.F) {
	f.Add([]byte(`{"access_token":"canary","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer","scope":"read"}`))
	f.Add([]byte(`{"access_token":"canary","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer","scope":["read","write"]}`))
	f.Add([]byte(`{"access_token":"canary","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer","scope":"read,write"}`))
	f.Add([]byte(`{"error":"canary"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		tokens, err := decodeLinearTokens(raw, time.Unix(0, 0))
		if err != nil {
			var d *Diagnostic
			if !errors.As(err, &d) || strings.Contains(err.Error(), "canary") {
				t.Fatal("unsafe parse error")
			}
		} else if !validTokens(tokens) || tokens.AccessToken == "" || tokens.RefreshToken == "" || !hasScopes(tokens.GrantedScopes, []string{"read"}) {
			t.Fatal("invalid parsed grant")
		}
	})
}

func TestLinearTokenFailuresAreSafeAndDistinct(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
	}{
		{"revoked", 400, `{"error":"invalid_grant","detail":"linear-canary"}`, CodeGrantRevoked},
		{"scope", 403, `{"error":"invalid_scope"}`, CodeInsufficientScope},
		{"sso", 403, `{"error":"SAML authorization required"}`, CodeSSORequired},
		{"limit", 429, `linear-canary`, CodeRateLimited},
		{"down", 503, `linear-canary`, CodeProviderDown},
		{"redirect", 302, `linear-canary`, CodeProviderDown},
		{"missing scopes", 200, `{"access_token":"linear-canary","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer"}`, CodeInsufficientScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			p := newLinearProvider()
			p.client = server.Client()
			p.tokenURL = server.URL
			_, err := p.Refresh(context.Background(), Account{}, Tokens{RefreshToken: "canary-refresh"})
			var d *Diagnostic
			if !errors.As(err, &d) || d.Code != tc.code || strings.Contains(fmt.Sprint(err), "canary") {
				t.Fatalf("unsafe/wrong failure: %v", err)
			}
			if tc.code == CodeRateLimited && d.RetryAt.IsZero() {
				t.Fatal("rate limit lost retry time")
			}
		})
	}
}
