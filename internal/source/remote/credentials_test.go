package remote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

func helper(t *testing.T, script string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credential-helper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return []string{path}
}

// Catches subprocess credential inheritance, helper stderr/verifier leakage,
// principal substitution, oversized helper output and unsafe token formatting.
func TestCredentialsBoundedPrivateAndVerified(t *testing.T) {
	testkit.Isolate(t)
	const canary = "canary-private-credential-49bc"
	t.Setenv("GH_TOKEN", canary)
	t.Setenv("WORKLEASE_SESSION_ID", canary)
	secrets := &Secrets{}
	resolver := &Credentials{Secrets: secrets}
	account := project.AccountConfig{User: "alice", Credential: helper(t, "[ -z \"$GH_TOKEN$WORKLEASE_SESSION_ID\" ] || exit 2\nprintf 'canary-private-credential-49bc\\n'\n")}
	verify := func(_ context.Context, credential Credential) (string, error) {
		if credential.token != canary {
			t.Fatal("wrong helper token")
		}
		return "alice", nil
	}
	credential, err := resolver.Resolve(context.Background(), account, verify)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", credential, credential), canary) {
		t.Fatal("credential formatting leaked")
	}
	if secrets.Redact("prefix "+canary) != "prefix [REDACTED]" {
		t.Fatal("credential not registered for redaction")
	}
	badVerify := func(context.Context, Credential) (string, error) { return "", fmt.Errorf("error %s", canary) }
	if _, err = resolver.Resolve(context.Background(), account, badVerify); err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("unsafe verifier error: %v", err)
	}
	account.User = "mallory"
	if _, err = resolver.Resolve(context.Background(), account, verify); err == nil {
		t.Fatal("wrong principal accepted")
	}
	account.User = "alice"
	for _, script := range []string{"printf 'canary-private-credential-49bc' >&2; exit 2\n", "printf '%4097s' x\n", "printf '%4097s' x >&2; printf 'token'\n"} {
		account.Credential = helper(t, script)
		if _, err = resolver.Resolve(context.Background(), account, verify); err == nil || strings.Contains(err.Error(), canary) {
			t.Fatalf("unsafe helper failure: %v", err)
		}
	}
	account.Credential = nil
	account.Env = "GH_TOKEN"
	if _, err = resolver.Resolve(context.Background(), account, verify); err != nil {
		t.Fatal(err)
	}
}
