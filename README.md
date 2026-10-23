# backlog

`bl` is a keyboard-first terminal backlog for people and coding agents. Tasks are
Markdown files in Git; linked worktrees share one live queue.

![TUI demo](docs/demo.gif)

## Quickstart

Install a binary with the [user guide](docs/guide.md#install), or build from source
with [Development](docs/development.md#setup). Inside a Git repository, with `bl`
on PATH:

```sh
bl init
bl new 'Ship the first feature !high #release' --ac 'Feature works'
bl ls
bl show BL-1 --json
bl next --explain --json
```

Run `bl` in a terminal. `j`/`k` move, `enter` opens detail, `gt` switches views,
`5` opens the board, `/` filters, `n` creates, `?` shows keys, and `q` quits.
CLI commands return a versioned envelope with `--json`; `bl instructions workflow`
prints the installed version's agent protocol.

Native files, Backlog.md import, local Beads, CLI/MCP, the TUI, hooks, actions,
and optional Worklease coordination are available. Remote and external source
adapters are still being implemented; see [remote adapter status](docs/remote.md).

## Docs

- [User guide](docs/guide.md): install, import, TUI, agents, configuration, automation
- [Design](docs/design.md): decisions, architecture, budgets, milestones
- [Native format](docs/format.md)
- [Query language](docs/query.md)
- [CLI commands](docs/cli.md)
- [TUI and keymap](docs/tui.md)
- [Readiness and ranking](docs/readiness.md)
- [Backlog.md import](docs/import.md)
- [Beads](docs/beads.md)
- [Remote adapters](docs/remote.md)
- [MCP](docs/mcp.md)
- [Events](docs/events.md)
- [Hooks, actions, and agent handoff](docs/automation.md)
- [Performance measurements](docs/benchmarks.md)
- [TUI stack spike](docs/spikes/tui-stack.md)
- [Development and releases](docs/development.md)
- [Project config schema](docs/schemas/project-config.json)
- [User config schema](docs/schemas/user-config.json)
- [Demo tape](docs/demo.tape) and [recording instructions](docs/guide.md#demo)
