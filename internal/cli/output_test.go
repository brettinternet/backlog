package cli_test

import (
	"encoding/json"
	"testing"

	"github.com/brettinternet/backlog/internal/cli"
)

// These mappings are consumed by agents; changing one can retry rejected writes
// or mistake contention for a usage failure.
func TestExitReasons(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason string
		code   int
	}{
		{"internal", 1}, {"usage", 2}, {"session-required", 2},
		{"not-found", 3}, {"conflict", 4}, {"claim-held", 4}, {"run-mismatch", 4},
		{"rejected", 5}, {"invalid-config", 5}, {"unavailable", 6}, {"contention", 7},
		{"unknown-future-reason", 1},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			failure := &cli.Error{Reason: tc.reason, Message: "failed", Details: map[string]any{"item": "BL-1"}}
			if failure.ExitCode() != tc.code {
				t.Fatalf("exit=%d want %d", failure.ExitCode(), tc.code)
			}
			raw, err := json.Marshal(cli.Envelope{SchemaVersion: 1, Command: "set", Error: failure})
			if err != nil {
				t.Fatal(err)
			}
			var envelope map[string]any
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			body := envelope["error"].(map[string]any)
			if len(envelope) != 4 || envelope["schemaVersion"] != float64(1) || envelope["ok"] != false || envelope["command"] != "set" || body["reason"] != tc.reason || body["message"] != "failed" || body["details"].(map[string]any)["item"] != "BL-1" {
				t.Fatalf("wrong error shape: %s", raw)
			}
		})
	}
}
