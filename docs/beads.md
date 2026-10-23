# Beads

`bl` reads and updates a local embedded-Dolt Beads checkout through **bd 1.3.0**
on `PATH`. No Beads daemon is needed.

```yaml
sources:
  - id: issues
    type: beads
    path: /absolute/path/to/beads-checkout
statuses:
  - {key: open, name: Open, category: todo}
  - {key: in_progress, name: In progress, category: active}
  - {key: closed, name: Closed, category: done}
workflow: {start: in_progress, done: closed}
ready:
  statuses: [open]
  satisfiedBy: [done, canceled]
```

Relative paths resolve from the control root. Project-declared Beads sources
need `bl trust`. A detected `.beads/` isn't loaded until configured. Declare
custom statuses; unknown ones are `backlog` and never ready. Server mode and
Dolt remotes (`sync.remote`) are refused.

## Reads

Each refresh runs one command, whatever the item count:

```text
bd --json --readonly --sandbox -C CHECKOUT list --all --limit 0
```

It polls every 15 seconds on the source worker. Detail uses the same data (`bd
show` nests dependencies differently). `bl` computes readiness itself from its
statuses and the dependency graph.

| Beads | `bl` |
| --- | --- |
| `id` | Issue ID, never reallocated |
| `title`, `issue_type`, `status` | Title, type, raw status |
| `open`, `in_progress`, `closed`, `tombstone` | todo, active, done, canceled (unless configured) |
| `blocked`, `deferred`, unknown | backlog (unless configured) |
| Priority 0–4 | urgent, high, medium, low, low (raw value kept) |
| `assignee` | Zero or one assignee |
| `labels`, `description`, `notes` | Same |
| `created_at`, `updated_at` | Created, updated |
| Every JSON field | Raw `fields` |
| Full issue JSON, canonicalized | SHA-256 version (covers dependencies and comment count) |

| `dependencies[].type` | Edge |
| --- | --- |
| `blocks` | **The only hard edge:** `issue_id` is blocked by `depends_on_id` |
| `parent-child` | Parent is `depends_on_id` (not a blocker) |
| Anything else (`discovered-from`, `related`, `tracks`, …) | Related; type kept in raw fields |

Malformed output fails the whole refresh: a positive dependency count without
the array, bad dependencies, duplicate IDs, null lists, or invalid JSON. A
failed refresh never counts as complete or proves deletion. Empty arrays are a
valid empty snapshot.

## Writes

```sh
bl set issues:bd-12 status=in_progress priority=high --if-version VERSION
bl comment issues:bd-12 'Progress update'
```

Supported: setting title, status, type, priority, assignees, and labels (several
at once on one issue), and appending comments. Anything else fails before
writing. `low` writes as priority 3. Comment history isn't shown in detail.

Every write rereads the issue and refuses a missing or stale version
(`conflict`). Updates also pass `--if-status` and `--if-assignee`. `bd` has no
full compare-and-swap, so another writer can still change other fields, or add
a comment, between the check and the write. Beads assignment isn't a claim;
Worklease coordination is advisory and needs the same source identity across
workers.

Writes run `bd update` or `bd comment` with `--sandbox --dolt-auto-commit=on`
and reread afterward. That makes a **local Dolt commit**, not a Git commit, with
auto-push off. With `export.auto=true`, Beads may also update
`.beads/issues.jsonl`. `bl` doesn't sync, push, or retry. After a failed command
or reread, reread before trying again, especially before another comment.

## Tests

```sh
task cli:test PKG=./internal/source/beads -- -count=1
BL_PROVIDER_TESTS=1 task cli:test PKG=./internal/source/beads -- -run TestProviderRoundTrip -count=1 -v
```

The opt-in test builds a disposable Git and Beads checkout in the temp
directory, creates two issues and a blocking edge, edits fields, and comments.
It never opens an existing database. If a `PATH` shim breaks under test
isolation, set `BL_TEST_BD_BINARY=/absolute/path/to/bd`.
