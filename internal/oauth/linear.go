package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Public OAuth application identifier, not a client secret.
const linearClientID = "6eaa52fb21a79f49ba564d31ee0bab91" // betterleaks:allow
const linearRedirect = "http://127.0.0.1:47653/oauth/callback"

// linearProvider uses only the owner-approved public application. Endpoints and
// callback are not configurable: user/project config cannot redirect credentials.
type linearProvider struct {
	client                                                *http.Client
	authorizeURL, tokenURL, graphURL, revokeURL, redirect string
}

func newLinearProvider() *linearProvider {
	return &linearProvider{client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, authorizeURL: "https://linear.app/oauth/authorize", tokenURL: "https://api.linear.app/oauth/token", graphURL: "https://api.linear.app/graphql", revokeURL: "https://api.linear.app/oauth/revoke", redirect: linearRedirect}
}
func (*linearProvider) ID() string                   { return "linear" }
func (*linearProvider) ReadScopes(Account) []string  { return []string{"read"} }
func (*linearProvider) WriteScopes(Account) []string { return []string{"write"} }
func linearAccount(a Account) error {
	if a.Host != "" && a.Host != "api.linear.app" {
		return Failure(CodeProviderUnavailable, time.Time{})
	}
	return nil
}
func (p *linearProvider) Authorize(ctx context.Context, a Account, scopes []string, _ *Tokens, interaction Interaction) (Tokens, error) {
	if err := linearAccount(a); err != nil {
		return Tokens{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	state := randomVerifier()
	verifier := randomVerifier()
	challenge := sha256.Sum256([]byte(verifier))
	redirect, _ := url.Parse(p.redirect)
	listener, err := net.Listen("tcp4", redirect.Host)
	if err != nil {
		return Tokens{}, Failure(CodeInteractionRequired, time.Time{})
	}
	defer listener.Close()
	type result struct {
		code string
		err  error
	}
	received := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(redirect.Path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		q, parseErr := url.ParseQuery(r.URL.RawQuery)
		if r.Method != http.MethodGet || r.Host != redirect.Host || parseErr != nil || len(q["state"]) != 1 || q.Get("state") != state {
			http.Error(w, "Invalid authorization callback", http.StatusBadRequest)
			return
		}
		value := result{code: q.Get("code")}
		if q.Get("error") != "" {
			value.err = Failure(CodeInsufficientScope, time.Time{})
		} else if len(q["code"]) != 1 || value.code == "" || len(value.code) > 8192 {
			http.Error(w, "Invalid authorization callback", http.StatusBadRequest)
			return
		}
		select {
		case received <- value:
			fmt.Fprintln(w, "Authorization received. Return to bl.")
		default:
			http.Error(w, "Authorization already received", http.StatusConflict)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	query := url.Values{"client_id": {linearClientID}, "redirect_uri": {p.redirect}, "response_type": {"code"}, "scope": {strings.Join(scopes, ",")}, "state": {state}, "actor": {"user"}, "prompt": {"consent"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	if _, err = fmt.Fprintln(interaction.Out, "Open this URL to authorize Linear:\n"+p.authorizeURL+"?"+query.Encode()); err != nil {
		return Tokens{}, Failure(CodeInteractionRequired, time.Time{})
	}
	select {
	case value := <-received:
		if value.err != nil {
			return Tokens{}, value.err
		}
		tokens, err := p.exchange(ctx, url.Values{"client_id": {linearClientID}, "grant_type": {"authorization_code"}, "redirect_uri": {p.redirect}, "code": {value.code}, "code_verifier": {verifier}}, scopes)
		// Previously authorized grants may be broader than this consent. Never
		// turn a read-only login into implicit write consent.
		if err == nil && !hasScopes(scopes, tokens.GrantedScopes) {
			return Tokens{}, Failure(CodeInsufficientScope, time.Time{})
		}
		return tokens, err
	case <-ctx.Done():
		return Tokens{}, Failure(CodeInteractionRequired, time.Time{})
	}
}
func randomVerifier() string { return rand.Text() + rand.Text() }

func (p *linearProvider) Refresh(ctx context.Context, a Account, t Tokens) (Tokens, error) {
	if err := linearAccount(a); err != nil {
		return Tokens{}, err
	}
	return p.exchange(ctx, url.Values{"client_id": {linearClientID}, "grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}}, t.GrantedScopes)
}

// exchange treats an omitted scope as the prior grant (RFC 6749 sections 5.1
// and 6): the requested scopes for a code, the stored grant for a refresh.
func (p *linearProvider) exchange(ctx context.Context, form url.Values, omitted []string) (Tokens, error) {
	raw, err := p.request(ctx, p.tokenURL, "application/x-www-form-urlencoded", form.Encode(), "")
	if err != nil {
		return Tokens{}, err
	}
	return decodeLinearTokens(raw, time.Now(), omitted)
}

func decodeLinearTokens(raw []byte, now time.Time, omitted []string) (Tokens, error) {
	var response struct {
		Access  string          `json:"access_token"`
		Refresh string          `json:"refresh_token"`
		Expires int64           `json:"expires_in"`
		Type    string          `json:"token_type"`
		Scope   json.RawMessage `json:"scope"`
	}
	if json.Unmarshal(raw, &response) != nil || !strings.EqualFold(response.Type, "Bearer") || response.Access == "" || response.Refresh == "" || response.Expires <= 0 || response.Expires > 366*24*3600 {
		return Tokens{}, Failure(CodeProviderDown, time.Time{})
	}
	var scopes []string
	var scope string
	if len(response.Scope) == 0 || string(response.Scope) == "null" {
		scopes = append([]string(nil), omitted...)
	} else if json.Unmarshal(response.Scope, &scope) == nil {
		scopes = strings.FieldsFunc(scope, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	} else if json.Unmarshal(response.Scope, &scopes) != nil {
		return Tokens{}, Failure(CodeInsufficientScope, time.Time{})
	}
	if len(scopes) > 128 || !hasScopes(scopes, []string{"read"}) {
		return Tokens{}, Failure(CodeInsufficientScope, time.Time{})
	}
	tokens := Tokens{AccessToken: response.Access, RefreshToken: response.Refresh, ExpiresAt: now.Add(time.Duration(response.Expires) * time.Second), GrantedScopes: ScopeSet(scopes)}
	if !validTokens(tokens) {
		return Tokens{}, Failure(CodeProviderDown, time.Time{})
	}
	return tokens, nil
}
func (p *linearProvider) Verify(ctx context.Context, a Account, token string) (Principal, error) {
	if err := linearAccount(a); err != nil {
		return Principal{}, err
	}
	raw, err := p.request(ctx, p.graphURL, "application/json", `{"query":"{ viewer { id active } }"}`, token)
	if err != nil {
		return Principal{}, err
	}
	var response struct {
		Data struct {
			Viewer *struct {
				ID     string
				Active bool
			}
		}
		Errors []json.RawMessage
	}
	if json.Unmarshal(raw, &response) != nil || len(response.Errors) > 0 || response.Data.Viewer == nil {
		return Principal{}, Failure(CodeProviderDown, time.Time{})
	}
	if !response.Data.Viewer.Active || response.Data.Viewer.ID != a.User {
		return Principal{}, Failure(CodePrincipalMismatch, time.Time{})
	}
	// Actual scopes come from the token response, not from the viewer query.
	return Principal{ID: response.Data.Viewer.ID, Login: response.Data.Viewer.ID}, nil
}
func (p *linearProvider) Rediscover(ctx context.Context, a Account, token string) error {
	_, err := p.Verify(ctx, a, token)
	return err
}
func (p *linearProvider) Revoke(ctx context.Context, a Account, t Tokens) (bool, error) {
	if err := linearAccount(a); err != nil {
		return true, err
	}
	_, err := p.request(ctx, p.revokeURL, "application/x-www-form-urlencoded", url.Values{"token": {t.RefreshToken}, "token_type_hint": {"refresh_token"}}.Encode(), "")
	return true, err
}
func (p *linearProvider) request(ctx context.Context, endpoint, contentType, body, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, Failure(CodeProviderDown, time.Time{})
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, Failure(CodeProviderDown, time.Time{})
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, Failure(CodeProviderDown, time.Time{})
	}
	var failure struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &failure)
	code := ""
	switch {
	case response.StatusCode == 429:
		code = CodeRateLimited
	case failure.Error == "invalid_grant" || response.StatusCode == 401:
		code = CodeGrantRevoked
	case response.StatusCode == 403 && (strings.Contains(strings.ToLower(string(raw)), "saml") || strings.Contains(strings.ToLower(string(raw)), "sso")):
		code = CodeSSORequired
	case failure.Error == "access_denied" || failure.Error == "invalid_scope" || response.StatusCode == 403:
		code = CodeInsufficientScope
	case response.StatusCode < 200 || response.StatusCode >= 300 || failure.Error != "":
		code = CodeProviderDown
	}
	if code != "" {
		retry := time.Time{}
		if code == CodeRateLimited {
			retry = time.Now().Add(time.Minute)
			if seconds, err := strconv.ParseInt(response.Header.Get("Retry-After"), 10, 32); err == nil && seconds >= 0 {
				retry = time.Now().Add(time.Duration(seconds) * time.Second)
			} else if date, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
				retry = date
			}
		}
		return nil, Failure(code, retry)
	}
	return raw, nil
}
