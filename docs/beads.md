# Beads

`bl` reads and updates a local embedded-Dolt Beads checkout through **bd 1.3.0**.
Install that version and put `bd` on PATH. No Beads daemon is required.

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

Relative paths resolve from the control root. Project-origin executable sources
require `bl trust` before use. Detection alone does not enable a source. Configure
custom statuses explicitly; unknown statuses default to backlog (never ready).

## Reads and mapping

Construction checks the version, embedded-Dolt metadata, and empty `sync.remote`.
Server mode and configured Dolt remotes are refused. A refresh runs exactly one
`bd --json --readonly --sandbox -C CHECKOUT list --all --limit 0`, regardless of
item count. The full list retains bodies and version fields, unlike `--brief`.
Polling runs on the source worker every 15 seconds, not on the UI update loop.
Details use the same bulk schema: `bd show` nests dependencies differently.
There is no per-item refresh process or provider-reported readiness lookup.
`bl` computes readiness from its own configured statuses and dependency graph.

| Beads field | `bl` field |
| --- | --- |
| `id` | Source-qualified issue ID; never reallocated |
| `title`, `issue_type`, `status` | Title, type, raw status |
| `open`, `in_progress`, `closed`, `tombstone` | todo, active, done, canceled unless configured otherwise |
| `blocked`, `deferred`, unknown status | backlog unless configured otherwise |
| Priority 0, 1, 2, 3 or 4 | urgent, high, medium, low (raw priority retained) |
| `assignee` | Zero or one assignee |
| `labels`, `description`, `notes` | Labels, description, notes |
| `created_at`, `updated_at` | Created and updated timestamps |
| All JSON fields, including unmapped fields | Raw `fields` |
| Canonical full issue JSON | SHA-256 opaque version, including dependency changes and comment count |

| `dependencies[].type` | `bl` edge |
| --- | --- |
| `blocks` | **Only hard edge**: `issue_id` depends on prerequisite `depends_on_id` |
| `parent-child` | Issue's parent is `depends_on_id`; informational for readiness |
| Every other type, including `discovered-from`, `related`, `tracks`, and unknown types | Related/informational; original type retained in raw fields |

Missing arrays with a positive dependency count, malformed dependencies, duplicate
IDs, null lists, and invalid JSON fail the whole refresh. They never establish
complete coverage or prove deletion. Empty arrays are complete empty snapshots.

## Writes

Supported operations are field sets (title, status, type, priority, assignees,
labels), same-issue batches of those sets, and appended comments. Unsupported
fields and operations fail before writing. Low priority writes as Beads priority 3;
priority 4 remains visible in raw fields. Comments are appended with `bl comment`;
comment history is not loaded into the item detail.

Every write requires the version that was read. Preflight rereads the full issue
JSON and refuses missing or stale versions with `conflict`. Updates additionally
use Beads' `--if-status` and `--if-assignee` guards; a failed guard reports a
conflict. `bd` has no full-revision compare-and-swap: another uncoordinated writer
can still change other fields between preflight and update, or append a comment
between preflight and comment. Worklease coordination remains advisory and must
use the same source identity across workers. Beads assignment is not a claim.

Writes invoke `bd update` or `bd comment` with `--sandbox --dolt-auto-commit=on`,
without `--readonly`, and reread after success. They create a **local Dolt commit**,
not a Git commit, and disable remote auto-push. With `export.auto=true`, Beads may
also update `.beads/issues.jsonl`. `bl` does not sync, push, retry ambiguous writes,
or keep a receipt/recovery journal. A command or read-back failure must be
reconciled by rereading before attempting another comment.

## Verification

```sh
task cli:test PKG=./internal/source/beads -- -count=1
BL_PROVIDER_TESTS=1 task cli:test PKG=./internal/source/beads -- -run TestProviderRoundTrip -count=1 -v
```

The opt-in test initializes a disposable Git/Beads checkout under the OS temporary
directory, creates two issues and a hard edge, then updates fields and appends a
comment. It never opens an existing user database. If PATH uses a tool shim that
is unavailable after test isolation, pass `BL_TEST_BD_BINARY=/absolute/path/to/bd`.
