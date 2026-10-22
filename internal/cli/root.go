// Package cli adapts bl operations to human and machine output.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	urfave "github.com/urfave/cli/v3"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/tui"
)

type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Go      string `json:"go"`
}

// Run owns streams and exit codes; the framework must never exit the process.
// Help and version do not load configuration.
func Run(ctx context.Context, args []string, build BuildInfo, in io.Reader, out, errOut io.Writer) int {
	build.Go = runtime.Version()
	command := "bl"
	version := func(_ context.Context, cmd *urfave.Command) error {
		command = "version"
		if cmd.Bool("json") {
			return success(out, command, build)
		}
		_, err := fmt.Fprintf(out, "bl %s (%s)\n", build.Version, build.Commit)
		return err
	}
	showConfig := func(_ context.Context, cmd *urfave.Command) error {
		cfg, err := loadConfig(cmd)
		if err != nil {
			return err
		}
		if cmd.Bool("json") {
			return success(out, "config show", cfg)
		}
		_, err = fmt.Fprintf(out, "User config: %s\nState: %s\nCache: %s\nui.highContrast: %t\n",
			cfg.Paths.ConfigFile, cfg.Paths.StateDir, cfg.Paths.CacheDir, cfg.UI.HighContrast)
		return err
	}
	openTUI := func(ctx context.Context, cmd *urfave.Command) error {
		if cmd.Bool("json") {
			return &Error{Reason: "usage", Message: "the TUI does not support --json; use bl --json version or bl --json config show"}
		}
		if !terminal(in) || !terminal(out) {
			return &Error{Reason: "unavailable", Message: "the TUI requires terminal stdin and stdout; use bl --help"}
		}
		cfg, err := loadConfig(cmd)
		if err != nil {
			return err
		}
		model := tui.New(tui.Options{
			HighContrast: cfg.UI.HighContrast,
			NoColor:      os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb",
		})
		_, err = tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run()
		if ctx.Err() != nil || errors.Is(err, tea.ErrInterrupted) {
			return nil
		}
		return err
	}
	root := &urfave.Command{
		Name: "bl", Usage: "A terminal backlog for people and coding agents",
		Description: "Bootstrap build: configuration and an empty TUI shell. Task operations are not implemented yet.\nWithout a command, open the TUI on a terminal; otherwise print help.",
		Reader:      in, Writer: out, ErrWriter: errOut,
		ExitErrHandler: func(context.Context, *urfave.Command, error) {},
		Flags: []urfave.Flag{
			&urfave.BoolFlag{Name: "json", Aliases: []string{"j"}, Usage: "Print a versioned JSON envelope"},
			&urfave.BoolFlag{Name: "version", Aliases: []string{"v"}, Usage: "Print version and commit"},
			&urfave.BoolFlag{Name: "high-contrast", Usage: "Use bold and underline instead of color (env: BL_HIGH_CONTRAST)"},
		},
		Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Bool("version") {
				return version(ctx, cmd)
			}
			if !cmd.Bool("json") && (!terminal(in) || !terminal(out)) {
				return urfave.ShowAppHelp(cmd)
			}
			return openTUI(ctx, cmd)
		},
		Commands: []*urfave.Command{
			{Name: "version", Usage: "Print build information", Action: version},
			{Name: "tui", Usage: "Open the empty TUI shell (requires a terminal)", Action: openTUI},
			{Name: "config", Usage: "Inspect bootstrap user settings (no project layers yet)",
				Action: func(_ context.Context, _ *urfave.Command) error {
					return &Error{Reason: "usage", Message: "use bl config show"}
				},
				Commands: []*urfave.Command{{Name: "show", Usage: "Show user settings and XDG paths without creating files", Action: showConfig}},
			},
		},
	}
	_ = root.Walk(func(cmd *urfave.Command) error {
		cmd.OnUsageError = func(_ context.Context, selected *urfave.Command, err error, _ bool) error {
			command = commandName(selected)
			return &Error{Reason: "usage", Message: err.Error()}
		}
		action := cmd.Action
		if action != nil {
			cmd.Action = func(ctx context.Context, selected *urfave.Command) error {
				command = commandName(selected)
				if selected.Args().Present() {
					return &Error{Reason: "usage", Message: fmt.Sprintf("unexpected argument %q; use bl --help", selected.Args().First())}
				}
				return action(ctx, selected)
			}
		}
		return nil
	})
	if err := root.Run(ctx, args); err != nil {
		var failure *Error
		if !errors.As(err, &failure) {
			failure = &Error{Reason: "internal", Message: err.Error()}
		}
		if jsonRequested(args) {
			if err := json.NewEncoder(out).Encode(Envelope{SchemaVersion: 1, Command: command, Error: failure}); err != nil {
				fmt.Fprintf(errOut, "bl: write JSON: %v\n", err)
				return 1
			}
		} else {
			fmt.Fprintf(errOut, "bl: %s\n", failure.Message)
			if failure.Reason == "usage" {
				fmt.Fprintln(errOut, "Run bl --help for usage.")
			}
		}
		return failure.ExitCode()
	}
	return 0
}

func loadConfig(cmd *urfave.Command) (project.Config, error) {
	paths, err := project.UserPaths()
	if err != nil {
		return project.Config{}, &Error{Reason: "invalid-config", Message: err.Error()}
	}
	var contrast *bool
	if cmd.IsSet("high-contrast") {
		value := cmd.Bool("high-contrast")
		contrast = &value
	} else if raw := os.Getenv("BL_HIGH_CONTRAST"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return project.Config{}, &Error{Reason: "invalid-config", Message: "BL_HIGH_CONTRAST must be a boolean (true or false)"}
		}
		contrast = &value
	}
	cfg, err := project.Load(paths, contrast)
	if err != nil {
		return project.Config{}, &Error{Reason: "invalid-config", Message: err.Error()}
	}
	return cfg, nil
}

func terminal(stream any) bool {
	file, ok := stream.(*os.File)
	return ok && term.IsTerminal(file.Fd())
}

func commandName(cmd *urfave.Command) string {
	return strings.TrimPrefix(cmd.FullName(), "bl ")
}

// The bootstrap flags are all booleans. Inspect them even when parsing fails
// before reaching --json, but never interpret positional data after "--".
func jsonRequested(args []string) bool {
	if len(args) < 2 {
		return false
	}
	value := false
	for _, arg := range args[1:] {
		if arg == "--" {
			break
		}
		name, raw, hasValue := strings.Cut(arg, "=")
		if name == "--json" || name == "-j" {
			value = true
			if hasValue {
				// An invalid value is still a request for machine-readable errors.
				if parsed, err := strconv.ParseBool(raw); err == nil {
					value = parsed
				}
			}
		}
	}
	return value
}
