# CLI commands

## Git auto-commit

`git.autoCommit` defaults to `false`. Enable it in user or project config:

```yaml
git:
  autoCommit: true
```

Project-configured auto-commit requires `bl trust`, because Git executes hooks.
Native item mutations (including `check --fix` and `renumber`) make one
pathspec-limited commit per CLI command in the control checkout. Multiple items
share that commit. MCP mutations commit once per request. The subject is
`docs(backlog): <verb> <ID> <title>`; a multi-item batch uses its last item's
ID and title. CLI verbs use the command name; TUI and MCP use the mutation
operation. No fetch, pull, push, or hook bypass occurs.

Unrelated staged and unstaged paths remain untouched. A target path already
dirty before the first edit, changed externally before commit, or staged during
the batch causes the batch to skip with a warning. Merge, rebase, cherry-pick,
detached HEAD, a busy repository, unavailable Git, and failed hooks also warn
and leave the file write in place. Git commands time out after 30 seconds.
Skipped or failed batches are not retried automatically. Hooks must not mutate
the same backlog while its Git commit holds the repository write lock.

Auto-commit does not apply to project initialization, import into a new project,
user configuration, or remote-provider writes.

`ls`, `show`, and the read-only `next` interface are frozen. All accept global
`-C DIR` and `--json` (`-j`) before or after the command. Help is plain text.
JSON uses the [schema-version-1 envelope](design.md#7-agent-interface).
The write commands below are also frozen. Coordination is optional; GitHub is
available with [provider-specific write semantics](github.md).

```sh
bl ls 'is:open priority:>=high' --sort priority,id --group status -n 20
bl ls --view Next
bl show 12
bl show tasks:BL-012 --section criteria --json
bl next 'label:cli' -n 3 --explain --json
```

## Configuration, trust, and diagnostics

`bl config show [--json]` loads settings without running declarations or contacting
sources. JSON `data` contains the effective non-secret settings at the top level,
plus `directory`, `paths`, `discovery`, `hostLocal`, `provenance`, and `privileged`.
`provenance` maps each declared value's dotted/indexed key to
`{layer, file?, line?}`; layers are `built-in`, `discovery`, `user`, `project`,
`user-project`, `environment`, and `flag`. Implicit serialized defaults such as
`milestones[0].archived: false` have the `built-in` layer. For example:

```json
{"ui":{"highContrast":false},"provenance":{"ui.highContrast":{"layer":"project","file":"/repo/.config/backlog.yaml","line":4}},"privileged":[{"key":"hooks[0]","trusted":false,"origin":{"layer":"project","file":"/repo/.config/backlog.yaml","line":8}}]}
```

Human output prints effective settings and their provenance. Account credentials
are never included. `privileged` lists hooks, actions, and program/network-backed
sources, with each declaration's trust marker and origin.

`bl trust --show [--json]` shows the control root, SHA-256 digest, declarations,
and trust markers without granting consent. Inspect it before `bl trust`, which
atomically records consent under `$XDG_STATE_HOME/backlog/trust/` (default
`~/.local/state/backlog/trust/`). JSON `data` has `root`, `digest`, `granted`,
`entries`, and effective `hooks`, `actions`, and `sources` when present.
Consent is bound to the canonical control-root path, project identity, and the
effective project-origin privileged subset. Every field of those declarations
is hashed, including argv, cwd, conditions, subscriptions, and timeouts. Mapping
key order, comments, and nonprivileged settings do not invalidate consent;
semantic privileged changes require re-trust. User and user-per-project
config are always trusted. Grants never launch anything by themselves.

Untrusted programs and network sources remain inert on every interface. The TUI
shows a persistent trust banner and can still browse local native sources.
Hooks and actions enforce `Config.RequireTrusted` before execution. This build
ships native, [Backlog.md](backlog-md.md), Beads, [GitHub](github.md), [Linear](linear.md),
and external adapters; Jira remains unavailable. Doctor does not execute provider
credential helpers; actual reads verify credentials and provider bindings.

`bl doctor [--json] [--online]` reports six checks: `config`, `trust`,
`control-root`, `cache`, `worklease`, and `sources`. Each check has `name`,
`status` (`pass`, `warn`, `fail`, or `skip`), `message`, and a nonempty `fix` hint.
JSON `data.checks` contains every check, including on failure (`ok: false`,
`error.reason: rejected`, exit 5). Invalid config yields a failed config check
and skipped dependent checks. Any failed check exits 5; warnings alone exit 0.

Doctor checks existing native caches read-only, without repairing corruption.
It never fetches Git refs: the control branch is compared with local origin/HEAD,
then init.defaultBranch, then an existing main/master; unknown or nondefault
branches warn. Worklease's local `version --json` must report schema v2 and
version >= 1.10.1. Absence warns when coordination is disabled and fails when
configured. Source checks validate native directories and prefixes; unavailable
adapters fail with a fix hint. Backlog.md sources validate their provider config
and local directory. Network probes are permitted only by `--online`,
never before trust, and only for installed adapters; the native-only build
performs no network probes even with that flag.

## `bl adapter check [--timeout 5s] EXEC [ARG...]`

Explicitly execute an external adapter and check protocol negotiation, mandatory
capabilities, bounded list pages, and a read of the first item when present.
Use `--` before EXEC when its arguments begin with flags. This command does not
grant trust to configured sources. Timeout must be positive and at most one
minute. Success prints `Adapter protocol v1: PASS`; JSON data is
`{"protocolVersion":1,"valid":true}`. Failures use reason `adapter-check` and a
safe diagnostic, including invalid frames, protocol versions or capabilities.
See [external-adapter-protocol.md](external-adapter-protocol.md).

## `bl instructions [TOPIC]`

Print embedded, version-matched Markdown without loading project configuration.
With no topic, print the topic list and overview. Topics are `overview`,
`workflow`, `query`, `format`, and `json`. Unknown topics or extra arguments
exit 2 and list the valid topics. With `--json`, `data` contains `topic`,
`topics`, and `markdown`; no topic selects overview.

The workflow documents the claimed next/heartbeat/done loop through CLI and MCP,
with one stable distinct session, version guards, exact pending recovery, and
separate claim/status outcomes. Plain `next` remains read-only. See the
provider-evidence requirements below.

## OAuth accounts

OAuth accounts are declared only in user config and explicitly opt in with
`oauth: true`. They cannot also select a credential helper or environment token.
The shared manager stores tokens in the owner-private XDG state directory, not
config or cache, verifies the account principal, serializes refresh across
processes, and removes token/cache state on logout. Existing helper and environment
accounts keep their current behavior.

```sh
bl auth login linear work          # interactive, read scopes only
bl auth login linear work --write  # explicit read grant, then write-scope upgrade
bl auth logout linear work         # local deletion/cache purge; remote revoke if supported
```

Login requires a terminal and is never initiated by MCP, claims, hooks or actions.
Scope upgrades trigger provider permission/capability rediscovery. Logout reports
when provider settings must be used for manual revocation. The shared command
exists, but no production provider flow is registered yet; BL-61/62 own those
integrations. Jira OAuth is excluded by the owner. See [OAuth qualification](oauth.md)
for the evidence matrix, current boundary and provider requirements.

`--json` returns a versioned envelope with `provider`, `account`, requested
write-consent state, and for logout whether remote revocation happened or is
manual. OAuth failures use stable `error.reason` codes such as
`authentication-required`, `token-expired`, `grant-revoked`, `insufficient-scope`,
`sso-required`, `rate-limited`, `provider-unavailable`, and
`principal-mismatch`; messages never include provider response bodies or tokens.

## Native read cache

`ls` and `next` keep a disposable SQLite WAL cache under
`$XDG_CACHE_HOME/backlog/projects/<project-id-hash>/` (default
`~/.cache/backlog/projects/`). Each source and absolute source path has its
own database and adjacent refresh lock. Unchanged items are validated by size,
nanosecond mtime, inode, and ctime without opening item files. Changed files
alone are reparsed; refreshes are serialized across processes.

Corrupt or incompatible caches are deleted and rebuilt with a warning on
stderr. Rows written by a different `bl` executable are reparsed silently, so
an upgraded parser never serves stale items. An unavailable cache falls back to
native files.
Removing cache data only costs the next read time; it never removes backlog
items. `show` and writes read authoritative files directly.

## `bl import backlog-md [PATH] [--to DIR] [--dry-run]`

Imports Backlog.md files directly into a separate native project. PATH defaults
to the invocation directory; a repository root or backlog directory is accepted.
`--to` defaults to `backlog-import` relative to `-C`. The destination must be empty
or absent, including for a dry run; refusal exits 5. The source is never changed.
`--dry-run` prints the validated plan without writing. Human output is an indented
mapping report; `--json` puts that report in the standard envelope's `data`.
See [import.md](import.md) for field mappings and skipped content.

## `bl ls [QUERY] [--workspace NAME] [--view V] [--sort S] [--group G] [-n N]`

`--workspace NAME` reads all configured member projects with their own statuses and trust. Workspace item JSON adds `project` (the project identity); `source` is the stable `url.QueryEscape(project.id)/source-id` key. Without this flag, `ls` retains project scope. See [format.md](format.md) for cross-project references.

Lists source items. Partial provider scans are usable: fresh rows may be ready
for `next`, but retained stale rows and missing or stale blockers never authorize
selection. Unknown coverage or error findings fail the read closed.
With no `is:` term in QUERY or the view, `is:open` is
added, so closed items need `is:closed` (or another `is:` term). Archives are
loaded only for a positive `is:archived` term; archived items are never ready.
Query terms use [query.md](query.md); multiple positional
arguments are joined with spaces. Put `--` before a negated first term
(`bl ls -- -label:wontfix`). `--view` selects an exact saved-view name.
The view query is ANDed with QUERY; view sort/group override query directives,
and explicit flags override the view. Otherwise query directives apply.
With no explicit sort, engine next ranking breaks ties. Sorting precedes the
limit, then grouping; multi-value groups can repeat an item. `-n` defaults to
0 (all); negative limits are usage errors.

`data`:

```json
{"groups":[{"value":"todo","items":[{"source":"tasks","id":"BL-012","title":"Ready","status":"todo","category":"todo","priority":"high","version":"opaque-content-hash"}]}]}
```

An ungrouped result has one group with `value: ""`, including when empty.
Grouped empty results have `groups: []`. Every summary item has all eight
fields above; an unset priority is `""`. Status is the provider's raw status,
not its display name or category. IDs retain their original padding.

## `bl show ID [--section S]`

Shows one item's metadata and body. Numeric `12` and `012` expand using each
configured native source's prefix. `BL-12` also matches `BL-012`. Lookups search
configured sources, or only the named source with `source:ID`; ambiguity is
never silently resolved. Exit 2 (`usage`) lists source-qualified matches in
both the message and `error.details.matches`. An ID stored by both an active
and an archived file also exits 2; select one with `bl renumber`'s path form. A missing ID exits 3
(`not-found`). Explicit lookups can find archived items. Detail reads parse only
the matched files, not every item, and do not compute readiness/ranking.

`--section` accepts `description`, `criteria`, `dod`, `plan`, `notes`, `summary`,
or `comments`. A missing section is an empty string. Invalid names exit 2.
Text section output prints only that section. JSON still carries item metadata.
Criteria and DoD use Markdown checkboxes; comments use timestamp/author/body.

`data`:

```json
{
  "item":{"source":"tasks","id":"BL-012","title":"Ready","status":"todo","category":"todo","priority":"high","version":"opaque-content-hash"},
  "metadata":{
    "type":"task","rank":"","assignees":null,"labels":null,
    "milestone":"","due":"","parent":"","children":[],
    "blockedBy":[],"blocks":[],"related":[],"duplicateOf":"",
    "refs":null,"files":null,
    "created":"2026-10-21T00:00:00Z","updated":"2026-10-21T00:00:00Z",
    "url":"","fields":{}
  },
  "sections":{"description":"Body.","criteria":"- [ ] Works","dod":"","plan":"","notes":"","summary":"","comments":""}
}
```

`item` has the same summary shape as `ls`. `metadata` always has the listed keys.
Parent, duplicate, and edge references are source-qualified strings; unset
single refs are `""`. Edge arrays are empty arrays when unset; assignees, labels,
refs, and files may be null. `fields` is the source's raw frontmatter object,
including unknown fields. Timestamps use RFC 3339. Native reads currently expose
stored edges; inverse edges are not synthesized for a detail-only read.
With `--section`, `sections` contains only that key.
JSON also includes `criteria` and `dod` arrays when nonempty. Each entry has
`text`, `checked`, and optional `evidence`; evidence is not duplicated in `text`.
The Markdown `sections.criteria` and `sections.dod` include evidence lines.

**Version contract:** `item.version` is the source's opaque version (the native
codec's content hash for files; a composite issue/relations/project fingerprint
for GitHub), unchanged from the source. Pass it verbatim to `--if-version` on
writes. It is not a hash of this JSON rendering.

