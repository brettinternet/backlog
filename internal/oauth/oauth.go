// Package oauth owns opt-in remote-account tokens. Provider integrations implement
// the small Provider contract; this package owns consent sequencing, protected
// persistence, principal checks, refresh serialization, and safe diagnostics.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"time"
)

const (
	CodeAuthenticationRequired = "authentication-required"
	CodeProviderUnavailable    = "oauth-provider-unavailable"
	CodeTokenExpired           = "token-expired"
	CodeGrantRevoked           = "grant-revoked"
	CodeInsufficientScope      = "insufficient-scope"
	CodeSSORequired            = "sso-required"
	CodeRateLimited            = "rate-limited"
	CodeProviderDown           = "provider-unavailable"
	CodePrincipalMismatch      = "principal-mismatch"
	CodeNotOptedIn             = "oauth-not-opted-in"
	CodeStorageUnavailable     = "credential-store-unavailable"
	CodeInteractionRequired    = "interactive-login-required"
)

// Diagnostic contains only a stable code, a fixed safe message and optional
// retry time. Provider response bodies and URLs are never copied into it.
type Diagnostic struct {
	Code    string    `json:"code"`
	Message string    `json:"message"`
	RetryAt time.Time `json:"retryAt,omitempty"`
}

func (d *Diagnostic) Error() string {
	if d == nil {
		return "OAuth operation failed"
	}
	return d.Code + ": " + d.Message
}
func (d *Diagnostic) String() string   { return d.Error() }
func (d *Diagnostic) GoString() string { return d.Error() }
func (d Diagnostic) MarshalJSON() ([]byte, error) {
	value := map[string]any{"code": d.Code, "message": d.Message}
	if !d.RetryAt.IsZero() {
		value["retryAt"] = d.RetryAt
	}
	return json.Marshal(value)
}

var messages = map[string]string{
	CodeAuthenticationRequired: "sign in explicitly with `bl auth login PROVIDER ACCOUNT` before using this account",
	CodeProviderUnavailable:    "this provider has no registered native OAuth flow in this build; use its configured credential helper or environment account",
	CodeTokenExpired:           "the OAuth access token expired; run `bl auth login PROVIDER ACCOUNT`",
	CodeGrantRevoked:           "the OAuth grant was revoked; run `bl auth login PROVIDER ACCOUNT`",
	CodeInsufficientScope:      "the OAuth grant lacks required permissions; enable writes explicitly with `bl auth login PROVIDER ACCOUNT --write`",
	CodeSSORequired:            "the provider requires SSO/SAML authorization for this account; authorize the app in the provider organization",
	CodeRateLimited:            "the provider rate-limited OAuth; retry after the reported time",
	CodeProviderDown:           "the provider OAuth service is unavailable; retry later",
	CodePrincipalMismatch:      "the authenticated principal differs from the configured account; this source is disabled until the account is corrected",
	CodeNotOptedIn:             "OAuth is not enabled for this user account; set `oauth: true` in user config and remove helper/environment credentials",
	CodeStorageUnavailable:     "the owner-private OAuth token store is unavailable; check its permissions and free disk space",
	CodeInteractionRequired:    "OAuth login requires an interactive terminal; run `bl auth login` from a terminal",
}

// Failure constructs a provider-safe structured diagnostic. Unknown codes are
// deliberately reduced to a provider-down error.
func Failure(code string, retryAt time.Time) *Diagnostic {
	message, ok := messages[code]
	if !ok {
		code, message = CodeProviderDown, messages[CodeProviderDown]
	}
	return &Diagnostic{Code: code, Message: message, RetryAt: retryAt}
}

// Account is a user-configured identity. OptIn is only set from user config;
// HasLegacyCredential prevents OAuth from silently replacing helper/env auth.
type Account struct {
	Provider            string
	Name                string
	Host                string
	User                string
	OptIn               bool
	HasLegacyCredential bool
}

// Tokens never serialize or format their values except in the private store.
// The token strings are only exposed for a provider request or remote client.
type Tokens struct {
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	ExpiresAt    time.Time `json:"-"`
	// GrantedScopes is populated from a provider token response when no scope
	// introspection endpoint exists. It is persisted with the rotated pair.
	GrantedScopes []string `json:"-"`
}

func (Tokens) String() string   { return "[REDACTED]" }
func (Tokens) GoString() string { return "[REDACTED]" }

// Principal is the provider-verified immutable identity and actual permissions.
type Principal struct {
	ID     string
	Login  string
	Scopes []string
}

