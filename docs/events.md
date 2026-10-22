# Events

`bl events [--follow] [--json] [--since ID] [QUERY]` reads the project's durable
JSONL event log. `--follow` also watches the configured sources: no TUI or daemon
is required. The next snapshot load observes edits made while no process ran.
The first load establishes a baseline without inventing historical events.

```sh
bl events
bl events --json --since 01K8JHW1J00000000000000000
bl events --follow --json 'changed:status'
bl events --follow 'changed:readiness is:ready'
```

## Wire format

```json
{"schemaVersion":1,"id":"01K8JHW1J00000000000000000","type":"item.updated","time":"2026-10-22T18:00:00Z","project":"github.com/example/project","source":"tasks","ref":"tasks:BL-12","changes":{"status":{"from":"todo","to":"done"}},"item":{"ref":{"source":"tasks","id":"BL-12"},"title":"Ship events","status":"done","category":"done","version":"new-content-hash"},"actor":"@brett","origin":"cli","cause":null,"depth":0,"versions":["tasks:BL-12","old-content-hash","new-content-hash","false","false","false"]}
```

The example elides other changed fields and item fields. Fields are:

| Field | Contract |
| --- | --- |
| `schemaVersion` | Currently `1` |
| `id` | 26-character ULID; strictly increasing in append order, even within a millisecond or after a clock rollback |
| `type`, `time` | Event type and append time in UTC |
| `project`, `source`, `ref` | Stable project identity, source ID, and source-qualified display ID |
| `changes` | Changed fields mapped to `{from,to}`; readiness adds `reason` |
| `item` | Item after the edit, with lower-camel-case model keys; deletion uses the last observed item |
| `actor` | Configured default identity, when available; not an authenticated attribution |
| `origin` | `cli`, `tui`, `mcp`, `hook`, `action`, or `sync` |
| `cause`, `depth` | Causal event ID (or `null`) and nonnegative chain depth |
| `versions` | Producing identities used in deduplication, described below |

`changes` uses native field names for `blocked_by` and `duplicate_of`. It includes
body-section changes, evidence changes inside `criteria`/`dod`, generated
`updated` timestamps, and raw frontmatter under `fields`; it excludes the opaque
version and derived inverse edges. A successful no-op mutation still appends one
mutation event, with no changed fields and a unique operation nonce. Failed
mutations append no mutation event (a preceding sync may observe an external edit).
An atomic multi-field command appends one mutation event, not one per field.

## Event types

