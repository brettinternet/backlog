package cli

import (
	"context"
	"io"
	"time"

	"github.com/brettinternet/backlog/internal/source/external"
	urfave "github.com/urfave/cli/v3"
)

func adapterCommand(directory *string, out io.Writer) *urfave.Command {
	return &urfave.Command{Name: "adapter", Usage: "Check an explicitly selected external adapter", Commands: []*urfave.Command{
		{Name: "check", Usage: "Run an adapter conformance check (executes EXEC)", ArgsUsage: "EXEC [ARG...]", Flags: []urfave.Flag{&urfave.DurationFlag{Name: "timeout", Value: 5 * time.Second}}, Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Args().Len() == 0 || cmd.Duration("timeout") <= 0 || cmd.Duration("timeout") > time.Minute {
				return &Error{Reason: "usage", Message: "use bl adapter check [--timeout 5s] EXEC [ARG...] (timeout at most 1m)"}
			}
			if err := external.Check(ctx, external.Options{ID: "conformance", Run: cmd.Args().Slice(), Dir: *directory, Timeout: cmd.Duration("timeout")}); err != nil {
				return &Error{Reason: "adapter-check", Message: err.Error()}
			}
			if cmd.Bool("json") {
				return success(out, "adapter check", map[string]any{"protocolVersion": 1, "valid": true})
			}
			return writeLine(out, "Adapter protocol v1: PASS")
		}},
	}}
}
