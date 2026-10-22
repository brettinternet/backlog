# CLI commands

`ls`, `show`, and the read-only `next` interface are frozen. All accept global
`-C DIR` and `--json` (`-j`) before or after the command. Help is plain text.
JSON uses the [schema-version-1 envelope](design.md#7-agent-interface).
The write commands below are also frozen. Coordination and remote adapters remain separate backlog work.

```sh
bl ls 'is:open priority:>=high' --sort priority,id --group status -n 20
bl ls --view Next
bl show 12
bl show tasks:BL-012 --section criteria --json
bl next 'label:cli' -n 3 --explain --json
```

## `bl instructions [TOPIC]`

Print embedded, version-matched Markdown without loading project configuration.
With no topic, print the topic list and overview. Topics are `overview`,
`workflow`, `query`, `format`, and `json`. Unknown topics or extra arguments
exit 2 and list the valid topics. With `--json`, `data` contains `topic`,
`topics`, and `markdown`; no topic selects overview.

The workflow documents the available next/start/note/ac/done loop with JSON
and version guards. This foundation release does not claim work: next is
read-only and start changes status only. Use the project's external
coordination policy before starting concurrent work. The embedded text names
no repository task IDs; BL-32 and BL-33 replace this guidance when claims ship.

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

## `bl ls [QUERY] [--view V] [--sort S] [--group G] [-n N]`

Lists native items. With no `is:` term in QUERY or the view, `is:open` is
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

**Version contract:** `item.version` is the native codec's content hash, unchanged
from the source. Pass it verbatim to `--if-version` on writes.
It is not a timestamp or a hash of the JSON rendering.

## `bl next [QUERY] [-n N] [--explain]`

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

## Write commands

All writes accept `--json` and `--if-version V`. Use the version returned by
`show` when an operation depends on prior content, especially criterion numbers.
A stale version exits 4 (`conflict`) without changing bytes. Even without the
flag, the engine binds each write to its initial read and checks it under the
source's write lock. Writes validate config and source capabilities before
atomic persistence. Unchanged lines, unknown fields, comments, and quoting are
preserved. Native archives move without rewriting bytes or changing version.

`new` and `edit` return the summary item directly as `data`, including `version`.
Other writes return `data.outcomes`, one result per input ID in input order:

```json
{"outcomes":[{"id":"12","item":{"source":"tasks","id":"BL-012","title":"Ready","status":"todo","category":"todo","priority":"high","version":"new-hash"}},{"id":"999","error":{"reason":"not-found","message":"item not found: 999","details":{}}}]}
```

Each successful outcome carries the new item version. Batches attempt every ID;
they are atomic per item, not across items. If any ID fails, the envelope has
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

Coordination, hook/action execution, and Git auto-commit are not yet implemented.

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

No read command executes hooks, provider programs, or network operations.
Unsupported configured source types or incomplete native loads exit 6
(`unavailable`), never silently yielding a partial ranking. Auto-detected
Backlog.md/Beads backlogs are not read as native files; configure a native source
or import them when import support ships. These commands do not claim or write.

Golden `data` examples are in `internal/cli/testdata/{ls,show,next}.json`.
Regenerate deliberately with `task cli:test PKG=./internal/cli -- -update` and
review the diff; ordinary test runs compare without rewriting them.
