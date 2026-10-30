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

The `bl://instructions` resource serves the `bl instructions` overview, followed
by the configured source IDs and types for `source:` filters.

## Tools

| Tool | Arguments | CLI equivalent |
| --- | --- | --- |
| `next` | `limit` (0 or omitted: all), `claim`, `start`, `session`, `run`, `agent`, `agentSession` | `bl next`, `bl next --claim --start` |
| `query` | `query` | `bl ls` |
| `show` | `id` | `bl show` (adds `resource` with coordination) |
| `create` | `title`, and optional `status`, `type`, `priority`, `description`, `criteria`, `assignees`, `labels`, `milestone`, `due`, `parent`, `blocked_by`, `source`, `sprint` | `bl new` |
| `update` | `id`, `version`, and `fields`, `operation: archive\|unarchive`, or `operation: replace_section` with `section` and `text`; `session`, `run`, `agent`, `agentSession` | `bl set`, `bl archive`, `bl edit --section` |
| `append` | `id`, `version`, `kind` (`note`\|`comment`\|`handoff`), `text`, `author`; `session`, `agent`, `agentSession` | `bl note`, `bl comment`, `bl handoff` |
| `criteria` | `id`, `version`, `operation` (`check`\|`uncheck`\|`add`\|`set`\|`remove`), `index`, `text`, `evidence`, `list` (`criteria`\|`dod`) | `bl ac` |
| `action` | `name`, `id`, `confirm`, `dryRun` | `bl action` |
| `events` | `since`, `query`, `limit` (0 or omitted: 100) | `bl events --since` (no follow) |
| `history` | `id`, `limit` (0 or omitted: all), `cursor` | `bl history` |
| `changes` | `id` | `bl changes` |
| `tour` | `id`, and `file` or `tour` to store, or `remove` and optional `change` | `bl tour`, `bl tour --remove` |
| `claim`, `release` | `id`, `session`, `run`, `agent`, `agentSession` | `bl claim`, `bl release` |
| `heartbeat` | `session`, `run` | `bl heartbeat` |

```json
{"name":"update","arguments":{"id":"12","version":"VERSION","fields":{"status":"in-progress","labels+":["bug"]}}}
{"name":"criteria","arguments":{"id":"12","version":"VERSION","operation":"check","index":1,"evidence":"go test ./... passed"}}
```

- `version` is optional on writes; a stale one returns `conflict` and writes
  nothing. The server runs one write at a time, so writes sent without
  `version` don't conflict with each other.
- `fields` takes `bl set` names. Suffix a list field with `+` or `-` to add or
  remove.
- `operation: "start"` or `"done"` sets the status `bl start` or `bl done`
  would, resolved per item source, and takes no `fields`. Under
  [`tours.mode`](changes.md#tours) `suggest`, a done result carries
  `tourRequest`; under `require`, done is refused until each matching change
  has a tour for its head.
- `criteria` changes acceptance criteria unless `list` is `dod`. `set` rewords
  the entry at `index` to `text`, keeping its check state and evidence.
- Editable sections: `description`, `criteria`, `dod`, `plan`, `notes`,
  `summary`.
- References accept the same shorthand as `bl new`. `status: draft` creates an
  item that is never ready.
- `create` picks its source like `bl new`: `source`, `defaults.source`, else
  the only source that can create.
- Comment `author` defaults to `me.default`. On GitHub, Linear, and Jira it is
  the source account's `user`, and any other `author` is `rejected`.
- Errors are tool results with `isError: true` and JSON `reason` and `message`.
  A `create` that leaves an item behind but fails a later step also returns
  `created`, the item's `source:ID`.

## Results

Results use the `data` shapes of the matching CLI `--json` command
([cli.md](cli.md)), without the envelope. `next` and `query` return `items`,
and write tools return `item`, each an item summary:

```json
{"item":{"source":"tasks","id":"BL-2","title":"Add sign-in page","status":"in-progress","category":"active","priority":"high","version":"VERSION"}}
```

`events` returns `events` like `bl events --json`, plus `cursor` and `more`.
Pass `cursor` back as `since` to continue: `more` means another page is ready,
otherwise poll later. An unknown `since` returns `not-found`.

`history` returns `entries`, `providers`, `cursor`, and `more`, like `bl
history --json`; read it to pick up earlier sessions' handoffs before starting.
An unknown `cursor` returns `not-found`.

`changes` returns `changes` and `providers`, like `bl changes --json`, so an
agent can see whether its pull request is open, green, and approved, which
checks fail, and how many review threads are unresolved, without calling the
forge itself.

`tour` stores a tour for the item's linked change whose head is its `ref` and
returns `item`, `change`, `ref`, and `steps`, like `bl tour --json`;
`bl instructions tours` describes the format. Pass a `file` path, or, when the
client doesn't share bl's filesystem, the tour object itself as `tour`, as in
`{"id": "BL-12", "tour": {"ref": "…", "steps": […]}}`; both go through the same
validator and 1 MiB limit, and giving both is refused. With neither it returns
`tours`, like `bl tour ID --json`. With `remove`, it deletes the tour stored
for `change`, or for the only linked change with one, and returns `item`,
`change`, and `removed`, like `bl tour ID --remove --json`.

`show` returns `item`, `metadata`, `sections`, `criteria`, `dod`, and, with
coordination, `resource`, `claims`, and `claimsError`, plus `sessions` with
their live [`contact`](cli.md#contact), like `bl show --json`. Edge targets in `metadata`
are always `source:id`. With coordination, a status `update` adds `transition`.

## Claims

The server never takes a session, handle, or run from its environment or an
earlier call, because one server can outlive the agent loop that started it.
Pass `session` (required) and `run` (optional) on every call that can claim,
renew, or release: `next` with `claim`, `claim`, `release`, `heartbeat`, and a
status `update` while coordination is on.

Events record the caller's session ([events.md](events.md#wire-format)). Pass
`agent` (your kind, such as `claude`) and `agentSession` (your harness session
ID; defaults to `session`) beside `session` on `next`, writes, `claim`, and
`release`. `create`, `append`, and `criteria` also accept `session`, `agent`, and
`agentSession` for their events.

`append` with `kind: "handoff"` records the session's handoff like
[`bl handoff`](cli.md#bl-handoff-id-text----session-s---if-version-v): it
requires `session` but no claim, refuses text over 4 KiB with `invalid-value`,
and returns `{event, comment?, item}`. `version` applies only under
`history.handoff: comment`.

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
