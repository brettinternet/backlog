# backlog

`bl` is a fast, keyboard-first terminal backlog for people and coding agents.

> [!NOTE]
> Pre-alpha. The design is settled and implementation is tracked in [`backlog/`](backlog/). Nothing is released yet.

```text
 backlog · project · Next 12  Active 3  Mine 5  All 318  Board          tasks ✓ synced
  ID     P  Status       Title                                   ⊘   ▣    Claim
  BL-12  ▲  To Do        Debounce detail rendering while…        ─   ─    ─
> BL-14  ▲  To Do        Board layout with column moves          1   ─    mine
  BL-15  ■  In Progress  Go-to-item fuzzy finder                 ─   2/3  agent@mbp
```

- **Instant.** The cached view paints right away and sources sync behind it. Holding `j` through 50,000 items stays smooth.
- **One TUI.** List, board, and tree layouts, saved views, a detail pane, a fuzzy go-to, and a command palette, all in one program.
- **Edit without leaving the list.** Single-key pickers, quick-add tokens (`!high @me #tui <BL-7`), `.` to repeat, undo, multi-select, and `$EDITOR` for long text.
- **Built for agents.** `bl next --claim --start --json`, short verbs, numeric IDs, stdin bodies, and an MCP server.
- **Git-native files.** One Markdown file per item, named by ID, with minimal diffs. Every worktree shares one live backlog.
- **Many backends.** Native files first, then Backlog.md, Beads, GitHub Issues, Linear, Jira Cloud, and external adapters.
- **Composable.** Hooks, actions, and an event stream connect it to [Worklease](https://github.com/brettinternet/worklease) claims and to any other tool.

## Try the bootstrap

```sh
mise install
mise exec task -- task init
task cli:build
bin/bl --help
bin/bl --json version
bin/bl config show
bin/bl                     # empty TUI shell; ? help, q quit
task ci
```

The tooling, CLI entry point, project discovery, layered settings, and Bubble Tea v2 shell work.
Task storage, queries, and edits are still planned—not implemented.
Bare `bl` prints help when stdin or stdout is not a terminal. `bl tui` requires both.

User settings live at `$XDG_CONFIG_HOME/backlog/config.yaml` (default
`~/.config/backlog/config.yaml`, including on macOS):

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/brettinternet/backlog/main/docs/schemas/user-config.json
ui:
  highContrast: false
```

Configuration layers are built-ins, user settings, `.config/backlog.yaml` at the
control root, user `projects.<id>` overrides, environment, then flags. Mappings
merge by key; lists replace. `BL_HIGH_CONTRAST=true` and `--high-contrast` override
`ui.highContrast`; explicit `--high-contrast=false` wins over both. `NO_COLOR=1`
disables styling. See the [project schema](docs/schemas/project-config.json) and
[configuration examples](docs/design.md#5-projects-configuration-and-trust).

`bl -C DIR config show --json` reports discovery, effective settings, per-value
provenance, and trust boundaries without creating files or changing cwd. Linked
worktrees share the primary checkout's control root; `root: checkout` opts out.
Origin URLs become stable project IDs; no origin, or a local-path or `file://` origin, means a host-local path identity.
Without project config, Backlog.md and Beads are detected read-only, not loaded as
writable sources. Privileged project entries remain untrusted and nothing runs
while loading configuration. Trust grants and diagnostics remain BL-29 work.
`--json`/`-j` returns a versioned success or error envelope, with structured error
`details`. Usage errors exit 2; see [the exit-code contract](docs/design.md#7-agent-interface)
for all stable reasons and codes.

The product sketch and capabilities above describe the target, not this bootstrap.

## Docs

- [Design](docs/design.md): goals, decision register, architecture, budgets, and milestones
- [Native format](docs/format.md)
- [Query language](docs/query.md)
- [TUI and keymap](docs/tui.md)
- [Development](docs/development.md)
