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

## Docs

- [Design](docs/design.md): goals, decision register, architecture, budgets, and milestones
- [Native format](docs/format.md)
- [Query language](docs/query.md)
- [TUI and keymap](docs/tui.md)
- [Development](docs/development.md)

## License

[MIT](LICENSE)
