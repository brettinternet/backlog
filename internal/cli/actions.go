package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	urfave "github.com/urfave/cli/v3"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/hooks"
)

func actionCommand(directory *string, in io.Reader, out, errOut io.Writer) *urfave.Command {
	return &urfave.Command{Name: "action", Usage: "Run a configured action on an item", ArgsUsage: "NAME ID", Category: categoryAutomation, Flags: []urfave.Flag{&urfave.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "Skip the confirmation prompt"}, &urfave.BoolFlag{Name: "dry-run", Usage: "Show what would run without running it"}}, Action: func(ctx context.Context, cmd *urfave.Command) error {
		if cmd.Args().Len() != 2 {
			return writeUsage("action requires NAME ID")
		}
		cfg, err := loadConfig(cmd, *directory)
		if err != nil {
			return err
		}
		ctx = engine.WithEventCWD(ctx, *directory)
		if cmd.Bool("dry-run") {
			preview, err := engine.PreviewAction(ctx, cfg, cmd.Args().Get(0), cmd.Args().Get(1))
			if err != nil {
				return writeError(err)
			}
			if cmd.Bool("json") {
				return success(out, "action", preview)
			}
			return writeLine(out, fmt.Sprintf("argv: %s\ncwd:  %s\nenv:  %s", quoteArgv(preview.Argv), preview.CWD, strings.Join(preview.Env, " ")))
		}
		choice, err := engine.PrepareAction(ctx, cfg, cmd.Args().Get(0), cmd.Args().Get(1), true)
		if err != nil {
			return writeError(err)
		}
		if choice.Confirm && !cmd.Bool("yes") {
			if !terminal(in) || cmd.Bool("json") {
				return &Error{Reason: "rejected", Message: "action requires --yes confirmation"}
			}
			fmt.Fprintf(errOut, "Run %s on %s? [y/N] ", choice.Name, choice.Invocation.Event.Ref)
			answer, _ := bufio.NewReader(in).ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				return &Error{Reason: "rejected", Message: "action cancelled"}
			}
		}
		var result hooks.ActionResult
		if choice.Detach {
			result, err = hooks.StartAction(cfg, choice.Invocation)
		} else {
			var output io.Writer
			if !cmd.Bool("json") {
				output = out
			}
			result, err = hooks.RunAction(ctx, cfg, choice.Invocation, output, errOut)
		}
		if err != nil {
			return writeError(err)
		}
		if !result.Detached && result.Event.Outcome != "ok" {
			return &Error{Reason: "rejected", Message: "action " + result.Event.Outcome + ": " + result.Message}
		}
		if cmd.Bool("json") {
			return success(out, "action", result)
		}
		return nil
	}}
}

// quoteArgv quotes only arguments that would not read back as one word.
func quoteArgv(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = arg
		if arg == "" || strings.ContainsAny(arg, " \t\n\"'\\$`*?[]{}()<>|&;#~") {
			quoted[i] = strconv.Quote(arg)
		}
	}
	return strings.Join(quoted, " ")
}
