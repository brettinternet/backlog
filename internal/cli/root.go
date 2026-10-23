// Package cli adapts bl operations to human and machine output.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	urfave "github.com/urfave/cli/v3"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/mcp"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/brettinternet/backlog/internal/tui"
	"github.com/brettinternet/backlog/internal/worklease"
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
	var directory string
	version := func(_ context.Context, cmd *urfave.Command) error {
		command = "version"
		if cmd.Bool("json") {
			return success(out, command, build)
		}
		_, err := fmt.Fprintf(out, "bl %s (%s)\n", build.Version, build.Commit)
		return err
	}
	showConfig := func(_ context.Context, cmd *urfave.Command) error {
		cfg, err := loadConfig(cmd, directory)
		if err != nil {
			return err
		}
		if cmd.Bool("json") {
			return success(out, "config show", struct {
				project.Config
				Directory string `json:"directory"`
			}{cfg, directory})
		}
		_, err = fmt.Fprintf(out, "Directory: %s\nControl root: %s\nProject: %s (host-local: %t)\nUser config: %s\nState: %s\nCache: %s\nui.highContrast: %t (%s)\n",
			directory, cfg.Discovery.ControlRoot, cfg.Project.ID, cfg.HostLocal, cfg.Paths.ConfigFile, cfg.Paths.StateDir, cfg.Paths.CacheDir, cfg.UI.HighContrast, cfg.Provenance["ui.highContrast"].Layer)
		if err != nil {
			return err
		}
		settings, err := json.MarshalIndent(cfg.Settings, "", "  ")
		if err != nil {
			return err
		}
		if err := writeLine(out, string(settings)); err != nil {
			return err
		}
		keys := make([]string, 0, len(cfg.Provenance))
		for key := range cfg.Provenance {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			p := cfg.Provenance[key]
			if err := writeLine(out, fmt.Sprintf("%s: %s (%s:%d)", key, p.Layer, p.File, p.Line)); err != nil {
				return err
			}
		}
		for _, entry := range cfg.Privileged {
			if err := writeLine(out, fmt.Sprintf("%s trusted: %t", entry.Key, entry.Trusted)); err != nil {
				return err
			}
		}
		for _, warning := range cfg.Discovery.Warnings {
			fmt.Fprintf(errOut, "bl: warning: %s\n", warning)
		}
		for _, source := range cfg.Discovery.Detected {
			fmt.Fprintf(out, "Detected %s: %s (read-only; use bl import or an explicit source)\n", source.Type, source.Path)
		}
		return nil
	}
	openTUI := func(ctx context.Context, cmd *urfave.Command) error {
		if cmd.Bool("json") {
			return &Error{Reason: "usage", Message: "the TUI does not support --json; use bl --json version or bl --json config show"}
		}
		if !terminal(in) || !terminal(out) {
			return &Error{Reason: "unavailable", Message: "the TUI requires terminal stdin and stdout; use bl --help"}
		}
		cfg, err := loadBrowseConfig(cmd, directory, true)
		if err != nil {
			return err
		}
		if err := tui.ValidateKeys(cfg.Keys, cfg.Actions...); err != nil {
			return &Error{Reason: "invalid-config", Message: err.Error()}
		}
		defer tui.ConfigureRuntime()()
		session, cancel := context.WithCancel(engine.WithEventOrigin(ctx, "tui"))
		defer cancel()
		store, err := engine.OpenProjectStore(session, cfg)
		if err != nil {
			return &Error{Reason: "unavailable", Message: err.Error()}
		}
		defer store.Close()
		projector := engine.NewProjector(session, cfg)
		defer projector.Close()
		snapshots, unsubscribe := store.Subscribe()
		defer unsubscribe()
		sources := make([]string, 0, len(cfg.Sources))
		for _, s := range cfg.Sources {
			sources = append(sources, s.ID)
		}
		restored, warning := tui.LoadSession(cfg)
		createSource := ""
		if len(cfg.Sources) == 1 {
			createSource = cfg.Sources[0].ID
		}
		trustWarning := ""
		if len(cfg.Untrusted()) > 0 {
			trustWarning = "Untrusted project config · bl trust --show"
		}
		workspaceScope := cmd.String("workspace") != ""
		if cfg.Workspace != nil {
			discovery, _ := project.Discover(directory)
			workspaceScope = workspaceScope || discovery.CheckoutRoot == ""
		}
		app := tui.New(tui.Options{
			Workspace: &cfg, WorkspaceScope: workspaceScope,
			Automation:   &cfg,
			TrustWarning: trustWarning,
			CreateSource: createSource, DefaultType: cfg.Defaults.Type, DefaultDoD: cfg.Defaults.DOD,
			ValidateItem: func(item model.Item) error {
				p, _ := cfg.SourceProject(item.Ref.Source)
				return engine.ValidateItem(p, item)
			},
			ResolveRef: func(ctx context.Context, source string, ref model.Ref) (model.Ref, error) {
				return ResolveRef(ctx, cfg, source, ref)
			},
			NativeBytes: func(_ context.Context, item model.Item) ([]byte, error) {
				p, local := cfg.SourceProject(item.Ref.Source)
				item.Ref.Source = local
				return readNativeBytes(p, item)
			},
			NativeSource: func(id string) bool {
				for _, s := range cfg.Sources {
					if s.ID == id {
						return s.Type == "native"
					}
				}
				return false
			},
			EditorDir: directory, CommentAuthor: cfg.Me.Default,
			Views: cfg.SavedViews, Session: restored, Warning: warning, Keys: cfg.Keys,
			GoToItems: func(ctx context.Context) ([]tui.GoToItem, error) { return engine.GoToItems(ctx, cfg) },
			SaveView: func(v project.ViewConfig, shared bool) (project.SavedView, error) {
				path, scope := cfg.Paths.ConfigFile, "user"
				if shared {
					if cfg.Discovery.ControlRoot == "" {
						return project.SavedView{}, fmt.Errorf("project config requires a repository")
					}
					path, scope = filepath.Join(cfg.Discovery.ControlRoot, ".config", "backlog.yaml"), "project"
				}
				err := project.SaveView(path, !shared, v)
				return project.SavedView{ID: scope + ":" + v.Name, ViewConfig: v}, err
			},
			Context: session, Store: store, Projector: projector, Sources: sources, Project: cfg.Project.ID,
			Statuses: cfg.Statuses, Workflow: cfg.Workflow, Labels: cfg.Labels, Milestones: cfg.Milestones, Me: cfg.Me.Default,
			Read: func(readCtx context.Context, ref model.Ref) (model.Item, error) {
				return engine.ReadItem(readCtx, cfg, ref)
			},
			HighContrast: cfg.UI.HighContrast,
			NoColor:      os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb",
		})
		app.Bind(snapshots)
		final, runErr := tea.NewProgram(app, tea.WithContext(session), tea.WithInput(in), tea.WithOutput(out), tea.WithFPS(120)).Run()
		err = runErr
		if result, ok := final.(tui.Model); ok {
			if saveErr := result.SaveSession(cfg); saveErr != nil {
				fmt.Fprintf(errOut, "bl: warning: UI state not saved: %v\n", saveErr)
			}
		}
		cancel()
		if ctx.Err() != nil || errors.Is(err, tea.ErrInterrupted) {
			return nil
		}
		return err
	}
	root := &urfave.Command{
		Name: "bl", Usage: "A terminal backlog for people and coding agents",
		Description: "Read items with ls, show, and next.\nWithout a command, open the TUI on a terminal; otherwise print help.",
		Reader:      in, Writer: out, ErrWriter: errOut,
		ExitErrHandler: func(context.Context, *urfave.Command, error) {},
		Flags: []urfave.Flag{
			workspaceFlag(),
			&urfave.StringFlag{Name: "session", Usage: "Coordination session (BL_SESSION or WORKLEASE_SESSION_ID)"},
			&urfave.StringFlag{Name: "C", Value: ".", Usage: "Resolve the invocation from directory DIR"},
			&urfave.BoolFlag{Name: "json", Aliases: []string{"j"}, Usage: "Print a versioned JSON envelope"},
			&urfave.BoolFlag{Name: "version", Aliases: []string{"v"}, Usage: "Print version and commit"},
			&urfave.BoolFlag{Name: "high-contrast", Usage: "Use bold and underline instead of color (env: BL_HIGH_CONTRAST)"},
		},
		Before: func(_ context.Context, cmd *urfave.Command) (context.Context, error) {
			requested := cmd.String("C")
			resolved, err := filepath.Abs(requested)
			if err == nil {
				var info os.FileInfo
				info, err = os.Stat(resolved)
				if err == nil && !info.IsDir() {
					err = fmt.Errorf("not a directory")
				}
			}
			if err != nil {
				return nil, &Error{Reason: "usage", Message: fmt.Sprintf("-C %q: %v", requested, err),
					Details: map[string]any{"flag": "C", "path": requested}}
			}
			directory = resolved
			return engine.WithEventCWD(ctx, resolved), nil
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
			{Name: "mcp", Usage: "Serve MCP tools over stdio", Action: func(ctx context.Context, cmd *urfave.Command) error {
				cfg, err := loadConfig(cmd, directory)
				if err != nil {
					return err
				}
				return mcp.ServeStreams(ctx, cfg, in, out)
			}},
			{Name: "tui", Usage: "Browse the backlog (requires a terminal)", Flags: []urfave.Flag{workspaceFlag()}, Action: openTUI},
			{Name: "config", Usage: "Inspect layered project settings",
				Action: func(_ context.Context, _ *urfave.Command) error {
					return &Error{Reason: "usage", Message: "use bl config show"}
				},
				Commands: []*urfave.Command{{Name: "show", Usage: "Show effective settings, provenance, discovery, and XDG paths without creating files", Action: showConfig}},
			},
		},
	}
	root.Commands = append(root.Commands, claimCommands(&directory, out)...)
	root.Commands = append(root.Commands, diagnosticCommands(&directory, out)...)
	root.Commands = append(root.Commands, eventsCommand(&directory, out))
	root.Commands = append(root.Commands, hooksCommand(&directory), actionCommand(&directory, in, out, errOut))
	root.Commands = append(root.Commands, instructionsCommand(out))
	root.Commands = append(root.Commands, readCommands(&directory, out, errOut)...)
	root.Commands = append(root.Commands, writeCommands(&directory, in, out, errOut)...)
	root.Commands = append(root.Commands, maintenanceCommands(&directory, out, errOut)...)
	root.Commands = append(root.Commands, importCommands(&directory, out)...)
	valueFlags := map[string]bool{}
	_ = root.Walk(func(cmd *urfave.Command) error {
		for _, flag := range cmd.Flags {
			if f, ok := flag.(urfave.DocGenerationFlag); ok && f.TakesValue() {
				for _, name := range flag.Names() {
					valueFlags[name] = true
				}
			}
		}
		cmd.OnUsageError = func(_ context.Context, selected *urfave.Command, err error, _ bool) error {
			command = commandName(selected)
			return &Error{Reason: "usage", Message: err.Error()}
		}
		action := cmd.Action
		if action != nil {
			cmd.Action = func(ctx context.Context, selected *urfave.Command) error {
				command = commandName(selected)
				if selected.Args().Present() && !acceptsArguments(selected.Name) {
					return &Error{Reason: "usage", Message: fmt.Sprintf("unexpected argument %q; use bl --help", selected.Args().First()), Details: map[string]any{"argument": selected.Args().First()}}
				}
				if selected.Name != "mcp" && selected.Name != "version" && selected.Name != "instructions" {
					if cfg, err := loadConfig(selected, directory); err == nil {
						caller := model.CallerContext{Session: selected.String("session"), Run: os.Getenv("WORKLEASE_RUN_ID")}
						if caller.Session == "" {
							caller.Session = os.Getenv("BL_SESSION")
						}
						if caller.Session == "" {
							caller.Session = os.Getenv("WORKLEASE_SESSION_ID")
						}
						var coordinator model.Coordinator
						if cfg.Coordination.Worklease != nil {
							coordinator = worklease.New(cfg)
						}
						ctx = engine.WithClaimEvents(engine.WithCoordination(ctx, coordinator, caller), cfg)
					}
				}
				return action(engine.WithEventOrigin(ctx, "cli"), selected)
			}
		}
		return nil
	})
	if err := root.Run(ctx, args); err != nil {
		var failure *Error
		if !errors.As(err, &failure) {
			failure = &Error{Reason: "internal", Message: err.Error()}
		}
		if jsonRequested(args, valueFlags) {
			if err := json.NewEncoder(out).Encode(Envelope{SchemaVersion: 1, Command: command, Data: failure.Data, Error: failure}); err != nil {
				fmt.Fprintf(errOut, "bl: write JSON: %v\n", err)
				return 1
			}
		} else {
			fmt.Fprintf(errOut, "bl: %s\n", textutil.Line(failure.Message))
			if failure.Reason == "usage" {
				fmt.Fprintln(errOut, "Run bl --help for usage.")
			}
		}
		return failure.ExitCode()
	}
	return 0
}

