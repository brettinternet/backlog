# MCP server

`bl mcp` serves the Model Context Protocol over stdio. Configure the client to
launch the `bl` binary from the project directory; stdout is reserved for MCP
frames. Project and user configuration are resolved once when the process starts.
Caller ownership is request-scoped: the server never reads session, handle, or run
selectors from its environment or retains them from a previous call.

## Tools

All writes use the item `version` returned by `show` as an optional optimistic
concurrency guard. If it is stale, the tool returns a `conflict` reason and does
not write. Errors are tool results with `isError: true` and JSON text containing
`reason` and `message`.

- `next` accepts `limit` (zero or omitted means all) and returns ready items in
  readiness order. Optional `claim` and `start` select/claim one item and then
  start it; `start` requires `claim`. Claimed calls require explicit `session`
  and accept optional `run`, with the same attempts, resume behavior, and
  separate claim/status outcomes as CLI `next --claim --start`.
- `query` accepts `query`, using the same expression language as `bl ls`.
- `show` accepts `id` and returns the complete item, including its version, plus
  `resource` when coordination is enabled.
- `create` accepts `title`, optional configured `status`, `type`, `priority`,
  `description`, `criteria`, `assignees`, `labels`, `milestone`, `due`, `parent`,
  `blocked_by`, and `source`. Reference values use the same numeric shorthand as
  `bl new`; use `status: draft` to create an open item that is not ready.
- `update` accepts `id`, `version`, and either `fields`, a mapping of field names
  and values accepted by `bl set`, or `operation: archive|unarchive`. List fields
  use suffixes `+` and `-` to add/remove values. Use `operation: replace_section`
  with an editable `section` (`description`, `criteria`, `dod`, `plan`, `notes`,
  or `summary`) and replacement `text` to edit content. `start` and `done` are
  status field updates, matching the configured CLI workflow statuses. When
  coordination is on, a status update requires `session` and accepts optional
  `run`; ownership is validated before writing. Results report separate `transition`
  claim/status outcomes, including a retained acquisition or failed release.
- `append` accepts `id`, `version`, `kind` (`note` or `comment`), `text`, and
  optional `author` for comments (otherwise `me.default` is used).
- `criteria` accepts `id`, `version`, and `operation` (`check`, `uncheck`,
  `add`, or `remove`), with `index` for check/uncheck/remove and `text` for add;
  `evidence` may accompany check.

- `claim` and `release` accept `id`, required `session`, and optional `run`.
- `heartbeat` accepts required `session` and optional `run`, returning `expiresAt`.

A run is verified through the coordinator for the exact caller and resource.
Run-managed claims are never acquired, renewed, or released. A missing session
returns `session-required`; mismatched run evidence returns `run-mismatch` before
any write or release. A caller-managed session cannot borrow the server's run.
Plain `next` does not claim. Claimed selection checks the session first, resumes
held open items, replays pending acquire exactly, and only walks one ranked
snapshot when the session is confirmed empty. Confirmed contention skips an item;
unknown-outcome stops immediately without trying another. Closed held items,
missing items, or unresolved ownership refuse fresh selection. Keep the same
stable distinct session for the loop; never switch sessions to bypass recovery.
A verified run returns only its item without a walk. Renew caller-managed claims
with heartbeat before half TTL. A done status update completes matching release
before returning, permitting the same session's next selection. The [provider-evidence requirements](cli.md#provider-evidence-and-recovery)
also apply to MCP; unresolved/redacted evidence never authorizes side effects.

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
