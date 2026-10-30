package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Owner-approved public application identifiers, not secrets. Separate apps
// preserve provider-enforced read-only authorization, not merely a local gate.
const githubReadClientID = "Iv23lixJ0srsgnPbExrP"  // betterleaks:allow
const githubWriteClientID = "Iv23liwvINakcZA9rAst" // betterleaks:allow
const GitHubReadApp = "bl-cli-read"
const GitHubWriteApp = "bl-cli"

type githubProvider struct {
	client                      *http.Client
	deviceURL, tokenURL, apiURL string
	now                         func() time.Time
	wait                        func(context.Context, time.Duration) error
}

func newGitHubProvider() *githubProvider {
	return &githubProvider{client: &http.Client{Timeout: 15 * time.Second}, deviceURL: "https://github.com/login/device/code", tokenURL: "https://github.com/login/oauth/access_token", apiURL: "https://api.github.com", now: time.Now, wait: waitGitHubPoll}
}
func (*githubProvider) ID() string { return "github" }

// These are internal capability labels, never OAuth scope request parameters.
func (*githubProvider) ReadScopes(Account) []string  { return []string{"read"} }
func (*githubProvider) WriteScopes(Account) []string { return []string{"write"} }
func githubAccount(a Account) error {
	if a.Host != "" && a.Host != "github.com" {
		return Failure(CodeProviderUnavailable, time.Time{})
	}
	return nil
}
func githubApp(scopes []string) (string, string) {
	if hasScopes(scopes, []string{"write"}) {
		return githubWriteClientID, GitHubWriteApp
	}
	return githubReadClientID, GitHubReadApp
}
func waitGitHubPoll(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *githubProvider) Authorize(ctx context.Context, a Account, scopes []string, previous *Tokens, interaction Interaction) (Tokens, error) {
	if err := githubAccount(a); err != nil {
		return Tokens{}, err
	}
	if !interactive(interaction) {
		return Tokens{}, Failure(CodeInteractionRequired, time.Time{})
	}
	clientID, slug := githubApp(scopes)
	// --write is a separate device authorization, never an OAuth scope escalation.
	if slug == GitHubWriteApp && previous == nil {
		return Tokens{}, Failure(CodeAuthenticationRequired, time.Time{})
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if _, err := fmt.Fprintf(interaction.Out, "Install %s on selected repositories first: https://github.com/apps/%s/installations/new\n", slug, slug); err != nil {
		return Tokens{}, Failure(CodeInteractionRequired, time.Time{})
	}
	raw, err := p.request(ctx, http.MethodPost, p.deviceURL, url.Values{"client_id": {clientID}}.Encode(), "")
	if err != nil {
		return Tokens{}, err
	}
	var device struct {
		Device   string `json:"device_code"`
		User     string `json:"user_code"`
		URI      string `json:"verification_uri"`
		Expires  int64  `json:"expires_in"`
		Interval int64  `json:"interval"`
	}
	if json.Unmarshal(raw, &device) != nil || !validToken(device.Device) || device.Device == "" || len(device.User) != 9 || device.User[4] != '-' || device.URI != "https://github.com/login/device" || device.Expires <= 0 || device.Expires > 900 || device.Interval < 1 || device.Interval > 900 {
		return Tokens{}, Failure(CodeProviderDown, time.Time{})
	}
	for _, c := range strings.ReplaceAll(device.User, "-", "") {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return Tokens{}, Failure(CodeProviderDown, time.Time{})
		}
	}
	if _, err = fmt.Fprintf(interaction.Out, "Open https://github.com/login/device and enter %s\n", device.User); err != nil {
		return Tokens{}, Failure(CodeInteractionRequired, time.Time{})
	}
	deadline := p.now().Add(time.Duration(device.Expires) * time.Second)
	pollCtx, stop := context.WithTimeout(ctx, time.Duration(device.Expires)*time.Second)
	defer stop()
	interval := time.Duration(device.Interval) * time.Second
	for {
		if !p.now().Add(interval).Before(deadline) {
			return Tokens{}, Failure(CodeTokenExpired, time.Time{})
		}
		if p.wait(pollCtx, interval) != nil {
			return Tokens{}, Failure(CodeTokenExpired, time.Time{})
		}
		raw, err = p.request(pollCtx, http.MethodPost, p.tokenURL, url.Values{"client_id": {clientID}, "device_code": {device.Device}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}.Encode(), "")
		if err != nil {
			return Tokens{}, err
		}
		var status struct {
			Error    string `json:"error"`
			Interval int64  `json:"interval"`
		}
		if json.Unmarshal(raw, &status) != nil {
			return Tokens{}, Failure(CodeProviderDown, time.Time{})
		}
		switch status.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			if status.Interval > 900 {
				return Tokens{}, Failure(CodeProviderDown, time.Time{})
			}
			if time.Duration(status.Interval)*time.Second > interval {
				interval = time.Duration(status.Interval) * time.Second
			}
			continue
		case "":
			tokens, err := decodeGitHubTokens(raw, p.now(), scopes)
			if err != nil {
				return Tokens{}, err
			}
			if _, err = p.installations(ctx, tokens.AccessToken, slug); err != nil {
				return Tokens{}, err
			}
			return tokens, nil
		default:
			return Tokens{}, githubTokenFailure(status.Error)
		}
	}
}
func (p *githubProvider) Refresh(ctx context.Context, a Account, t Tokens) (Tokens, error) {
	if err := githubAccount(a); err != nil {
		return Tokens{}, err
	}
	clientID, _ := githubApp(t.GrantedScopes)
	raw, err := p.request(ctx, http.MethodPost, p.tokenURL, url.Values{"client_id": {clientID}, "grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}}.Encode(), "")
	if err != nil {
		return Tokens{}, err
	}
	return decodeGitHubTokens(raw, p.now(), t.GrantedScopes)
}
func decodeGitHubTokens(raw []byte, now time.Time, scopes []string) (Tokens, error) {
	var r struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int64  `json:"expires_in"`
		Type    string `json:"token_type"`
		Scope   string `json:"scope"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return Tokens{}, Failure(CodeProviderDown, time.Time{})
	}
	if r.Error != "" {
		return Tokens{}, githubTokenFailure(r.Error)
	}
	if !strings.EqualFold(r.Type, "bearer") || r.Access == "" || r.Scope != "" || r.Expires < 0 || r.Expires > 366*24*3600 || r.Expires > 0 && r.Refresh == "" || r.Expires == 0 && r.Refresh != "" {
		return Tokens{}, Failure(CodeProviderDown, time.Time{})
	}
	t := Tokens{AccessToken: r.Access, RefreshToken: r.Refresh, GrantedScopes: ScopeSet(scopes)}
	if r.Expires > 0 {
		t.ExpiresAt = now.Add(time.Duration(r.Expires) * time.Second)
	}
	if !validTokens(t) {
		return Tokens{}, Failure(CodeProviderDown, time.Time{})
	}
	return t, nil
}
func githubTokenFailure(code string) error {
	switch code {
	case "access_denied":
		return Failure(CodeInsufficientScope, time.Time{})
	case "expired_token", "token_expired":
		return Failure(CodeTokenExpired, time.Time{})
	case "bad_refresh_token", "invalid_grant":
		return Failure(CodeGrantRevoked, time.Time{})
	default:
		return Failure(CodeProviderDown, time.Time{})
	}
}

// GitHubInstallation is live permission evidence, not a token scope. App user
// access remains the intersection of this grant and the user's permissions.
type GitHubInstallation struct {
	ID      int64  `json:"id"`
	AppSlug string `json:"app_slug"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	Permissions map[string]string `json:"permissions"`
	SuspendedAt *string           `json:"suspended_at"`
}

// GitHubInstallations never follows provider-supplied URLs. The caller supplies
// its existing authenticated transport; pages and response sizes stay bounded.
func GitHubInstallations(ctx context.Context, get func(context.Context, string) ([]byte, error)) ([]GitHubInstallation, error) {
	var result []GitHubInstallation
	seen := map[int64]bool{}
	total := -1
	for page := 1; page <= 100; page++ {
		raw, err := get(ctx, "/user/installations?per_page=100&page="+strconv.Itoa(page))
		if err != nil {
			return nil, err
		}
		var r struct {
			Total         *int                 `json:"total_count"`
			Installations []GitHubInstallation `json:"installations"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Total == nil || *r.Total < 0 || *r.Total > 10000 || total >= 0 && total != *r.Total || len(r.Installations) > 100 {
			return nil, Failure(CodeProviderDown, time.Time{})
		}
		total = *r.Total
		for _, i := range r.Installations {
			if i.ID <= 0 || seen[i.ID] || i.Account.Login == "" || i.AppSlug != "bl-cli" && i.AppSlug != "bl-cli-read" {
				return nil, Failure(CodeInsufficientScope, time.Time{})
			}
			seen[i.ID] = true
			if i.AppSlug == GitHubReadApp {
				for _, permission := range i.Permissions {
					if permission != "read" && permission != "none" {
						return nil, Failure(CodeInsufficientScope, time.Time{})
					}
				}
			}
			result = append(result, i)
		}
		if len(result) == total {
			return result, nil
		}
		if len(result) > total || len(r.Installations) != 100 {
			return nil, Failure(CodeProviderDown, time.Time{})
		}
	}
	return nil, Failure(CodeProviderDown, time.Time{})
}
func (p *githubProvider) installations(ctx context.Context, token, slug string) ([]GitHubInstallation, error) {
	all, err := GitHubInstallations(ctx, func(ctx context.Context, path string) ([]byte, error) {
		return p.request(ctx, http.MethodGet, p.apiURL+path, "", token)
	})
	if err != nil {
		return nil, err
	}
	// An app user token lists only its own app's installations.
	if len(all) == 0 && slug != "" {
		return nil, GitHubNotInstalled(slug)
	}
	if len(all) == 0 {
		return nil, Failure(CodeAppNotInstalled, time.Time{})
	}
	active := false
	for _, i := range all {
		if slug != "" && i.AppSlug != slug {
			return nil, Failure(CodeInsufficientScope, time.Time{})
		}
		if i.SuspendedAt == nil && (i.Permissions["issues"] == "read" || i.Permissions["issues"] == "write") {
			active = true
		}
	}
	if !active {
		return nil, Failure(CodeInsufficientScope, time.Time{})
	}
	return all, nil
}
func (p *githubProvider) Verify(ctx context.Context, a Account, token string) (Principal, error) {
	if err := githubAccount(a); err != nil {
		return Principal{}, err
	}
	raw, err := p.request(ctx, http.MethodGet, p.apiURL+"/user", "", token)
	if err != nil {
		return Principal{}, err
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if json.Unmarshal(raw, &user) != nil || user.ID <= 0 {
		return Principal{}, Failure(CodeProviderDown, time.Time{})
	}
	if !strings.EqualFold(user.Login, a.User) {
		return Principal{}, Failure(CodePrincipalMismatch, time.Time{})
	}
	installations, err := p.installations(ctx, token, "")
	if err != nil {
		return Principal{}, err
	}
	scopes := []string{"read"}
	if installations[0].AppSlug == GitHubWriteApp {
		scopes = append(scopes, "write")
	}
	for _, i := range installations {
		if i.AppSlug != installations[0].AppSlug {
			return Principal{}, Failure(CodeInsufficientScope, time.Time{})
		}
	}
	return Principal{ID: strconv.FormatInt(user.ID, 10), Login: a.User, Scopes: scopes}, nil
}
func (p *githubProvider) Rediscover(ctx context.Context, a Account, token string) error {
	_, err := p.Verify(ctx, a, token)
	return err
}
func (*githubProvider) Revoke(context.Context, Account, Tokens) (bool, error) { return false, nil }
func (p *githubProvider) request(ctx context.Context, method, endpoint, body, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, Failure(CodeProviderDown, time.Time{})
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r, err := client.Do(req)
	if err != nil {
		return nil, Failure(CodeProviderDown, time.Time{})
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, Failure(CodeProviderDown, time.Time{})
	}
	switch {
	case r.StatusCode == 429 || r.StatusCode == 403 && r.Header.Get("X-RateLimit-Remaining") == "0" || r.StatusCode == 403 && r.Header.Get("Retry-After") != "":
		retry := p.now().Add(time.Minute)
		if seconds, err := strconv.ParseInt(r.Header.Get("Retry-After"), 10, 32); err == nil && seconds >= 0 {
			retry = p.now().Add(time.Duration(seconds) * time.Second)
		} else if date, err := http.ParseTime(r.Header.Get("Retry-After")); err == nil {
			retry = date
		} else if seconds, err := strconv.ParseInt(r.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			retry = time.Unix(seconds, 0)
		}
		return nil, Failure(CodeRateLimited, retry)
	case r.StatusCode == 401:
		return nil, Failure(CodeGrantRevoked, time.Time{})
	case r.StatusCode == 403:
		if strings.Contains(strings.ToLower(string(raw)), "saml") || strings.Contains(strings.ToLower(string(raw)), "sso") {
			return nil, Failure(CodeSSORequired, time.Time{})
		}
		return nil, Failure(CodeInsufficientScope, time.Time{})
	case r.StatusCode < 200 || r.StatusCode >= 300:
		return nil, Failure(CodeProviderDown, time.Time{})
	}
	return raw, nil
}
