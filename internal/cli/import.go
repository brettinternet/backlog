package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/brettinternet/backlog/internal/source/backlogmd"
	"github.com/brettinternet/backlog/internal/textutil"
	urfave "github.com/urfave/cli/v3"
)

func importCommands(directory *string, out io.Writer) []*urfave.Command {
	return []*urfave.Command{{Name: "import", Usage: "Import another tracker into a new native project", Category: categoryProject, Action: groupHelp("use bl import backlog-md [PATH] --to DIR"), Commands: []*urfave.Command{{Name: "backlog-md", Usage: "Import Backlog.md files", ArgsUsage: "[PATH]", Flags: []urfave.Flag{
		&urfave.StringFlag{Name: "to", Value: "backlog-import", Usage: "Create the project in `DIR`, which must be empty or absent"},
		&urfave.BoolFlag{Name: "dry-run", Usage: "Print the import plan without writing"},
	}, Action: func(ctx context.Context, cmd *urfave.Command) error {
		if cmd.Args().Len() > 1 {
			return &Error{Reason: "usage", Message: "backlog-md accepts at most one source path"}
		}
		input := cmd.Args().First()
		if input == "" {
			input = "."
		}
		if !filepath.IsAbs(input) {
			input = filepath.Join(*directory, input)
		}
		target := cmd.String("to")
		if target == "" {
			return &Error{Reason: "usage", Message: "--to must not be empty"}
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(*directory, target)
		}
		report, err := backlogmd.Import(ctx, input, target, cmd.Bool("dry-run"))
		if err != nil {
			return &Error{Reason: "rejected", Message: err.Error(), Data: report}
		}
		if cmd.Bool("json") {
			return success(out, "import backlog-md", report)
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, textutil.Sanitize(string(data)))
		return err
	}}}}}
}