| Type | When and payload |
| --- | --- |
| `item.created` | A creation succeeds, or an established baseline discovers a new item. `changes` compares an absent item with its new values. |
| `item.updated` | An edit succeeds, an item is unarchived, or sync observes changed content. Unarchive includes `changes.archived: {from:true,to:false}`. |
| `item.archived` | An item moves into the archive, or a complete snapshot observes removal. A move includes `changes.archived`; removal includes `changes.present: {from:true,to:false}`. Editing an already archived item is `item.updated`. |
| `item.readiness` | Item data changes derived readiness. `changes.readiness` contains `from`, `to`, and `reason` (the engine's structured reason list, including `candidate` when becoming ready). |
| `claim.acquired`, `claim.released` | Successful claim operations through `bl`; producers ship with BL-32. Worklease-only overlay changes never emit `item.readiness`. |
| `hook.finished`, `action.finished` | Automation completion; producers ship with BL-30/BL-31. Additional fields: `name`, `outcome` (`ok`, `failed`, `timeout`, `refused`), `exitCode`, `duration` (nanoseconds), and optional `result` object (at most 8 KiB). |

Readiness includes the changed item, transitive dependents, and old/new parents
whose container eligibility may change. It evaluates item data only, never claims.
Only actual readiness transitions emit events; a dependent whose blocker is still
unsatisfied emits none. Partial source coverage never establishes deletion or
replaces the complete observation baseline. Valid items remain readable and writable
when an unrelated file is malformed; full sync resumes once coverage is complete.

## Deduplication and publication

The sole dedupe authority is the JSONL log. Under its cross-process project lock,
a producer checks `(type, ref, versions)` and appends only if that tuple is absent.
Mutation producing identities contain the ref, old/new content versions, old/new
archive flags, and deletion flag. Readiness identities contain the producing
transitions for the sync/mutation, in deterministic order, so changes to an
unchanged dependent caused by different blockers do not collapse.

A disposable SQLite index accelerates dedupe and cursor lookup. Each transaction
indexes new complete log lines before checking a key, recovering appends whose
process died before its index commit. Removing the index rebuilds it from the
log, without changing dedupe. Only a successful appender owns dispatch;
`Transaction.Appended()` returns that producer's events with their assigned IDs.
Automation execution itself ships separately.

Comparison, append, and publication of the per-item SQLite observation baseline
are serialized by the same lock. Two TUIs and a follower observing one edit
therefore emit one mutation event and one readiness event per actual transition,
not three copies. Unchanged native cache revisions skip baseline scans. Mutation
writes touch one baseline item; readiness reads the affected graph and its direct
neighbors, not every item's body. All this work runs off the Bubble Tea update loop.

## Cause and depth

Root commands and sync events have `cause: null` and `depth: 0`. A hook/action
subprocess inherits `BL_EVENT_ID` and `BL_HOOK_DEPTH`; its mutation names that
cause and increments depth. `BL_EVENT_ORIGIN=hook|action` identifies the producer.
The CLI, TUI, and MCP otherwise identify their own origins. Sync never inherits a
causal chain merely because an automation happened to discover an external edit.
Events deeper than 3 can be recorded, but hooks must not dispatch for them.
Hook/action completion uses the same causal contract. Nothing retries automation.

## Rotation and replay

Logs live in `$XDG_STATE_HOME/backlog/events/<sha256(project)>/` (under the
platform's resolved XDG state directory). `current.jsonl` rotates at 8 MiB; an
individual event is bounded to 16 MiB and is never split across segments. Append
and rotation hold the lock; complete lines and directory entries are fsynced.
An interrupted, unterminated tail is discarded under the lock before the next
append/read. Corruption of a complete line is an error, not silently skipped.

Rotated segments are retained without automatic expiry. `--since ID` is exclusive
and resumes across every retained segment without gaps or duplicates. An unknown
cursor is an explicit error, never a silent reset. Clearing project event state
intentionally destroys replay and dedupe history; consumers must then restart
without an old cursor. The observation baseline lives in XDG cache and can be
removed independently; its next full load establishes a fresh baseline, rather
than claiming knowledge of edits that were never observed.

Without `--follow`, JSON is one normal envelope with `data.events` (an array).
With `--follow --json`, each output line is a normal envelope with `data.event`.
The follow stream includes existing matching events, then new events; use
`--since` to avoid replaying already consumed history. Cancellation exits normally.
A follower checks the log every 25 ms while source watchers reconcile changes.

`QUERY` uses the shared query language, with event terms `changed:`, `hook:`,
`action:`, and `outcome:` enabled. Item predicates use the event's item, not the
item's current on-disk version. `is:ready`/`is:blocked`/`is:waiting`/`is:unknown`
use `changes.readiness.to` on readiness events; derived readiness is unknown on
other event types. `is:archived` uses an explicit archive transition.

## Verification

```sh
task cli:test PKG='./internal/cli ./internal/hooks' -- -run 'TestWriteGolden|TestEvent' -count=1
BL_BENCH_EVENTS=1 task cli:test PKG=./internal/cli -- -run '^TestEventThreeObservers$' -count=1 -v
task race
task ci
```

The process tests cover eight appenders, three independent observers, exclusive
cursor replay across rotation, index rebuild, and interruption recovery. Timing
is reported only in opt-in measurements, never asserted by normal CI.
