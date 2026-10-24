package cli

import (
	"context"
	"fmt"
	"strings"

	urfave "github.com/urfave/cli/v3"
)

// Help lists item commands first, then these categories in name order.
const (
	categoryAutomation   = "Automation"
	categoryCoordination = "Coordination"
	categoryProject      = "Project"
)

const lsHelp = `QUERY uses the query language (bl instructions query). Without an is: term,
ls lists open items.

Examples:
  bl ls status:todo label:bug
  bl ls is:closed sort:-updated -n 10`

const newHelp = `Tokens in TITLE set fields: !high priority, @me assignee, #bug label,
^BL-1 parent, <BL-2 blocker, ~m-1 milestone, and :feature type.

Examples:
  bl new 'Fix login !high #bug @me'
  bl new 'Write the guide' --status draft --ac 'Covers setup'`

const setHelp = `FIELD=VALUE sets a field, FIELD+=VALUE adds list entries, and FIELD-=VALUE
removes them. List values are comma-separated; an empty value clears a field.

Fields: title status type priority assignees labels milestone parent
blocked_by related duplicate_of rank due refs files

Examples:
  bl set 12 status=in-progress priority=high
  bl set 12 13 label+=bug blocked_by+=7`

const acHelp = `Criteria are numbered from 1. When the numbers come from an earlier read,
pass its version with --if-version so a changed list fails instead of
updating the wrong criterion. --dod targets definition-of-done items, which
are numbered separately.

Examples:
  bl ac 12 check 1 2
  bl ac 12 check 3 --evidence 'go test ./... passes'
  bl ac 12 add 'Docs updated'
  bl ac 12 rm 4
  bl ac 12 check 1 --dod --evidence 'task ci passed'`

func init() {
	// Match the capitalization of every other option.
	urfave.HelpFlag = &urfave.BoolFlag{Name: "help", Aliases: []string{"h"}, Usage: "Show help", HideDefault: true, Local: true}
	// Categories group the root listing; a command's own help need not repeat its group.
	const category = "{{if .Category}}\n\nCATEGORY:\n   {{.Category}}{{end}}"
	urfave.CommandHelpTemplate = strings.Replace(urfave.CommandHelpTemplate, category, "", 1)
	urfave.SubcommandHelpTemplate = strings.Replace(urfave.SubcommandHelpTemplate, category, "", 1)
}

// helpCommand keeps "bl help [COMMAND...]" working without listing itself,
// including for subcommands such as "bl help auth login".
func helpCommand() *urfave.Command {
	return &urfave.Command{Name: "help", Hidden: true, ArgsUsage: "[COMMAND...]", Action: func(ctx context.Context, cmd *urfave.Command) error {
		names := cmd.Args().Slice()
		parent := cmd.Root()
		for i, name := range names {
			if parent.Command(name) == nil {
				return &Error{Reason: "usage", Message: fmt.Sprintf("unknown command %q", strings.Join(names[:i+1], " "))}
			}
			if i == len(names)-1 {
				return urfave.ShowCommandHelp(ctx, parent, name)
			}
			parent = parent.Command(name)
		}
		return urfave.ShowAppHelp(parent)
	}}
}

// groupHelp shows a command group's subcommands when none is named; JSON
// callers get a usage error instead of help text.
func groupHelp(message string) urfave.ActionFunc {
	return func(_ context.Context, cmd *urfave.Command) error {
		if cmd.Bool("json") {
			return &Error{Reason: "usage", Message: message}
		}
		return urfave.ShowSubcommandHelp(cmd)
	}
}
