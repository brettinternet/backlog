package oauth

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
)

// Manager coordinates the provider contract with its owner-private Store. Cache
// purging is performed by principal hash after local logout, even if remote
// revocation is unsupported or fails.
const expirySkew = time.Minute

type Manager struct {
	store     Store
	cacheDir  string
	providers Registry
	secrets   SecretSink
	now       func() time.Time
}

func NewManager(store Store, cacheDir string, providers Registry, secrets SecretSink) *Manager {
	copy := Registry{}
	for id, provider := range providers {
		if provider != nil && validProviderID(id) && provider.ID() == id {
			copy[id] = provider
		}
	}
	return &Manager{store: store, cacheDir: cacheDir, providers: copy, secrets: secrets, now: time.Now}
}

func (m *Manager) provider(account Account) (Provider, *Diagnostic) {
	if diagnostic := validateAccount(account); diagnostic != nil {
		return nil, diagnostic
	}
	provider := m.providers[account.Provider]
	if provider == nil {
		return nil, Failure(CodeProviderUnavailable, time.Time{})
	}
	return provider, nil
}

// Login performs a fresh explicit authorization for read-only scopes. It never
// requests write scopes; callers must separately invoke EnableWrites after a
// user opt-in. The provider owns the protocol-specific interactive exchange.
func (m *Manager) Login(ctx context.Context, account Account, interaction Interaction) error {
	if !interactive(interaction) {
		return Failure(CodeInteractionRequired, time.Time{})
	}
	provider, diagnostic := m.provider(account)
	if diagnostic != nil {
		return diagnostic
	}
	scopes := ScopeSet(provider.ReadScopes(account))
	if len(scopes) == 0 {
		return Failure(CodeProviderUnavailable, time.Time{})
	}
	err := m.store.withRecord(ctx, tokenKey(account), func(current *storedRecord, _ func(*storedRecord) error) (*storedRecord, bool, error) {
		var previous *Tokens
		if recordMatches(account, current) {
			value := tokensFrom(current)
			previous = &value
		}
		tokens, err := provider.Authorize(ctx, account, scopes, previous, interaction)
		if err != nil {
			return current, false, safeError(err)
		}
		if !validTokens(tokens) || tokens.AccessToken == "" {
			return current, false, Failure(CodeProviderDown, time.Time{})
		}
		m.register(tokens)
		principal, err := verifyGrant(ctx, provider, account, tokens)
		if err != nil {
			return current, false, safeError(err)
		}
		if err = verifyPrincipal(account, principal, ""); err != nil {
			return current, false, err
		}
		if !hasScopes(principal.Scopes, scopes) {
			return current, false, scopeFailure(principal.Scopes, scopes)
		}
		if err = provider.Rediscover(ctx, account, tokens.AccessToken); err != nil {
			return current, false, safeError(err)
		}
		record := recordFor(account, principal, tokens)
		return &record, true, nil
	})
	if err != nil {
		return storageOrDiagnostic(err)
	}
	return nil
}

// EnableWrites requests read plus write scopes only after a separate explicit
// user action, verifies the same principal, and asks the provider to rediscover
// current account capabilities before persisting the upgraded grant.
func (m *Manager) EnableWrites(ctx context.Context, account Account, interaction Interaction) error {
	if !interactive(interaction) {
		return Failure(CodeInteractionRequired, time.Time{})
	}
	provider, diagnostic := m.provider(account)
	if diagnostic != nil {
		return diagnostic
	}
	scopes := ScopeSet(provider.ReadScopes(account), provider.WriteScopes(account))
	if len(scopes) <= len(ScopeSet(provider.ReadScopes(account))) {
		return Failure(CodeProviderUnavailable, time.Time{})
	}
	err := m.store.withRecord(ctx, tokenKey(account), func(current *storedRecord, _ func(*storedRecord) error) (*storedRecord, bool, error) {
		if !recordMatches(account, current) {
			return current, false, Failure(CodeAuthenticationRequired, time.Time{})
		}
		previous := tokensFrom(current)
		tokens, err := provider.Authorize(ctx, account, scopes, &previous, interaction)
		if err != nil {
			return current, false, safeError(err)
		}
		if !validTokens(tokens) || tokens.AccessToken == "" {
			return current, false, Failure(CodeProviderDown, time.Time{})
		}
		m.register(tokens)
		principal, err := verifyGrant(ctx, provider, account, tokens)
		if err != nil {
			return current, false, safeError(err)
		}
		if err = verifyPrincipal(account, principal, current.PrincipalID); err != nil {
			return current, false, err
		}
		if !hasScopes(principal.Scopes, scopes) {
			return current, false, scopeFailure(principal.Scopes, scopes)
		}
		if err = provider.Rediscover(ctx, account, tokens.AccessToken); err != nil {
			return current, false, safeError(err)
		}
		record := recordFor(account, principal, tokens)
		return &record, true, nil
	})
	if err != nil {
		return storageOrDiagnostic(err)
	}
	return nil
}