// Interaction is supplied only by an explicit interactive login command. The
// provider owns its protocol-specific browser/device-code UX; shared code never
// invents endpoints or writes access/refresh tokens to these streams.
type Interaction struct {
	In  io.Reader
	Out io.Writer
}

// Provider is implemented by an in-tree provider child, not configured by users.
// Authorize may use a prior token only to implement the provider's documented
// consent/upgrade behavior. Verify must return immutable ID, configured login,
// and actual scopes/permissions for the supplied access token.
type Provider interface {
	ID() string
	ReadScopes(Account) []string
	WriteScopes(Account) []string
	Authorize(context.Context, Account, []string, *Tokens, Interaction) (Tokens, error)
	Refresh(context.Context, Account, Tokens) (Tokens, error)
	Verify(context.Context, Account, string) (Principal, error)
	Rediscover(context.Context, Account, string) error
	Revoke(context.Context, Account, Tokens) (supported bool, err error)
}

// Registry contains built-in provider flows. It intentionally has no user
// endpoint/configuration mechanism; BL-61/62 register qualified providers.
type Registry map[string]Provider

func NewRegistry(providers ...Provider) (Registry, error) {
	registry := Registry{}
	for _, provider := range providers {
		if provider == nil || !validProviderID(provider.ID()) || registry[provider.ID()] != nil {
			return nil, fmt.Errorf("invalid or duplicate OAuth provider")
		}
		registry[provider.ID()] = provider
	}
	return registry, nil
}

func validProviderID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

var defaultRegistry = struct {
	sync.RWMutex
	providers Registry
}{providers: Registry{"linear": newLinearProvider(), "github": newGitHubProvider()}}

// RegisterDefault registers one provider implementation compiled into bl.
func RegisterDefault(provider Provider) error {
	if provider == nil || !validProviderID(provider.ID()) {
		return fmt.Errorf("invalid OAuth provider")
	}
	defaultRegistry.Lock()
	defer defaultRegistry.Unlock()
	if defaultRegistry.providers[provider.ID()] != nil {
		return fmt.Errorf("duplicate OAuth provider")
	}
	defaultRegistry.providers[provider.ID()] = provider
	return nil
}

// DefaultRegistry returns a copy of the in-tree provider registry.
func DefaultRegistry() Registry {
	defaultRegistry.RLock()
	defer defaultRegistry.RUnlock()
	return maps.Clone(defaultRegistry.providers)
}

// SecretSink records token values for in-memory redaction before any verifier,
// provider response, or source adapter can reflect them.
type SecretSink interface{ Register(string) }

// PrincipalKey partitions disposable cache rows by provider principal, not by
// source alias. It contains no token and is suitable only as a hash input.
func PrincipalKey(provider, host, user string) string {
	return strings.ToLower(provider) + "\x00" + strings.ToLower(host) + "\x00" + user
}

// ScopeSet returns a de-duplicated copy while preserving provider order.
func ScopeSet(scopes ...[]string) []string {
	seen := map[string]bool{}
	var result []string
	for _, group := range scopes {
		for _, scope := range group {
			if scope != "" && !seen[scope] {
				seen[scope] = true
				result = append(result, scope)
			}
		}
	}
	return result
}

func hasScopes(granted, required []string) bool {
	set := make(map[string]bool, len(granted))
	for _, scope := range granted {
		set[scope] = true
	}
	for _, scope := range required {
		if !set[scope] {
			return false
		}
	}
	return true
}

func safeProviderError(err error) *Diagnostic {
	var diagnostic *Diagnostic
	if errors.As(err, &diagnostic) && diagnostic != nil {
		return Failure(diagnostic.Code, diagnostic.RetryAt)
	}
	return Failure(CodeProviderDown, time.Time{})
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Failure(CodeProviderDown, time.Time{})
	}
	return safeProviderError(err)
}

func validateAccount(account Account) *Diagnostic {
	if !account.OptIn || account.HasLegacyCredential {
		return Failure(CodeNotOptedIn, time.Time{})
	}
	if !validProviderID(account.Provider) || account.Name == "" || account.User == "" {
		return Failure(CodeProviderUnavailable, time.Time{})
	}
	return nil
}

func validTokens(tokens Tokens) bool {
	return validToken(tokens.AccessToken) && validToken(tokens.RefreshToken)
}
func validToken(value string) bool {
	return len(value) <= 8192 && !strings.ContainsAny(value, "\r\n\x00")
}
