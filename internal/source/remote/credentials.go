// Package remote supplies the shared cache, credential and HTTP boundaries for
// remote adapters. Provider adapters still own their wire format and versions.
package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/proctree"
	"github.com/brettinternet/backlog/internal/project"
)

// Secrets holds credentials only in memory. Share one instance across the
// client's adapters so old credentials remain redacted after a refresh.
type Secrets struct {
	mu     sync.RWMutex
	values []string
}

func (*Secrets) String() string   { return "[REDACTED]" }
func (*Secrets) GoString() string { return "[REDACTED]" }

// ProcessSecrets is shared by configured remote sources and explicit OAuth
// lifecycle commands so rotated tokens remain redacted for the process lifetime.
var ProcessSecrets = &Secrets{}

func (s *Secrets) add(value string) {
	if s == nil || value == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.values {
		if old == value {
			return
		}
	}
	s.values = append(s.values, value)
}
func (s *Secrets) Redact(text string) string {
	if s == nil {
		return text
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.values {
		encoded, _ := json.Marshal(value)
		text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), "[REDACTED]")
		text = strings.ReplaceAll(text, value, "[REDACTED]")
	}
	return text
}

// Credential deliberately has no exported token field or JSON representation.
// Only Client can attach it to a request. Formatting never reveals the value.
// Register adds a credential to the process-wide redaction set before it can
// be reflected by a verifier or provider response.
func (s *Secrets) Register(value string) { s.add(value) }

type Credential struct {
	token  string
	oauth  bool
	scopes []string
}

// HasScope preserves helper behavior; OAuth permissions come only from the
// actual token response stored by the shared manager.
func (c Credential) HasScope(scope string) bool {
	if !c.oauth {
		return true
	}
	for _, granted := range c.scopes {
		if granted == scope {
			return true
		}
	}
	return false
}

func (Credential) String() string   { return "[REDACTED]" }
func (Credential) GoString() string { return "[REDACTED]" }

// Credentials is the bounded helper/principal-verification path ported from
// Worklease v1.9.3 internal/queue/credential_helper.go, without its journals.
// Share it across adapters: identical helper commands cannot refresh in parallel.
type Credentials struct {
	Secrets *Secrets
	OAuth   *oauth.Manager
	mu      sync.Mutex
	locks   map[[32]byte]chan struct{}
}

// Resolve uses only an explicitly selected user account, never ambient provider
// credentials. The provider must verify the supplied token's principal. Resolve
// again immediately before writes. Neither helper nor verifier errors escape.
func (h *Credentials) Resolve(ctx context.Context, account project.AccountConfig, verify func(context.Context, Credential) (string, error)) (Credential, error) {
	fail := func(message string) (Credential, error) { return Credential{}, fmt.Errorf("credentials: %s", message) }
	if h.Secrets == nil || account.User == "" || verify == nil {
		return fail("expected user, secret store and verifier required")
	}
	if account.OAuth {
		if len(account.Credential) != 0 || account.Env != "" || h.OAuth == nil || account.Provider == "" {
			return Credential{}, oauth.Failure(oauth.CodeProviderUnavailable, time.Time{})
		}
		tokens, err := h.OAuth.Resolve(ctx, oauth.Account{Provider: account.Provider, Name: account.Name, Host: account.Host, User: account.User, OptIn: account.OAuth}, func(ctx context.Context, tokens oauth.Tokens) (string, error) {
			h.Secrets.add(tokens.AccessToken)
			credential := Credential{token: tokens.AccessToken, oauth: true, scopes: tokens.GrantedScopes}
			principal, verifyErr := verify(ctx, credential)
			if verifyErr != nil {
				var limited ScheduleDiagnostic
				if errors.As(verifyErr, &limited) {
					return "", oauth.Failure(oauth.CodeRateLimited, limited.RetryAt)
				}
			}
			return principal, verifyErr
		})
		if err != nil {
			return Credential{}, err
		}
		return Credential{token: tokens.AccessToken, oauth: true, scopes: tokens.GrantedScopes}, nil
	}
	if (len(account.Credential) == 0) == (account.Env == "") {
		return fail("one helper or environment variable required")
	}
	key := sha256.Sum256([]byte(strings.Join(account.Credential, "\x00") + "\x00" + account.Env))
	h.mu.Lock()
	if h.locks == nil {
		h.locks = make(map[[32]byte]chan struct{})
	}
	lock := h.locks[key]
	if lock == nil {
		lock = make(chan struct{}, 1)
		h.locks[key] = lock
	}
	h.mu.Unlock()
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return fail("canceled waiting for refresh")
	}
	defer func() { <-lock }()
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	token := os.Getenv(account.Env)
	if len(account.Credential) > 0 {
		if account.Credential[0] == "" {
			return fail("helper executable required")
		}
		cmd := exec.CommandContext(bounded, account.Credential[0], account.Credential[1:]...)
		cmd.Env = helperEnvironment()
		cmd.WaitDelay = time.Second
		output, stderr := &boundedOutput{cancel: cancel}, &boundedOutput{cancel: cancel}
		cmd.Stdout, cmd.Stderr = output, stderr
		tree, err := proctree.Start(cmd)
		defer tree.Kill()
		if err == nil {
			err = cmd.Wait()
		}
		if err != nil || output.exceeded || stderr.exceeded {
			return fail("helper failed or exceeded 15s/4KiB")
		}
		token = strings.TrimSpace(output.buffer.String())
	}
	if len(token) == 0 || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return fail("expected one non-empty token line, at most 4KiB")
	}
	h.Secrets.add(token) // before the verifier can reflect it in any response
	principal, err := verify(bounded, Credential{token: token})
	if err != nil {
		var diagnostic ScheduleDiagnostic
		if errors.As(err, &diagnostic) {
			return Credential{}, diagnostic
		}
		return fail("cannot verify authenticated principal")
	}
	if principal != account.User {
		return fail("authenticated principal differs from configured user")
	}
	return Credential{token: token}, nil
}
func helperEnvironment() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

type boundedOutput struct {
	buffer   bytes.Buffer
	exceeded bool
	cancel   context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.buffer.Len()+n > 4096 {
		b.exceeded = true
		b.cancel()
		p = p[:max(0, 4096-b.buffer.Len())]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
