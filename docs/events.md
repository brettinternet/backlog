# Events

Every project has a durable JSONL event log. `bl events` reads it;
`--follow` also watches the sources, so no TUI or daemon is needed.

```sh
bl events
bl events --json --since 01K8JHW1J00000000000000000
bl events --follow --json 'changed:status'
bl events --follow 'changed:readiness is:ready'
```

Edits made while no `bl` process ran appear at the next load. The first load
records a baseline without inventing history.

## Wire format

```json
{"schemaVersion":1,"id":"01K8JHW1J00000000000000000","type":"item.updated","time":"2026-10-22T18:00:00Z","project":"github.com/example/project","source":"tasks","ref":"tasks:BL-12","changes":{"status":{"from":"todo","to":"done"}},"item":{"ref":{"source":"tasks","id":"BL-12"},"title":"Ship events","status":"done","category":"done","version":"new-content-hash"},"actor":"@brett","origin":"cli","cause":null,"depth":0,"versions":["tasks:BL-12","old-content-hash","new-content-hash","false","false","false"]}
```

(Other changed fields and item fields are elided.)

| Field | Contract |
| --- | --- |
| `schemaVersion` | `1` |
| `id` | ULID, strictly increasing in append order, even within a millisecond or after a clock rollback |
| `type`, `time` | Event type; append time in UTC |
| `project`, `source`, `ref` | Project ID, source ID, `source:ID` |
| `changes` | Field → `{from, to}`; readiness adds `reason` |
| `item` | The item after the change (lower camel case). For a removal, the last observed item. |
| `actor` | Configured default identity, if any. Not authenticated. |
| `origin` | `cli`, `tui`, `mcp`, `hook`, `action`, or `sync` |
| `cause`, `depth` | Causing event ID (or `null`) and chain depth |
| `versions` | The identity used for [deduplication](#deduplication) |

`changes` uses native names (`blocked_by`, `duplicate_of`) and covers body
sections, evidence inside `criteria`/`dod`, `updated`, and raw frontmatter under
`fields`. It leaves out the version and derived inverse edges.

One successful command appends one mutation event, however many fields it
changes. A successful no-op still appends one, with no changes and a unique
nonce. A failed mutation appends none.

## Types

| Type | When |
| --- | --- |
| `item.created` | A create succeeds, or sync finds a new item after the baseline |
| `item.updated` | An edit succeeds, an item is unarchived (`changes.archived: {from: true, to: false}`), or sync sees changed content |
| `item.archived` | An item is archived (`changes.archived`), or a complete snapshot no longer has it (`changes.present: {from: true, to: false}`). Editing an archived item is `item.updated`. |
| `item.readiness` | Item data changes readiness. `changes.readiness` has `from`, `to`, and `reason` (including `candidate` on becoming ready). |
| `claim.acquired`, `claim.released` | A claim operation through `bl` succeeds |
| `hook.finished`, `action.finished` | A hook or action ends. Adds `name`, `outcome` (`ok`, `failed`, `timeout`, `refused`), `exitCode`, `duration` (ns), optional `result` (≤ 8 KiB), and, unless `ok`, optional `message`: the refusal or exec error, or for a nonzero exit or timeout the last lines of stderr, ≤ 1 KiB with terminal controls stripped. `message` is display text, not a contract for parsing. |

Readiness is checked for the changed item, its transitive dependents, and its old
and new parents. Only real transitions emit: a dependent still blocked by
something else emits nothing. Claims never trigger readiness events.

Partial coverage never implies deletion or replaces the baseline. Valid items
stay readable and writable when another file is malformed.

## Deduplication

The log is the only dedupe authority. Under a cross-process project lock, a
producer appends an event only if no event with the same `(type, ref,
versions)` exists. For mutations, `versions` is the ref, old and new content
versions, old and new archive flags, and a deletion flag. For readiness, it's
the transitions that caused it, so different blockers changing one dependent
don't collapse.

Only the process that appended an event runs its hooks. Two TUIs and a follower
seeing one edit produce one mutation event and one readiness event per
transition, not three.

A disposable SQLite index speeds lookups. Each transaction first indexes any
complete log lines it hasn't seen, recovering from a crash between append and
index. Deleting the index rebuilds it from the log. The per-item observation
baseline lives in the XDG cache under the same lock; deleting it starts a fresh
baseline at the next full load. Unchanged native cache revisions skip baseline
scans, and none of this runs on the Bubble Tea update loop.

## Cause and depth

Commands and sync start at `cause: null`, `depth: 0`. A hook or action
subprocess inherits `BL_EVENT_ID` and `BL_HOOK_DEPTH`, so its writes name that
cause at depth + 1, and `BL_EVENT_ORIGIN` marks the origin as `hook` or
`action`. Sync never inherits a chain. Events deeper than 3 are recorded, but no
hook runs for them. Nothing retries.

## Storage and replay

Logs live in `~/.local/state/backlog/events/<sha256(project)>/`
(`$XDG_STATE_HOME`). `current.jsonl` rotates at 8 MiB. One event may be up to
16 MiB and never splits across files. Appends and rotation hold the lock and
fsync. An unterminated tail from an interrupted append is discarded; a corrupt
complete line is an error.

Rotated files are kept indefinitely. `--since ID` is exclusive and resumes
across files without gaps or duplicates. An unknown ID is an error, never a
silent reset. Deleting the event directory destroys replay and dedupe history;
consumers must then start without a cursor.

## Output and filters

| Mode | JSON |
| --- | --- |
| Default | One envelope with `data.events` |
| `--follow` | One envelope per line with `data.event`: existing matches, then new events |

Use `--since` to skip consumed history. The follower polls the log every 25 ms
while source watchers reconcile changes. Cancellation exits 0.

The MCP `events` tool reads the same events, a page at a time, without follow
([mcp.md](mcp.md#results)).

QUERY uses the [query language](query.md) with `changed:`, `hook:`, `action:`,
and `outcome:` enabled. Item terms match the event's item, not the current file.
`is:ready`, `is:blocked`, `is:waiting`, and `is:unknown` use
`changes.readiness.to` and are unknown for other event types. `is:archived`
needs an archive transition.

## Verification

```sh
task cli:test PKG='./internal/cli ./internal/hooks' -- -run 'TestWriteGolden|TestEvent' -count=1
BL_BENCH_EVENTS=1 task cli:test PKG=./internal/cli -- -run '^TestEventThreeObservers$' -count=1 -v
```

Process tests cover eight concurrent appenders, three observers, replay across
rotation, index rebuild, and crash recovery. Timing is only reported, never
asserted.
