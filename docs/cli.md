# Read commands

`ls`, `show`, and the read-only `next` interface are frozen. All accept global
`-C DIR` and `--json` (`-j`) before or after the command. Help is plain text.
JSON uses the [schema-version-1 envelope](design.md#7-agent-interface).
Writes, coordination, and remote adapters are separate backlog work.

```sh
bl ls 'is:open priority:>=high' --sort priority,id --group status -n 20
bl ls --view Next
bl show 12
bl show tasks:BL-012 --section criteria --json
bl next 'label:cli' -n 3 --explain --json
```

## `bl ls [QUERY] [--view V] [--sort S] [--group G] [-n N]`

Lists native items, excluding archives. It includes closed items unless the
query excludes them. Query terms use [query.md](query.md); multiple positional
arguments are joined with spaces. `--view` selects an exact saved-view name.
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
both the message and `error.details.matches`. A missing ID exits 3
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

**Version contract:** `item.version` is the native codec's content hash, unchanged
from the source. Pass it verbatim to `--if-version` when write commands ship.
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
