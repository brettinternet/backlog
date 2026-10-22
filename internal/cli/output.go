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
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func (e *Error) ExitCode() int {
	switch e.Reason {
	case "usage":
		return 2
	case "invalid-config":
		return 5
	case "unavailable":
		return 6
	default:
		return 1
	}
}

func success(out io.Writer, command string, data any) error {
	return json.NewEncoder(out).Encode(Envelope{
		SchemaVersion: 1, OK: true, Command: command, Data: data,
	})
}
