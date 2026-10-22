# MCP server

`bl mcp` serves the Model Context Protocol over stdio. Configure the client to
launch the `bl` binary from the project directory; stdout is reserved for MCP
frames. Project and user configuration are resolved once when the process starts.
Caller-specific claim context is intentionally not supported by this foundation
server; claim tools and request-scoped session/run arguments arrive in later work.

## Tools

All writes use the item `version` returned by `show` as an optional optimistic
concurrency guard. If it is stale, the tool returns a `conflict` reason and does
not write. Errors are tool results with `isError: true` and JSON text containing
`reason` and `message`.

- `next` accepts `limit` (zero or omitted means all) and returns ready items in
  readiness order.
- `query` accepts `query`, using the same expression language as `bl ls`.
- `show` accepts `id` and returns the complete item, including its version.
- `create` accepts `title`, optional configured `status`, `type`, `priority`,
  `description`, `criteria`, `assignees`, `labels`, `milestone`, `due`, `parent`,
  `blocked_by`, and `source`. Reference values use the same numeric shorthand as
  `bl new`; use `status: draft` to create an open item that is not ready.
- `update` accepts `id`, `version`, and either `fields`, a mapping of field names
  and values accepted by `bl set`, or `operation: archive|unarchive`. List fields
  use suffixes `+` and `-` to add/remove values. Use `operation: replace_section`
  with an editable `section` (`description`, `criteria`, `dod`, `plan`, `notes`,
  or `summary`) and replacement `text` to edit content. `start` and `done` are
  status field updates, matching the configured CLI workflow statuses.
- `append` accepts `id`, `version`, `kind` (`note` or `comment`), `text`, and
  optional `author` for comments (otherwise `me.default` is used).
- `criteria` accepts `id`, `version`, and `operation` (`check`, `uncheck`,
  `add`, or `remove`), with `index` for check/uncheck/remove and `text` for add;
  `evidence` may accompany check.

The `bl://instructions` resource publishes `bl instructions` overview guidance.

## pi

Register the server globally with pi:

```sh
pi mcp add backlog --cwd /path/to/project -- /absolute/path/to/bl mcp
```

Use `pi mcp add backlog --local ...` from the project root for a trusted,
project-scoped `.pi/mcp.json` entry. Check the connection with `pi mcp list`.

## Claude Code

Register the project-scoped server from the repository root:

```sh
claude mcp add --transport stdio backlog -- /absolute/path/to/bl mcp
```

Alternatively add this entry to `.mcp.json`:

```json
{
  "mcpServers": {
    "backlog": {
      "command": "/absolute/path/to/bl",
      "args": ["mcp"]
    }
  }
}
```
