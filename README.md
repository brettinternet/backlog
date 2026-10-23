<p align="center">
  <img width="128" src="assets/app-icon.png" alt="backlog app icon" style="padding:0.5rem;">
</p>

<h1 align="center">backlog</h1>

`bl` is a keyboard-first terminal backlog for people and coding agents. Tasks are
Markdown files in Git, and every worktree of a repository shares one live queue.

![TUI demo](docs/demo.gif)

## Quickstart

[Install `bl`](docs/guide.md#install), then run this inside a Git repository:

```sh
bl init
bl new 'Ship the first feature !high #release' --ac 'Feature works'
bl ls
bl show BL-1 --json
bl next --explain --json
```

Run `bl` to open the TUI: `j`/`k` move, `enter` opens, `/` filters, `n` creates,
`?` lists every key, `q` quits. Every command accepts `--json`, and
`bl instructions workflow` prints the agent loop for the installed version.

## Sources

| Source | Support |
| --- | --- |
| [Native files](docs/format.md) | Full read and write |
| [Backlog.md](docs/backlog-md.md) | Edit in place, or [import](docs/import.md) |
| [Beads](docs/beads.md) | Local checkout through `bd` 1.3.0 |
| [GitHub Issues and Projects v2](docs/github.md) | Single-field edits and comments |
| [Linear](docs/linear.md) | Single-field edits and comments |
| [External adapters](docs/external-adapter-protocol.md) | JSON-RPC over stdio |
| [Jira Cloud](docs/jira.md) | Discovered transitions, confirmed single-assignee replacement and ADF comments |

Around the backlog: [MCP](docs/mcp.md), [hooks and actions](docs/automation.md),
an [event stream](docs/events.md), and optional Worklease claims.

## Docs

Using `bl`:

- [User guide](docs/guide.md): install, import, TUI, configuration, agents, automation
- [CLI](docs/cli.md)
- [TUI and keymap](docs/tui.md)
- [Query language](docs/query.md)
- [Readiness and ranking](docs/readiness.md)
- [MCP](docs/mcp.md)
- [Hooks, actions, and agent handoff](docs/automation.md)
- [Events](docs/events.md)
- Config schemas: [project](docs/schemas/project-config.json), [user](docs/schemas/user-config.json)

Sources:

- [Native format](docs/format.md)
- [Backlog.md source](docs/backlog-md.md) and [import](docs/import.md)
- [Beads](docs/beads.md)
- [Remote sources](docs/remote.md) and [OAuth](docs/oauth.md)
- [GitHub](docs/github.md) ([live verification](docs/github-verification.md))
- [Linear](docs/linear.md) ([live verification](docs/linear-verification.md))
- [Jira Cloud](docs/jira.md) ([API probe](docs/providers/jira.md))
- [External adapter protocol v1](docs/external-adapter-protocol.md) ([schema](docs/external-adapter/v1/schema.json))

Project:

- [Design](docs/design.md): decisions, architecture, budgets
- [Development and releases](docs/development.md)
- [Benchmarks](docs/benchmarks.md)
- [TUI stack spike](docs/spikes/tui-stack.md)
- [Demo tape](docs/demo.tape) ([recording](docs/guide.md#demo))
