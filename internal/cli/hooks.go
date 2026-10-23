package cli

import (
	"context"
	"fmt"

	urfave "github.com/urfave/cli/v3"

	"github.com/brettinternet/backlog/internal/hooks"
)

func hooksCommand(directory *string) *urfave.Command {
	return &urfave.Command{Name: "hooks", Usage: "Run event hooks", Hidden: true, Commands: []*urfave.Command{{
		Name: "run", Usage: "Run hooks for appended event IDs", ArgsUsage: "EVENT_ID...",
		Action: func(ctx context.Context, cmd *urfave.Command) error {
			ids := cmd.Args().Slice()
			if len(ids) == 0 {
				return writeUsage("hooks run requires at least one event ID")
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			if err := hooks.RunEvents(ctx, cfg, ids, *directory); err != nil {
				return &Error{Reason: "unavailable", Message: fmt.Sprintf("hook runner: %v", err)}
			}
			return nil
		},
	}, {
		Name: "action", Usage: "Execute a prepared detached action", ArgsUsage: "HANDOFF",
		Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Args().Len() != 1 {
				return writeUsage("hooks action requires a handoff")
			}
			invocation, err := hooks.ReadActionInvocation(cmd.Args().First())
			if err != nil {
				return err
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			_, err = hooks.RunAction(ctx, cfg, invocation, nil, nil)
			return err
		},
	}}}
}