// Resolve loads an OAuth credential, refreshes it under a cross-process lock,
// verifies every rotated token before use, and invokes the source's own bound
// principal check. A helper/env account never enters this path.
func (m *Manager) Resolve(ctx context.Context, account Account, verify func(context.Context, Tokens) (string, error)) (Tokens, error) {
	provider, diagnostic := m.provider(account)
	if diagnostic != nil {
		return Tokens{}, diagnostic
	}
	if verify == nil {
		return Tokens{}, Failure(CodeProviderUnavailable, time.Time{})
	}
	var result Tokens
	err := m.store.withRecord(ctx, tokenKey(account), func(current *storedRecord, persist func(*storedRecord) error) (*storedRecord, bool, error) {
		if current == nil {
			return nil, false, Failure(CodeAuthenticationRequired, time.Time{})
		}
		if !recordMatches(account, current) {
			return current, false, Failure(CodePrincipalMismatch, time.Time{})
		}
		currentTokens := tokensFrom(current)
		m.register(currentTokens)
		validate := func(tokens Tokens) error {
			principal, err := verifyGrant(ctx, provider, account, tokens)
			if err != nil {
				return safeError(err)
			}
			if err = verifyPrincipal(account, principal, current.PrincipalID); err != nil {
				return err
			}
			if !hasScopes(principal.Scopes, current.Scopes) {
				return scopeFailure(principal.Scopes, current.Scopes)
			}
			if err = provider.Rediscover(ctx, account, tokens.AccessToken); err != nil {
				return safeError(err)
			}
			principalID, err := verify(ctx, tokens)
			if err != nil {
				return safeError(err)
			}
			if principalID != account.User {
				return Failure(CodePrincipalMismatch, time.Time{})
			}
			return nil
		}
		// Refresh slightly early so a token is not handed out to expire mid-request.
		usable := current.ExpiresAt.IsZero() || current.ExpiresAt.After(m.now().Add(expirySkew))
		if !current.ValidationPending && usable {
			principalID, err := verify(ctx, currentTokens)
			if err != nil {
				return current, false, safeError(err)
			}
			if principalID != account.User {
				return current, false, Failure(CodePrincipalMismatch, time.Time{})
			}
			result = currentTokens
			return current, false, nil
		}
		if current.ValidationPending && usable {
			if err := validate(currentTokens); err != nil {
				return current, false, err
			}
			next := *current
			next.ValidationPending = false
			result = currentTokens
			return &next, true, nil
		}
		if current.RefreshToken == "" {
			return current, false, Failure(CodeTokenExpired, time.Time{})
		}
		rotated, err := provider.Refresh(ctx, account, currentTokens)
		if err != nil {
			return current, false, safeError(err)
		}
		if !validTokens(rotated) || rotated.AccessToken == "" {
			return current, false, Failure(CodeProviderDown, time.Time{})
		}
		m.register(rotated)
		// Persist the rotated pair as pending before validation while the file
		// lock remains held. A provider may invalidate the old refresh token
		// immediately, so retries must validate this pair rather than refresh it.
		next := *current
		if rotated.RefreshToken == "" {
			rotated.RefreshToken = current.RefreshToken
		}
		next.AccessToken, next.RefreshToken, next.ExpiresAt = rotated.AccessToken, rotated.RefreshToken, rotated.ExpiresAt
		next.ValidationPending = true
		next.GrantedScopes = rotated.GrantedScopes
		m.register(rotated)
		if err := persist(&next); err != nil {
			return current, false, err
		}
		if err := validate(rotated); err != nil {
			return &next, false, err
		}
		next.ValidationPending = false
		result = rotated
		return &next, true, nil
	})
	if err != nil {
		return Tokens{}, storageOrDiagnostic(err)
	}
	return result, nil
}

// Status is an account's stored grant as last recorded. Problem is set when the
// account can't authenticate until the user runs bl auth login. Expires is when
// that happens on its own: zero for a refreshable or non-expiring grant.
type Status struct {
	Writes  bool
	Expires time.Time
	Problem *Diagnostic
}

