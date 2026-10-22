package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	urfave "github.com/urfave/cli/v3"
)

func claimCommands(directory *string, out io.Writer) []*urfave.Command {
	commands := []*urfave.Command{}
	for _, name := range []string{"claim", "release", "heartbeat"} {
		commands = append(commands, &urfave.Command{Name: name, Usage: "Coordinate item ownership", Action: func(ctx context.Context, cmd *urfave.Command) error {
			expected := 1
			if cmd.Name == "heartbeat" {
				expected = 0
			}
			if cmd.Args().Len() != expected {
				return writeUsage(fmt.Sprintf("%s requires %d item IDs", cmd.Name, expected))
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			var result model.ClaimResult
			if cmd.Name == "heartbeat" {
				result = engine.Heartbeat(ctx)
			} else {
				item, e := writeItem(ctx, cfg, cmd.Args().First())
				if e != nil {
					return e
				}
				if cmd.Name == "claim" {
					result = engine.ClaimItem(ctx, item)
				} else {
					result = engine.ReleaseItem(ctx, item)
				}
			}
			if result.Reason != "" {
				return &Error{Reason: result.Reason, Message: result.Message, Data: result}
			}
			if cmd.Bool("json") {
				return success(out, cmd.Name, result)
			}
			return writeLine(out, fmt.Sprintf("%s %s %s", result.Outcome, result.Resource, result.ExpiresAt))
		}})
	}
	return commands
}
