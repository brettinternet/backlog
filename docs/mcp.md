# MCP server

`bl mcp` serves the Model Context Protocol over stdio. Launch it from the
project directory; stdout carries only MCP frames. Config loads once at startup.

## Setup

pi, globally:

```sh
pi mcp add backlog --cwd /path/to/project -- /absolute/path/to/bl mcp
```

Add `--local` from the project root for a project-scoped `.pi/mcp.json` entry.
Check with `pi mcp list`.

Claude Code, from the repository root:

```sh
claude mcp add --transport stdio backlog -- /absolute/path/to/bl mcp
```

Or in `.mcp.json`:

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

The `bl://instructions` resource serves the `bl instructions` overview.

## Tools

| Tool | Arguments | CLI equivalent |
| --- | --- | --- |
| `next` | `limit` (0 or omitted: all), `claim`, `start`, `session`, `run` | `bl next`, `bl next --claim --start` |
| `query` | `query` | `bl ls` |
| `show` | `id` | `bl show` (adds `resource` with coordination) |
| `create` | `title`, and optional `status`, `type`, `priority`, `description`, `criteria`, `assignees`, `labels`, `milestone`, `due`, `parent`, `blocked_by`, `source` | `bl new` |
| `update` | `id`, `version`, and `fields`, `operation: archive\|unarchive`, or `operation: replace_section` with `section` and `text`; `session`, `run` | `bl set`, `bl archive`, `bl edit --section` |
| `append` | `id`, `version`, `kind` (`note`\|`comment`), `text`, `author` | `bl note`, `bl comment` |
| `criteria` | `id`, `version`, `operation` (`check`\|`uncheck`\|`add`\|`remove`), `index`, `text`, `evidence` | `bl ac` |
| `action` | `name`, `id`, `confirm` | `bl action` |
| `claim`, `release` | `id`, `session`, `run` | `bl claim`, `bl release` |
| `heartbeat` | `session`, `run` | `bl heartbeat` |

```json
{"name":"update","arguments":{"id":"12","version":"VERSION","fields":{"status":"in-progress","labels+":["bug"]}}}
{"name":"criteria","arguments":{"id":"12","version":"VERSION","operation":"check","index":1,"evidence":"go test ./... passed"}}
```

- `version` is optional on writes; a stale one returns `conflict` and writes
  nothing.
- `fields` takes `bl set` names. Suffix a list field with `+` or `-` to add or
  remove. `start` and `done` are status updates to the workflow statuses.
- Editable sections: `description`, `criteria`, `dod`, `plan`, `notes`,
  `summary`.
- References accept the same shorthand as `bl new`. `status: draft` creates an
  item that is never ready.
- Comment `author` defaults to `me.default`.
- Errors are tool results with `isError: true` and JSON `reason` and `message`.

## Claims

The server never takes a session, handle, or run from its environment or an
earlier call, because one server can outlive the agent loop that started it.
Pass `session` (required) and `run` (optional) on every call that can claim,
renew, or release: `next` with `claim`, `claim`, `release`, `heartbeat`, and a
status `update` while coordination is on.

| Problem | Result |
| --- | --- |
| No session | `session-required`, nothing written |
| Run doesn't verify for this caller and resource | `run-mismatch`, nothing written |

Behavior matches the [CLI](cli.md#coordination): claimed `next` resumes the
session's open item, replays a pending acquire, or walks one ranked snapshot,
skipping contention and stopping on an unknown outcome. Run-managed claims are
never acquired, renewed, or released. Results report claim and status outcomes
separately. A done update releases before returning. Renew with `heartbeat`
before half the TTL. Keep one session per loop and never switch sessions to get
past recovery. Unresolved or redacted [evidence](cli.md#provider-evidence-and-recovery)
never authorizes side effects.