// Status reads the stored record without locking, refreshing, creating the
// store, or network I/O. Records are replaced by rename, so a read never sees a
// partial write.
func (m *Manager) Status(account Account) Status {
	provider, diagnostic := m.provider(account)
	if diagnostic != nil {
		return Status{Problem: diagnostic}
	}
	if !filepath.IsAbs(m.store.StateDir) {
		return Status{Problem: Failure(CodeStorageUnavailable, time.Time{})}
	}
	record, err := readRecord(filepath.Join(m.store.StateDir, "oauth", fileID(tokenKey(account))+".json"))
	switch {
	case err != nil:
		return Status{Problem: Failure(CodeStorageUnavailable, time.Time{})}
	case record == nil:
		return Status{Problem: Failure(CodeAuthenticationRequired, time.Time{})}
	case !recordMatches(account, record):
		return Status{Problem: Failure(CodePrincipalMismatch, time.Time{})}
	}
	write := ScopeSet(provider.WriteScopes(account))
	status := Status{Writes: len(write) > 0 && hasScopes(record.Scopes, write)}
	if record.RefreshToken == "" && !record.ExpiresAt.IsZero() {
		if !record.ExpiresAt.After(m.now().Add(expirySkew)) {
			status.Problem = Failure(CodeTokenExpired, time.Time{})
		}
		status.Expires = record.ExpiresAt
	}
	return status
}

// Logout revokes where the registered provider offers a public-client endpoint,
// always deletes local tokens and purges that principal's disposable cache rows.
// Unsupported or failed remote revocation is reported as a manual action.
type LogoutResult struct {
	RemoteRevoked    bool        `json:"remoteRevoked"`
	ManualRevocation bool        `json:"manualRevocation"`
	Warning          *Diagnostic `json:"warning,omitempty"`
}

func (m *Manager) Logout(ctx context.Context, account Account) (LogoutResult, error) {
	if !validProviderID(account.Provider) || account.Name == "" {
		return LogoutResult{}, Failure(CodeProviderUnavailable, time.Time{})
	}
	provider := m.providers[account.Provider]
	result := LogoutResult{}
	var revokeDiagnostic *Diagnostic
	principalKeys := map[string]bool{PrincipalKey(account.Provider, account.Host, account.User): true}
	err := m.store.withRecord(ctx, tokenKey(account), func(current *storedRecord, _ func(*storedRecord) error) (*storedRecord, bool, error) {
		if current == nil {
			return nil, false, nil
		}
		principalKeys[PrincipalKey(current.Provider, current.Host, current.User)] = true
		if recordMatches(account, current) && provider != nil {
			tokens := tokensFrom(current)
			m.register(tokens)
			supported, revokeErr := provider.Revoke(ctx, account, tokens)
			if supported && revokeErr == nil {
				result.RemoteRevoked = true
			} else if !supported {
				result.ManualRevocation = true
			} else if revokeErr != nil {
				result.ManualRevocation = true
				revokeDiagnostic = safeError(revokeErr).(*Diagnostic)
			}
		} else {
			result.ManualRevocation = true
		}
		return nil, true, nil
	})
	if err != nil {
		return result, storageOrDiagnostic(err)
	}
	if m.cacheDir != "" {
		for principal := range principalKeys {
			if err := cache.PurgePrincipal(ctx, m.cacheDir, principal); err != nil {
				return result, Failure(CodeStorageUnavailable, time.Time{})
			}
		}
	}
	if revokeDiagnostic != nil {
		result.Warning = revokeDiagnostic
	}
	return result, nil
}

// verifyGrant combines live identity verification with scopes returned by the
// provider's token endpoint, retaining scope evidence across process restarts.
func verifyGrant(ctx context.Context, provider Provider, account Account, tokens Tokens) (Principal, error) {
	principal, err := provider.Verify(ctx, account, tokens.AccessToken)
	if err == nil && tokens.GrantedScopes != nil {
		if principal.Scopes != nil && !hasScopes(principal.Scopes, tokens.GrantedScopes) {
			return Principal{}, scopeFailure(principal.Scopes, tokens.GrantedScopes)
		}
		principal.Scopes = ScopeSet(tokens.GrantedScopes)
	}
	return principal, err
}

func interactive(interaction Interaction) bool {
	return interaction.In != nil && interaction.Out != nil
}

func verifyPrincipal(account Account, principal Principal, expectedID string) error {
	if principal.ID == "" || principal.Login != account.User || expectedID != "" && principal.ID != expectedID {
		return Failure(CodePrincipalMismatch, time.Time{})
	}
	return nil
}

func (m *Manager) register(tokens Tokens) {
	if m.secrets == nil {
		return
	}
	if tokens.AccessToken != "" {
		m.secrets.Register(tokens.AccessToken)
	}
	if tokens.RefreshToken != "" {
		m.secrets.Register(tokens.RefreshToken)
	}
}

func storageOrDiagnostic(err error) error {
	if err == nil {
		return nil
	}
	var diagnostic *Diagnostic
	if errors.As(err, &diagnostic) {
		return diagnostic
	}
	return Failure(CodeStorageUnavailable, time.Time{})
}
