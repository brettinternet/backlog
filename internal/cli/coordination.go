package cli

import (
	"context"
	"io"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	urfave "github.com/urfave/cli/v3"
)

func claimCommands(directory *string, out io.Writer) []*urfave.Command {
	commands := []*urfave.Command{}
	for _, c := range [][3]string{
		{"claim", "Claim an item for this session", "ID"},
		{"release", "Release this session's claim on an item", "ID"},
		{"heartbeat", "Renew this session's claim", ""},
	} {
		commands = append(commands, &urfave.Command{Name: c[0], Usage: c[1], ArgsUsage: c[2], Category: categoryCoordination, Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Name == "heartbeat" && cmd.Args().Len() != 0 {
				return writeUsage("heartbeat takes no arguments")
			}
			if cmd.Name != "heartbeat" && cmd.Args().Len() != 1 {
				return writeUsage(cmd.Name + " requires exactly one ID")
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			var result model.ClaimResult
			var label string
			if cmd.Name == "heartbeat" {
				result = engine.Heartbeat(ctx)
			} else {
				item, e := writeItem(ctx, cfg, cmd.Args().First())
				if e != nil {
					return e
				}
				label = refsFor(cfg).ref(item.Ref, item.Ref.Source) + " "
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
			text := label + string(result.Outcome)
			if !result.ExpiresAt.IsZero() {
				text += " until " + result.ExpiresAt.Local().Format(time.DateTime)
			}
			return writeLine(out, text)
		}})
	}
	return commands
}
