package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/brettinternet/backlog/internal/instructions"
	urfave "github.com/urfave/cli/v3"
)

func instructionsCommand(directory *string, out io.Writer) *urfave.Command {
	return &urfave.Command{
		Name: "instructions", Usage: "Print agent guidance for this version", ArgsUsage: "[TOPIC]", Category: categoryAutomation,
		Action: func(_ context.Context, cmd *urfave.Command) error {
			topic := cmd.Args().First()
			if topic == "" {
				topic = "overview"
			}
			markdown, ok := instructions.Read(topic)
			if !ok || cmd.Args().Len() > 1 {
				return &Error{Reason: "usage", Message: "use bl instructions [TOPIC]. " + instructions.List(),
					Details: map[string]any{"topics": instructions.Names()}}
			}
			// Guidance works outside a project, so project config is optional.
			if cfg, err := loadConfig(cmd, *directory); err == nil {
				markdown = instructions.WithCommitTrailer(topic, markdown, cfg.History.CommitTrailer)
			}
			if cmd.Bool("json") {
				return success(out, "instructions", struct {
					Topic    string   `json:"topic"`
					Topics   []string `json:"topics"`
					Markdown string   `json:"markdown"`
				}{topic, instructions.Names(), markdown})
			}
			if !cmd.Args().Present() {
				markdown = instructions.List() + "\n\n" + markdown
			}
			_, err := fmt.Fprint(out, markdown)
			return err
		},
	}
}
