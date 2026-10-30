package cli

import (
	"encoding/json"
	"io"
)

// Envelope is the versioned machine-output contract from design.md §7.
type Envelope struct {
	SchemaVersion int    `json:"schemaVersion"`
	OK            bool   `json:"ok"`
	Command       string `json:"command"`
	Data          any    `json:"data,omitempty"`
	Error         *Error `json:"error,omitempty"`
}

type Error struct {
	Reason  string         `json:"reason"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
	Data    any            `json:"-"` // per-ID outcomes on a failed batch
}

func (e *Error) Error() string { return e.Message }

// MarshalJSON keeps details an object, even when there is no additional metadata.
func (e *Error) MarshalJSON() ([]byte, error) {
	type wire Error
	value := wire(*e)
	if value.Details == nil {
		value.Details = map[string]any{}
	}
	return json.Marshal(value)
}

func (e *Error) ExitCode() int {
	switch e.Reason {
	case "usage", "session-required":
		return 2
	case "not-found":
		return 3
	case "conflict", "claim-held", "run-mismatch", "other-host", "checkout-removed", "live", "no-agent-config":
		return 4
	case "rejected", "invalid-config", "invalid-value", "oauth-not-opted-in":
		return 5
	case "unavailable", "unknown-outcome", "authentication-required", "oauth-provider-unavailable", "token-expired", "grant-revoked", "insufficient-scope", "app-not-installed", "sso-required", "rate-limited", "provider-unavailable", "principal-mismatch", "credential-store-unavailable", "interactive-login-required":
		return 6
	case "contention":
		return 7
	default:
		return 1
	}
}

func success(out io.Writer, command string, data any) error {
	return json.NewEncoder(out).Encode(Envelope{
		SchemaVersion: 1, OK: true, Command: command, Data: data,
	})
}