## `bl next [QUERY] [--workspace NAME] [-n N] [--explain] [--claim [--start]]`

`--workspace NAME` ranks the composed workspace using each item's own project policy. Its item shape matches workspace `ls`. Workspace `next` is read-only: `--claim` is refused; claim/start from the owning project so selection and recovery retain their project control root.

Returns ready candidates in the engine's deterministic ranking. QUERY narrows
candidates without removing their blockers from readiness evaluation. `-n`
defaults to 1; 0 means all; negative limits exit 2. Sort/group directives are
rejected because `next` always uses readiness ranking. No candidates is a
successful response with `items: []`.

`data` without `--explain`:

```json
{"items":[{"source":"tasks","id":"BL-012","title":"Ready","status":"todo","category":"todo","priority":"high","version":"opaque-content-hash"}]}
```

With `--explain`, a nonempty snapshot adds `explanations`, containing **every**
included and excluded item in engine ranking order:

```json
{"items":[],"explanations":[{"ref":{"source":"tasks","id":"BL-13"},"readiness":"blocked","reasons":[{"code":"blocker","refs":[{"source":"tasks","id":"BL-012"}]}],"priority":"","unblockImpact":0,"included":false}]}
```

Explanations preserve the [engine's reason codes](readiness.md) and optional
`rank` and `unblocks` (up to two refs). Reasons may carry `refs` or `values`.
`included` means selected in `items`; a ready item excluded by QUERY or the
limit has an additional `query` or `limit` reason. These do not change its
readiness. On an empty snapshot, `explanations` is omitted.

### Claimed selection

```sh
bl next --claim --start --session loop-a --explain --json
bl heartbeat --session loop-a --json
bl done BL-012 --session loop-a --if-version VERSION --json
```

`--claim` requires an explicit session before any Worklease call (exit 2,
`session-required`); `--start` requires `--claim`. Use one stable distinct session
through selection, renewal, status changes, and completion. Claimed selection
returns at most one item, walking one ranked snapshot without waiting or a
second pass. Confirmed contention skips to the next candidate; an uncertain
acquire stops with `unknown-outcome`, naming the item and retaining recovery
state. JSON includes every attempt and separate claim/status outcomes. A failed
start retains the acquired claim. A successful done waits for its caller-managed
release before returning, so the same session can select again.

Selection inspects session state before walking. A held open item returns
`resumed: true` without acquiring, even if it no longer matches readiness or the
query. A pending acquire replays the identical retained request and resumes only
after confirmed ownership. Still-uncertain replay stops with `unknown-outcome`;
missing items, unresolved state, and closed held items never fall through to new
selection (closed items report `claim-held`). Do not switch sessions to bypass
recovery. In a verified run, selection returns only that run's item without
acquiring or walking, or `claim-held` once that item is closed; the supervisor
owns heartbeat and release.

## Write commands

All writes accept `--json` and `--if-version V`. Use the version returned by
`show` when an operation depends on prior content, especially criterion numbers.
A stale version exits 4 (`conflict`) without changing bytes. Even without the
flag, the engine binds each write to its initial read and checks it under the
source's write lock for local files. Writes validate config and source capabilities
before persistence. Native persistence is atomic; unchanged lines, unknown fields,
comments, and quoting are preserved. Native archives move without rewriting bytes
or changing version. GitHub checks a fresh composite version before one non-retried
mutation, but its API has no CAS: a concurrent preflight-to-write race remains.
See [GitHub writes](github.md#writes-and-claims).

`new` and `edit` return the summary item directly as `data`, including `version`.
Other writes return `data.outcomes`, one result per input ID in input order:

```json
{"outcomes":[{"id":"12","item":{"source":"tasks","id":"BL-012","title":"Ready","status":"todo","category":"todo","priority":"high","version":"new-hash"}},{"id":"999","error":{"reason":"not-found","message":"item not found: 999","details":{}}}]}
```

Each successful outcome carries the new item version. Batches attempt every ID;
local batches are atomic per item, not across items. GitHub accepts one field
assignment per command and refuses multi-field batches. If any ID fails, the envelope has
`ok: false`, retains `data.outcomes`, and uses the first failed ID's error and
exit code. Multiple IDs with `--if-version` exit 2 before writing any item.
Text output reports each outcome too.

### `bl new TITLE [TOKENS] [--status S] [--body TEXT|-] [--ac TEXT]... [--source SOURCE]`

Creates an item using the [quick-add tokens](query.md#shared-quick-add-parser) (priority
`!h`, assignee `@me`, label `#bug`, parent `^BL-1`, blocker `<BL-2`,
milestone `~m-1`, and type `:feature`). Quote tokens against shell expansion.
`@me` resolves from `me.default`. Parent and blocker references accept ID
shorthand like `show` and are stored as the target's ID (`<1` stores `BL-1`,
or `BL-001` when that file exists); references to another source stay
qualified. A missing target is stored as written and reported by `bl check`. `--body -` reads stdin verbatim.
Repeat `--ac` for single-line criteria; commas stay inside one criterion. Config `defaults.type` applies unless a
type token is supplied, and `defaults.dod` supplies unchecked DoD entries.
`--status` takes a configured status key, so `--status draft` creates a draft that
is never ready (design.md D29); an unknown key exits 5 with the allowed values and
writes nothing. Without it, status is the first configured todo-category status,
or the first configured status if none is todo. `--source` is required unless exactly one source exists.
The source allocates a fresh shared ID; creation with a nonempty
`--if-version` fails with conflict because no prior item exists.

### `bl set ID... FIELD=V FIELD+=V FIELD-=V`

IDs precede assignments. All assignments for one item succeed or fail together.
Writable fields: `title status type priority assignees labels milestone parent
blocked_by related duplicate_of rank due refs files`. `label` and `assignee`
are aliases for the plural fields. List `=` takes comma-separated values;
an empty value clears an optional field. `+=` and `-=` add/remove one list
entry and are idempotent. Scalar fields accept only `=`. `parent`,
`blocked_by`, `related`, and `duplicate_of` values expand ID shorthand the
same way as `new` references.
Unknown fields, statuses, or priorities exit 5 (`rejected`) with allowed values.
Types, labels, and milestones are free text; catalogs provide suggestions only.
IDs and timestamps are not writable fields.

### `bl start ID` and `bl done ID`

Set status to `workflow.start` or `workflow.done`, respectively. Missing workflow
configuration is rejected, not guessed. Each takes exactly one ID.
`bl events [--follow] [--json] [--since ID] [QUERY]` reads the project event
log. Finite JSON uses `data.events`; follow mode emits one envelope with
`data.event` per line. The cursor is exclusive across rotation; an unknown cursor
is an error. Follow mode watches sources without a TUI. See [events.md](events.md)
for event types, filters, dedupe, retention, and causal depth.

Git auto-commit is not yet implemented. Coordination and automation follow the
contracts below and in [automation.md](automation.md).

### `bl note ID TEXT|-` and `bl comment ID TEXT|- [--author AUTHOR]`

Append a paragraph to Notes or a timestamped comment. A lone `-` reads stdin.
Empty text is a usage error. Comment author defaults to `me.default`; without
that setting, supply `--author`. Comments are append-only through these commands.

### `bl ac ID (check|uncheck) N...`

Check or uncheck one or more one-based acceptance-criterion indices atomically.
Use `bl ac ID add TEXT` to append one single-line criterion and
`bl ac ID rm N` to remove one criterion with its continuation lines.

`bl ac ID check N --evidence TEXT` takes exactly one N and nonempty single-line
evidence. It writes one indented `Evidence:` line under that criterion,
replacing existing evidence lines. Checking without the flag preserves evidence;
unchecking removes it. Removing an earlier criterion renumbers later criteria
without moving their evidence away from them. `show --json` exposes evidence
per criterion. Always pass `--if-version` when acting on indices read earlier.

### `bl edit ID [--section S]`

Launches `$EDITOR` (default `vi`) with a private temporary native-file buffer.
Editor arguments are supported through the shell. Without `--section`, edit
the complete native document. Editable sections are `description`, `criteria`,
`dod`, `plan`, `notes`, and `summary`; comments remain append-only through
section commands. Section buffers include unstructured Markdown, not just
parsed entries. The editor runs in the invocation directory; its output goes to
stderr so JSON stdout stays machine-readable.

The initial version is captured before launching the editor. A concurrent save
exits 4, leaving the concurrent file intact and retaining the edited buffer.
Invalid frontmatter or values exit 5 without changing the original.
Errors name the recovery file in the message and `error.details.editedPath`.
Successful changed saves return the new version; unchanged buffers are not written.

### `bl archive ID...` and `bl unarchive ID...`

Move native items into or out of the source's `archive/` directory. Occupied
targets are conflicts. Explicit ID reads still find archived items; lists and
next ranking exclude them. The bytes and content version are unchanged.

## `bl action NAME ID [--yes]`

Run a trusted configured action for one matching item. `when` and placeholder
validation are shared with the TUI and MCP `action` tool (`name`, `id`, optional
`confirm`). `confirm: true` requires interactive acknowledgement or `--yes`;
noninteractive requests without acknowledgement refuse without launching.
JSON success data contains `event` (the `action.finished` event), optional
`message`, and `detached: true` when a background runner accepted the invocation
instead of waiting for completion. Failed runs return a rejection; the event log
retains the outcome and result. See [automation.md](automation.md) for stdin,
environment, chaining, and example commands.

## Maintenance commands

### `bl init [--prefix P] [--path DIR]`

Initialize a repository with `.config/backlog.yaml` and a native item directory.
The defaults are prefix `BL` and path `backlog`; `--path` is stored in the
project config and created relative to the control root unless it is absolute.
The generated config includes `draft`, `todo`, `in-progress`, and `done`
statuses and workflow defaults. `project.id` is written only when derived from a shared
`origin` remote; a host-local path is left to the runtime default
(design.md §5). Run inside a Git repository. An existing project config is
never overwritten; this refusal exits 5 and creates no files.

### `bl check [--json] [--fix]`

Validate every active and archived item in configured native sources. Findings
include `code`, `severity`, `path` (relative to the control root when possible),
`line`, and `message`; JSON puts them in `data.findings`. Errors exit 5, while
warnings and informational findings do not. With no findings, JSON returns an
empty `findings` array.

`--fix` applies only the repairs listed in [format.md](format.md): it renames a
mismatched filename when the destination is free, repairs supported
`updated`-only and whole-comment merge/diff3 hunks, and normalizes line endings
and the final newline. Unsupported conflict hunks remain in place and are
reported as `merge-conflict`. All repair targets are checked while holding the
native write lock. Findings are reported after repairs, so unresolved errors
still produce exit 5.

### `bl renumber FROM TO`

Rename an item and update `parent`, `blocked_by`, `related`, `duplicate_of`, and
source-qualified references in every active and archived native item. The
operation preflights every affected file and refuses an existing target ID or
path. `FROM` may be an ID, a number, `SOURCE:ID`, or a source-relative file path
selector. A numeric `TO` takes the selected source's prefix (`bl renumber 1 7`).
For example, when active and archived items share `BL-9`, choose one explicitly
with `tasks:archive/BL-9.md` (or `tasks:BL-9.md` for the active file). An ID that
matches multiple items is refused with candidate paths rather than guessed.
Every reference that resolves to the selected source and old ID is rewritten
to `TO`. Invalid or conflicted item files prevent renumbering rather than
allowing an incomplete reference rewrite.

## Output and source safety

Text list rows are compact source-qualified ID/status/priority/title tables,
truncated to terminal cell width (80 columns off-terminal), including Unicode
wide characters. Bodies retain newlines and tabs. Text output strips CSI,
OSC, DCS and other terminal escape sequences and unsafe control characters;
table cells also flatten tabs and newlines. JSON preserves original strings.

Read commands do not execute hooks. With coordination enabled, list/next load one
bulk Worklease claim overlay; show derives its resource in-process.
Unsupported configured source types or incomplete native loads exit 6
(`unavailable`), never silently yielding a partial ranking. Auto-detected
Backlog.md/Beads backlogs are not read as native files; configure a native source
or import them when import support ships. Plain read commands do not claim or
write; `next --claim [--start]` explicitly opts into coordination and mutation.

Golden `data` examples are in `internal/cli/testdata/{ls,show,next}.json`.
Regenerate deliberately with `task cli:test PKG=./internal/cli -- -update` and
review the diff; ordinary test runs compare without rewriting them.

## Coordination

Enable with `coordination: {worklease: {}}`. Every Worklease child runs from the
control root, including when invoked from a linked worktree. Authority selection
(`WORKLEASE_HOME`, `WORKLEASE_PROFILE`) passes through; inherited
`WORKLEASE_SESSION_ID`, `WORKLEASE_HANDLE`, and `WORKLEASE_RUN_ID` never do.
CLI/TUI ownership comes from global `--session`, then `BL_SESSION`, then
`WORKLEASE_SESSION_ID`; `WORKLEASE_RUN_ID` requests run verification. Use a stable,
nonempty session per loop. MCP instead requires explicit context per call.

```sh
bl claim 12 --session loop-a --json
bl heartbeat --session loop-a --json
bl release 12 --session loop-a --json
```

`claim` has no wait. Contention exits 7 and names the holder. An occupied session
holding a different item exits 4 (`claim-held`), not contention. Missing session
exits 2. `release` changes only the caller's exact single-resource claim; an
absent or unrelated claim reports `not-held`. `heartbeat` reports `expiresAt`.
A verified live run reports `run-managed` without acquiring, renewing, or
releasing anything. Invalid run evidence exits 4 (`run-mismatch`).

Representative contention and release envelopes (opaque identities abbreviated):

```json
{"schemaVersion":1,"ok":false,"command":"claim","data":{"outcome":"contended","reason":"contention","message":"resource claimed (holder agent@host)","claim":{"claimId":"holder","resources":["RESOURCE"],"session":"other","agent":"agent@host","expiresAt":"2030-01-01T00:00:00Z"}},"error":{"reason":"contention","message":"resource claimed (holder agent@host)","details":{}}}
{"schemaVersion":1,"ok":true,"command":"release","data":{"outcome":"released","resource":"RESOURCE","claim":{"claimId":"claim","resources":["RESOURCE"],"session":"loop-a","agent":"agent@host","expiresAt":"2030-01-01T00:00:00Z"}}}
{"schemaVersion":1,"ok":true,"command":"heartbeat","data":{"outcome":"renewed","expiresAt":"2030-01-01T00:00:00Z"}}
```

`show --json` adds `data.resource`, derived using the same policy as acquire.
Native/Backlog.md identities use the project ID and stored item ID, never a
worktree path. GitHub and Linear policies match Worklease's portable keys.
Regenerate pinned vectors with `mise exec -- python3 scripts/worklease-key-vectors`.

Status changes share an engine gate across start/done, set, TUI, editor restores,
and MCP update. Entering active claims first. Contention writes nothing; a failed
write keeps the acquisition. Entering done/canceled writes first, then releases
only a matching caller-managed claim. Unresolved context refuses the status write.
A release failure leaves the status written. JSON write outcomes add
`transition: {status: "not-written"|"failed"|"written", claim: {...}}`, including
on errors. Inspect both outcomes before retrying. Multi-item activation is refused.

### Provider evidence and recovery

Coordination requires schema-v2 public resource identities from Worklease's
lifecycle, handle, run, and list commands, plus conditional release using
`release --if-claim-id ID`. The guard binds release to the verified claim rather
than a mutable session handle.

Worklease 1.10.1 is the minimum supported coordination release; this
repository selects the latest release through mise. It includes exact public resource identities and guarded
release. Worklease 1.9.3 remains the historical key-policy baseline, not a
supported lifecycle provider. Repository coordination remains optional; the
native development loop uses `next --claim --start`, `heartbeat`, and `done`.

Unknown, malformed, or redacted evidence fails closed: a redacted acquisition is `uncertain`/exit 6 and keeps the claim;
inspection is unresolved; an unusable bulk list makes readiness unknown. Do not
retry with a new session or synthesize a new resource. Inspect/recover the same
Worklease session explicitly.

Pending acquisition recovery remains in Worklease. The internal coordinator uses
an opaque adapter reference and rechecks identity before bare-handle exact replay;
it never reconstructs credentials, TTL, agent, work key, wait, or deadlines.
Version 1.9.3 supports this form remotely. Local pending options are not publicly
inspectable, so local exact recovery may require the original Worklease command.
Corrupt, foreign, stale, changed, or unverifiable handles never authorize a fresh
acquire. Offline handle inspection alone never proves live ownership.