func loadConfig(cmd *urfave.Command, directory string) (project.Config, error) {
	paths, err := project.UserPaths()
	if err != nil {
		return project.Config{}, &Error{Reason: "invalid-config", Message: err.Error()}
	}
	var contrast *bool
	if cmd.IsSet("high-contrast") {
		value := cmd.Bool("high-contrast")
		contrast = &value
	}
	cfg, err := project.Resolve(paths, directory, contrast)
	if err != nil {
		return project.Config{}, &Error{Reason: "invalid-config", Message: err.Error()}
	}
	for i, hook := range cfg.Hooks {
		if hook.If == "" {
			continue
		}
		if _, err := query.Parse(hook.If, query.Options{Hook: true}); err != nil {
			return project.Config{}, &Error{Reason: "invalid-config", Message: fmt.Sprintf("hooks[%d].if: %v", i, err)}
		}
	}
	return cfg, nil
}

func terminal(stream any) bool {
	file, ok := stream.(*os.File)
	return ok && term.IsTerminal(file.Fd())
}

func acceptsArguments(name string) bool {
	switch name {
	case "claim", "release", "events", "run", "action", "instructions", "ls", "show", "next", "new", "set", "start", "done", "note", "comment", "ac", "edit", "archive", "unarchive", "renumber", "backlog-md":
		return true
	}
	return false
}

func commandName(cmd *urfave.Command) string {
	return strings.TrimPrefix(cmd.FullName(), "bl ")
}

// Inspect output flags even when parsing fails before reaching --json.
// Skip values of non-boolean flags and positional data after "--".
func jsonRequested(args []string, valueFlags map[string]bool) bool {
	if len(args) < 2 {
		return false
	}
	value := false
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		name, raw, hasValue := strings.Cut(arg, "=")
		if strings.HasPrefix(name, "-") && valueFlags[strings.TrimLeft(name, "-")] && !hasValue {
			i++
			continue
		}
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
