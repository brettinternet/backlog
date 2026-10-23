package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

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
		return printImport(out, *directory, report)
	}}}}}
}

// printImport summarizes the report; --json carries every field mapping and decision.
func printImport(out io.Writer, directory string, report backlogmd.Report) error {
	verb := "Imported"
	if report.DryRun {
		verb = "Would import"
	}
	target := report.Target
	if relative, err := filepath.Rel(directory, target); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		target = relative
	}
	counts := report.Counts
	lines := []string{fmt.Sprintf("%s %d item(s) (%d draft(s), %d archived) and %d milestone(s) into %s", verb, counts.Tasks, counts.Drafts, counts.Archived, counts.Milestones, target)}
	for _, file := range report.Files {
		output := file.OutputPath
		if file.Kind == "milestone" {
			output += " " + file.ID
		}
		lines = append(lines, fmt.Sprintf("  %s → %s", file.SourcePath, output))
	}
	for _, skipped := range report.Skipped {
		path := skipped.Path
		if relative, err := filepath.Rel(report.Input, path); err == nil && filepath.IsAbs(path) {
			path = filepath.ToSlash(relative)
		}
		if skipped.Field != "" {
			path += " " + skipped.Field
		}
		lines = append(lines, fmt.Sprintf("skipped %s: %s", path, skipped.Reason))
	}
	for _, milestone := range report.UnresolvedMilestones {
		lines = append(lines, fmt.Sprintf("unresolved milestone %s: %q (%s)", milestone.TaskID, milestone.Value, milestone.SourcePath))
	}
	for _, line := range lines {
		// Paths, IDs, and milestone values come from the imported files.
		if _, err := fmt.Fprintln(out, textutil.Line(line)); err != nil {
			return err
		}
	}
	return nil
}
