# CLI

```sh
bl ls 'is:open priority:>=high' --sort priority,id --group status -n 20
bl ls --view Next
bl show 12
bl show tasks:BL-012 --section criteria --json
bl next 'label:cli' -n 3 --explain --json
```

Every command accepts `-C DIR` and `--json` (`-j`), before or after the command
name. JSON output is the [schema-version-1 envelope](design.md#7-agent-interface).
The `ls`, `show`, `next`, and write command interfaces are frozen.

`bl --help` lists item commands first, then the Automation, Coordination, and
Project groups. `bl COMMAND --help` (or `bl help COMMAND`, including
subcommands such as `bl help auth login`) shows arguments, options, and
examples. Bare `bl config` runs `config show`. `import`, `adapter`, and `auth`
without a subcommand print their help, or exit 2 with `--json`. A usage error
prints the problem and `Run bl COMMAND --help for usage.` on stderr.

Short flags: `-n` limit, `-s` status (`new`) or section (`show`, `edit`), `-b`
body, `-e` evidence, `-f` follow, `-y` yes, `-j` JSON, and `-C` directory.

## Version

`bl version` and `bl --version` print `bl VERSION (SHA, built TIME)`, with a
seven-character commit SHA and a UTC RFC 3339 build timestamp. Builds without
injected metadata report an unknown build time and omit the unknown commit.
With `--json`, `data` contains `version`, `commit` (unshortened), `build_time`,
and `go`.

## IDs

`12`, `012`, and `BL-12` all find `BL-012`. Numeric IDs use each native
source's prefix. `source:ID` picks one source. An ambiguous ID exits 2 and lists
the matches in the message and `error.details.matches`; so does an ID stored
both active and archived (pick one with a [path selector](#bl-renumber-from-to)).
A missing ID exits 3.

## Read commands

### `bl ls [QUERY] [--view V] [--sort S] [--group G] [-n N] [--workspace NAME]`

Alias: `bl list`.

QUERY uses the [query language](query.md); multiple arguments are joined with
spaces. Put `--` before a negated first term: `bl ls -- -label:wontfix`.

- Without an `is:` term, `is:open` is added. Use `is:closed` for closed items.
- Archived items load only for a positive `is:archived` term and are never ready.
- `--view` selects a saved view by exact name. Its query is ANDed with QUERY.
  Precedence for sort and group: flags, then the view, then query directives.
- Without a sort, `next` ranking orders items. Sorting runs before `-n`, then
  grouping. An item with several labels or assignees appears in each group.
- `-n` defaults to 0 (all). Negative values are usage errors.
- `--workspace NAME` reads every project in a [workspace](design.md#5-projects-configuration-and-trust)
  with its own statuses and trust. Items gain `project`, and `source` becomes
  `url.QueryEscape(project.id)/source-id`.

Partial remote scans still return results: fresh rows can be ready, but stale
rows and missing or stale blockers never are. Unknown coverage or error findings
fail the read.

```json
{"groups":[{"value":"todo","items":[{"source":"tasks","id":"BL-012","title":"Ready","status":"todo","category":"todo","priority":"high","version":"opaque-content-hash"}]}]}
```

An ungrouped result is one group with `value: ""`, even when empty. An empty
grouped result is `groups: []`. Every item has all eight fields; unset priority
is `""`. `status` is the raw provider status. IDs keep their padding.

### `bl show ID [--section S]`

Reads only the matched file and computes no readiness. Text output prints the
ID and title, each set field, then each nonempty section.

`--section` is one of `description`, `criteria`, `dod`, `plan`, `notes`,
`summary`, or `comments`. Text output prints only that section; JSON keeps the
metadata and only that section key. A missing section is `""`.

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

- `item` has the `ls` shape. `metadata` always has every key shown.
- References are source-qualified strings; unset ones are `""`. Edge arrays are
  `[]` when unset. `assignees`, `labels`, `refs`, and `files` may be `null`.
- `fields` is the raw frontmatter, including unknown keys.
- Native reads return stored edges only; inverse edges aren't computed.
- Nonempty criteria and DoD also appear as `criteria` and `dod` arrays of
  `{text, checked, evidence?}`. The Markdown sections include evidence lines.
- With coordination enabled, `data.resource` is the item's claim key, and
  `data.claims` lists the item's past and current claims from Worklease
  history, oldest first: `{claimId, session, agent, acquiredAt, endedAt?,
  endReason?, expiresAt?}`. An open claim has no `endedAt`. If history can't be
  read, `data.claimsError` says why and `show` still succeeds.
- `data.sessions` folds the item's [events](events.md) and claims by session,
  oldest first: `{id, agent?, agentSession?, host?, cwd?, branch?, firstSeen,
  lastSeen, phases?, claim?, handoff?, ended?, contact?}`. `phases` are the
  status categories the session's events saw. `ended` is `done` (the session
  closed the item), `released`, or `lapsed`; open sessions omit it. A
  claim-only session, such as one from another host, has only `claim`. If
  events can't be read, `data.sessionsError` says why and `show` still
  succeeds. Text output adds a Sessions section, one line per session.
- `contact` lists how to reach a session that holds its claim or was seen in
  the last 24 hours, looked up now and never stored (see
  [Contact](#contact)): one `{resolver, status, reason?, channels?}` per
  resolver, where `status` is `ok` or `unavailable` and each channel is
  `{channel, handle, status, detail?}` with a live `status` of `working`,
  `idle`, `waiting`, or `unknown`.

`item.version` is the source's opaque version: a content hash for files, a
composite fingerprint for GitHub. Pass it unchanged to `--if-version`.

### `bl changes ID`

Lists the item's linked worktrees, branches, pull requests, and landed commits,
furthest first ([Changes](changes.md)). Each line reads label (`#45` or the
branch), stage and state, checks, review decision, title, and URL, then why a
change is unknown or stale. JSON returns `data.changes`, each with `id`,
`stage` (`worktree`, `branch`, `pr`, `landed`), `state` (`open`, `draft`,
`merged`, `closed`, `unknown`, `stale`), `link` (how it was found: `branch`,
`title`, `closes`, `provider`, `trailer`, `merge`, `ref`), and optional
`reason`, `checks`, `review`, `branch`, `head`, `title`, `url`, `repo`,
`number`, `additions`, `deletions` (a branch or worktree's from its merge base
with the default branch, counting a worktree's uncommitted files), `worktree`,
`dirty`, `ahead`, `behind`, an open pull request's `checkRuns` (`name`,
`state`, `url`) and `unresolved` review thread count, `commits` (`sha`,
`subject`, `time`),
`events` (`kind`, `time`), `updated`, and `tour` (`ref`, `steps`, `stale`)
when a tour is stored for the change, stale once its head is another commit;
`data.providers` lists `git` and each forge with its `state` and
`reason`. Diff text stays out of JSON. Pull requests come from the forge's
cache, refreshed incrementally; Git is read from `.git` and never fetched.

### `bl tour ID [FILE]`

With FILE, stores a tour an agent wrote for the
item's linked change whose head is the tour's `ref`, for the TUI Diff tab
([schema](schemas/tour.json)). It refuses, with the reason, a file over 1 MiB,
an invalid tour, a step citing a criterion the item lacks, or a `ref` that
heads no linked change, listing the linked heads. It also refuses steps whose
file doesn't exist at `ref` (or, for `side: "old"`, before the change, where a
renamed file may use its old path) or whose line or range end is past the
file's end, listing every such step's index, file, lines, and the file's line
count in one refusal. The check reads Git objects only, never a worktree; steps on files
the change doesn't touch are accepted, and a `ref` the checkout doesn't hold
is stored unchecked. A later tour for the same
change replaces it. JSON returns `data.item`, `change`, `ref`, and `steps`.
`bl` never writes a tour itself; `bl instructions tours` explains how to.

Without FILE, prints the tour stored for each linked change: its change, ref,
step count, whether it is stale (the change's head is now another commit), and
its steps. JSON returns `data.tours`, each the stored tour (`item`, `change`,
`stored`, and the CodeTour `tour`) plus `ref` and `stale`; an item without a
stored tour returns an empty list.

### `bl next [QUERY] [-n N] [--explain] [--claim [--start]] [--workspace NAME]`

Returns ready items in [ranking order](readiness.md). QUERY narrows the
candidates, but blockers outside the query still count. `-n` defaults to 1;
0 means all. Sort and group directives are rejected. No candidates is success
with `items: []`.

```json
{"items":[{"source":"tasks","id":"BL-012","title":"Ready","status":"todo","category":"todo","priority":"high","version":"opaque-content-hash"}]}
```

`--explain` adds `explanations` for **every** item, included or not, in ranking
order (omitted on an empty snapshot):

```json
{"items":[],"explanations":[{"ref":{"source":"tasks","id":"BL-13"},"readiness":"blocked","reasons":[{"code":"blocker","refs":[{"source":"tasks","id":"BL-012"}]}],"priority":"","unblockImpact":0,"included":false}]}
```

Entries may also carry `rank` and `unblocks` (up to two refs); reasons may carry
`refs` or `values`. A ready item left out by QUERY or `-n` gets a `query` or
`limit` reason. Text output prints the items, then one wrapped line per
explanation:

```text
BL-1  todo  high  Debounce detail rendering

BL-1  ready       high · unblocks 2 (BL-2, BL-3)
BL-2  ineligible  unset · unblocks 0 · status: active; blocker: BL-1
BL-3  blocked     unset · unblocks 0 · blocker: BL-1
```

`--workspace NAME` ranks each item by its own project's policy and is read-only;
claim from the owning project. For `--claim`, see [Coordination](#coordination).

## Write commands

All writes accept `--if-version V`. A stale version exits 4 (`conflict`) and
changes nothing. Without the flag, a write is still checked against the version
it read, under the source's write lock for local files. Native writes are atomic
and preserve unchanged lines, unknown fields, comments, and quoting. GitHub and
Linear and [Jira Cloud](jira.md) check a fresh version before one mutation, but have no compare-and-swap,
so a concurrent edit can still race ([GitHub](github.md#writes-and-claims)).

`new` and `edit` return the item as `data`. Other writes return one outcome per
ID, in input order:

```json
{"outcomes":[{"id":"12","item":{"source":"tasks","id":"BL-012","title":"Ready","status":"todo","category":"todo","priority":"high","version":"new-hash"}},{"id":"999","error":{"reason":"not-found","message":"item not found: 999","details":{}}}]}
```

Every ID is attempted. Each item is atomic; the batch is not. If any ID fails,
the envelope is `ok: false` with the first failure's error and exit code, and
keeps `data.outcomes`. `--if-version` with several IDs exits 2 before writing.
Text output prints each written item, then `ID: message` for each failure.

### `bl new TITLE [TOKENS] [--status S] [--body TEXT|-] [--ac TEXT]... [--source SOURCE] [--sprint S]`

Alias: `bl create`.

```sh
bl new 'Fix login redirect !h @me #bug ^BL-1 <BL-2 ~m-1 :bug' --ac 'Redirect keeps the path'
echo 'Long description' | bl new 'Write docs' --body -
```

- [Quick-add tokens](query.md#shared-quick-add-parser) set fields. `@me` uses
  your identity in the target source: its account `user` for a provider,
  else `me.default`.
- `^` and `<` references accept ID shorthand and store the target's real ID
  (`<1` stores `BL-1`, or `BL-001` if that file exists). An unqualified ID
  missing from `--source` but found in exactly one other native source is
  stored qualified (`<API-1` stores `api:API-1`). A missing target is stored as
  written; `bl check` reports it, and `bl new` warns about a blocker that
  matches no item or several.
- Each `--ac` adds one criterion; commas stay in the text.
- `--status` takes a status key. `--status draft` creates an item that is never
  ready. An unknown key exits 5 and lists the allowed keys. The default is the
  first todo-category status, else the first status.
- `defaults.type` and `defaults.dod` from config apply to native and Backlog.md
  sources; Linear has no type or DoD, so its creates omit them.
- The source is the first of: `--source` or a `+ID` token (both given and
  different exits 2), `defaults.source`, or the only source that can create.
  Otherwise, or for an unknown source or one that can't create, `bl new` exits
  2 and lists the sources that can.
- `--sprint` takes `current`, `next`, `none`, a sprint name, or a key, resolved
  like `bl set sprint=`. Only Linear sources create with a sprint.
- `--if-version` on create always conflicts.
- A create that leaves an item behind but fails a later step, such as a Linear
  blocker relation, exits 6 with `unknown-outcome` and names the item in the
  message and `error.details.created`. Check it before creating again.

### `bl set ID... FIELD=V FIELD+=V FIELD-=V [--yes]`

```sh
bl set 12 status=in-progress priority=high label+=bug blocked_by+=BL-7
bl set 12 13 milestone=
```

IDs come first. All assignments to one item succeed or fail together. Jira
accepts one field per request; `status` must be a discovered destination name.
Jira single-assignee replacement (including clearing) requires `--yes` (MCP
`update.confirm: true`), and accepts zero or one accountId. See [Jira setup](jira.md).
`sprint` takes `current`, `next`, `none`, a sprint name, or a key, resolved
against the source's catalog; only Linear and Jira sources accept it, and a
sprint changed since the read fails with `conflict`.

| | |
| --- | --- |
| Fields | `title status type priority assignees labels milestone sprint parent blocked_by related duplicate_of rank due refs files` (`label`, `assignee` are aliases) |
| `=` | Set. Lists take comma-separated values. An empty value clears. |
| `+=`, `-=` | Add or remove comma-separated list entries; idempotent |
| References | `parent`, `blocked_by`, `related`, `duplicate_of` expand ID shorthand like `new` |

Unknown fields, statuses, and priorities exit 5 and list allowed values. A
reference that isn't an item ID exits 5 naming the field and value. Types,
labels, and milestones are free text.

### `bl start ID` and `bl done ID`

Set the status to `workflow.start` or `workflow.done` if the item's source
accepts it, else to the source's only active or done status, else to its
default (GitHub: `closed`). Anything else is refused naming the source
([details](format.md#frontmatter-keys)). One ID each.

### `bl note ID TEXT|-` and `bl comment ID TEXT|- [--author A]`

Append a paragraph to Notes, or a timestamped comment. `-` reads stdin. The
author defaults to `me.default`; without it, pass `--author`. GitHub, Linear,
and Jira attribute a comment to the source account's `user` instead, so neither
is needed, and any other `--author` is `rejected`. Comments can't be edited
through the CLI.

### `bl handoff ID TEXT|- --session S [--if-version V]`

Records what this session did, tried, and left for the next person or agent as
a `session.handoff` [event](events.md) with the session's identity. It needs a
session (exit 2, `session-required`) but no claim, so PR and review sessions
can record one. Text is trimmed; over 4 KiB is `invalid-value` (exit 5), never
truncated. `bl show` lists each session's newest handoff under its Sessions
line, with terminal controls stripped.

By default nothing is written to the item, and `--if-version` is ignored. With
`history.handoff: comment` in any config layer, the handoff is also a comment,
as `bl comment` writes, checked against `--if-version` or the version read:

```text
Handoff from claude on 2026-10-29

TEXT
```

The comment names only the agent kind and date, never the host, checkout,
branch, or session IDs. A refused comment records nothing. An
`unknown-outcome` comment still records the handoff, and the error's `data`
names the event. A source that can't comment records locally.

```json
{"id":"12","event":"01M64Z3BHTVDFPPBF7YNE5VHV5","comment":"posted","item":{"source":"tasks","id":"BL-012","title":"Ready","status":"todo","category":"todo","priority":"high","version":"new-hash"}}
```

`comment` is `posted` or `unsupported` under `history.handoff: comment`, and
absent otherwise.

A skill or hook that works an item without claiming it, such as PR follow-up
or review, can record a handoff for the item its PR's head branch names
(`bl-12-fix-login` is `BL-12`). Run it in a checkout of the PR's repository. It
does nothing outside a `bl` project:

```sh
top=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
branch=$(gh pr view 41 --json headRefName --jq .headRefName)
id=$(printf '%s\n' "$branch" | sed -nE 's/^([A-Za-z]+-[0-9]+).*/\1/p' | tr '[:lower:]' '[:upper:]')
if [ -f "$top/backlog.yaml" ] && [ -n "$id" ]; then
  printf '%s\n' "Reviewed PR 41; asked for a retry test. Next: rerun task ci after the fix." |
    bl handoff "$id" - --session "${CLAUDE_CODE_SESSION_ID:?}"
fi
```

Read the PR's head branch, not the current one: a review may run from another
branch, and a worktree at the PR head may be detached. A branch that names no
item exits 3 and records nothing. Pass your harness's own session ID, as for a
claimed loop.

### `bl ac ID ...`

```sh
bl ac 12 check 1 3
bl ac 12 uncheck 2
bl ac 12 check 1 --evidence 'go test ./... passed' --if-version VERSION
bl ac 12 add 'Handles empty input'
bl ac 12 set 2 'Handles empty and blank input'
bl ac 12 rm 2
bl ac 12 check 1 --dod --evidence 'task ci passed'
```

Indices start at 1. `--evidence` takes one index and one line, written as an
indented `Evidence:` line under the criterion (replacing any old one). Checking
without it keeps evidence; unchecking removes it. `set N TEXT` rewords one
entry in place, keeping its check state, position, and evidence. Criteria renumber when one is
removed, and evidence moves with its criterion. Always pass `--if-version` when
the indices came from an earlier read. `--dod` applies the same operations to
definition-of-done items, which are numbered separately from criteria.

### `bl edit ID [--section S [--text TEXT|-]]`

Opens the native file, or one section, in `$EDITOR` (default `vi`, run through
the shell so arguments work; on Windows, default `notepad`, split into arguments
without a shell). Sections: `description`, `criteria`, `dod`,
`plan`, `notes`, `summary`. Editor output goes to stderr, so JSON on stdout stays
clean.

`--text` replaces the section without opening an editor; `-` reads stdin. It
requires `--section` and fails like any other write.

```sh
bl edit 12 --section plan --text - --if-version VERSION < plan.md
```

| Outcome | Exit | Result |
| --- | --- | --- |
| Saved with changes | 0 | New version |
| No changes | 0 | Nothing written |
| Someone else saved first | 4 | Their file kept |
| Invalid frontmatter or values | 5 | Original kept |

On failure, your buffer is kept; the message and `error.details.editedPath` name it.

### `bl archive ID...` and `bl unarchive ID...`

Move native items into or out of `archive/` without changing their bytes or
version. An occupied target is a conflict. Archived items stay readable by ID
but leave lists and `next`. Linear sources with `allowWrites` archive and
unarchive the issue in Linear ([linear.md](linear.md#writes-and-claims)).

## Coordination

Enable with `coordination: {worklease: {}}` (Worklease 1.10.1 or later, or
1.13.0 on Windows).

```sh
bl next --claim --start --session loop-a --json
bl heartbeat --session loop-a --json
bl done BL-012 --session loop-a --if-version VERSION --json
```

The session comes from `--session`, then `BL_SESSION`, then
`WORKLEASE_SESSION_ID`; without one, the TUI uses a session per item, `tui:` plus a hash of its claim, so a person can hold several claims. `WORKLEASE_RUN_ID` requests run verification. MCP
instead takes `session` and `run` on each call. Use one stable session per loop,
distinct from every other loop.

Events record the agent running the session. `BL_AGENT=KIND` names it;
otherwise the first `agents:` entry, in key order, whose `session` variable is
set wins, and that variable's value is the agent's own session ID. Built-ins
detect `claude` (`CLAUDE_CODE_SESSION_ID`), `codex` (`CODEX_THREAD_ID`), and
`pi` (`PI_SESSION_ID`). Add kinds in user config; `backlog.yaml` and
`backlog.local.yaml` can't set `agents:`, and `bl doctor` warns if they try.
`resume` and `fork` reopen a recorded session ([`bl resume`](#bl-resume-id-session---fork--resume)).

```yaml
agents:
  mine:
    session: MINE_SESSION_ID
    resume: [mine, --resume, "{agentSession}"]
```

### Contact

`bl show` looks up live ways to reach each held or recent session from its
recorded agent, agent session, and host. `bl` never sends a message; use the
channel's own tool.

| Resolver | Source | Handle |
| --- | --- | --- |
| `herdr` | `herdr agent list`, matching the pane's agent session | pane ID, for `herdr agent prompt` |
| `claude-peer` | Claude Code's `sessions/*.json` under `CLAUDE_CONFIG_DIR` or `~/.claude`, for `claude` sessions | peer name, for `SendMessage` |

Built-ins report `unavailable: other host` for a session recorded on another
host. All resolvers share a 100 ms budget, and a slow one reports
`unavailable: timeout` without hiding the others. Text output adds a
`contact:` line under the session.

User config can add resolvers, replace a built-in by name, or disable one with
`false`; `backlog.yaml` and `backlog.local.yaml` can't set `contact:`. A
resolver is an argv run without a shell in the hook environment (PATH, HOME,
the XDG directories, and USERPROFILE on Windows), with `{agent}`, `{agentSession}`, `{session}`,
`{host}`, and `{cwd}` checked like hook placeholders. It prints a JSON array of
channels on stdout:

```yaml
contact:
  hcom: [hcom-resolve, "{agentSession}"]   # prints [{"channel":"hcom","handle":"…","status":"idle"}]
  claude-peer: false
```

Every Worklease call runs from the control root, even from a linked worktree.
`WORKLEASE_HOME` and `WORKLEASE_PROFILE` pass through; inherited
`WORKLEASE_SESSION_ID`, `WORKLEASE_HANDLE`, and `WORKLEASE_RUN_ID` don't.

### Remote authority

To coordinate across hosts, run one `worklease serve` and enroll each host
(see Worklease's remote claim authority guide). Enrollment makes the remote
profile the user default; select another with `worklease profile bind NAME` in
the repository or `WORKLEASE_PROFILE=NAME`. `bl` commands don't change:

```sh
worklease enroll --invite-file PATH
worklease profile show            # remote profile and its selection source
bl next --claim --start --session loop-a --json
```

Every clone must share the project ID, which defaults to the `origin` remote.
Without one, the ID is host-local and other hosts would derive different keys,
so `bl` refuses to acquire through a remote profile (exit 6, `unavailable`)
before writing, and `bl doctor` fails its `worklease` check. Heartbeat and
release still work. Set `project.id` in `backlog.yaml` or add `origin`.

### Claimed selection

`bl next --claim [--start]` returns at most one item:

1. Inside a verified run, it returns that run's item without acquiring, or
   `claim-held` once the item is closed. The supervisor renews and releases.
2. If the session holds an open item, it returns it with `resumed: true`,
   even if the item no longer matches.
3. If the session has a pending acquire, it replays the exact request. A
   still-uncertain replay stops with `unknown-outcome`.
4. Otherwise it walks one ranked snapshot without waiting. Contention skips to
   the next item; an uncertain acquire stops with `unknown-outcome`.

If the session's last claim expired, `bl` archives its handle with
`worklease handle archive` and continues as if the session held nothing, so the
same session can claim again. A pending or recovery handle, or an inactive claim
that hasn't expired, still refuses as unresolved.

A held closed item (`claim-held`), a missing item, or unresolved state stops
selection. Don't switch sessions to get past it. `--claim` without a session
exits 2 (`session-required`). `--start` needs `--claim`; a failed start keeps
the claim. JSON lists every attempt with separate claim and status outcomes.

### `bl claim`, `bl heartbeat`, `bl release`

```sh
bl claim 12 --session loop-a --json
bl heartbeat --session loop-a --json
bl heartbeat --session loop-a -- task ci
bl release 12 --session loop-a --json
```

| Result | Exit | Reason |
| --- | --- | --- |
| Claimed by someone else | 7 | `contention` (names the holder) |
| Session already holds another item | 4 | `claim-held` |
| No session | 2 | `session-required` |
| Run doesn't verify | 4 | `run-mismatch` |
| Verified run owns the claim | 0 | outcome `run-managed`; nothing changes |
| Release of a claim you don't hold | | outcome `not-held` |

`claim` never waits. `heartbeat` reports `expiresAt`. With `-- COMMAND`, it
renews, runs the command with bl's streams, renews again at half the remaining
lease until the command exits, and exits with the command's status. A failed
renewal warns on stderr and retries sooner, never stopping the command; after the
lease lapses it stops renewing. If the first renewal fails, the command doesn't
run. Text output looks like
`BL-12 acquired until 2030-01-01 00:00:00`.

```json
{"schemaVersion":1,"ok":false,"command":"claim","data":{"outcome":"contended","reason":"contention","message":"resource claimed (holder agent@host)","claim":{"claimId":"holder","resources":["RESOURCE"],"session":"other","agent":"agent@host","expiresAt":"2030-01-01T00:00:00Z"}},"error":{"reason":"contention","message":"resource claimed (holder agent@host)","details":{}}}
{"schemaVersion":1,"ok":true,"command":"release","data":{"outcome":"released","resource":"RESOURCE","claim":{"claimId":"claim","resources":["RESOURCE"],"session":"loop-a","agent":"agent@host","expiresAt":"2030-01-01T00:00:00Z"}}}
{"schemaVersion":1,"ok":true,"command":"heartbeat","data":{"outcome":"renewed","expiresAt":"2030-01-01T00:00:00Z"}}
```

### Status changes

The same rules apply to `start`, `done`, `set status=`, the TUI, editor restores,
and MCP `update`:

| Transition | Order | On failure |
| --- | --- | --- |
| Into an active status | Claim, then write | Contention writes nothing. A failed write keeps the claim. |
| Into done or canceled | Write, then release your claim | A failed release leaves the status written. |

Activating several items at once is refused. Unresolved claim state refuses the
write. JSON outcomes add
`transition: {status: "not-written"|"failed"|"written", claim: {...}}`, even on
error. Check both before retrying.

### Resource keys

`bl show --json` reports `data.resource`. Native and Backlog.md keys use the
project ID and item ID, never a worktree path. GitHub and Linear keys match
Worklease's own policies. Regenerate the pinned test vectors with
`mise exec -- python3 scripts/worklease-key-vectors`.

### Provider evidence and recovery

`bl` requires Worklease's schema-v2 JSON, which exposes exact resource
identities, and guarded release (`release --if-claim-id ID`), which binds a
release to the verified claim. Worklease 1.10.1 is the minimum; 1.9.3 is only
the key-policy baseline. Windows needs 1.13.0, the first Windows release: with
an older Worklease there, claim commands and active transitions refuse
`unavailable` naming that version.

Anything `bl` can't verify fails closed:

- A redacted acquire is `uncertain` (exit 6) and keeps the claim.
- Unusable session inspection is unresolved and refuses selection.
- An unusable bulk claim list makes readiness unknown.

Pending acquires are replayed by Worklease from its retained request; `bl` never
rebuilds credentials, TTL, agent, key, or deadlines. Worklease doesn't expose
local pending options, so local recovery may need the original Worklease command.
Corrupt, foreign, stale, or unverifiable state never permits a fresh acquire.
Inspect and recover the same session instead.

## Automation

### `bl action NAME ID [--yes|-y] [--dry-run]`

Runs a trusted configured action on one matching item. `confirm: true` actions
need an interactive yes or `--yes`; otherwise they refuse without running. JSON
`data` has `event` (the `action.finished` event), optional `message`, and
`detached: true` for background actions. See [Automation](automation.md).

`--dry-run` resolves `when` and expands placeholders through the same path as a
run, then prints the argv, cwd, and environment variable names. It runs nothing,
appends no event, and needs no `--yes`. JSON `data` has `name`, `ref`, `argv`,
`cwd`, and `env`. A disabled action fails as a real run would (exit 5) with the
reason, such as the placeholder that could not expand.

```text
argv: wt switch --create bl-12-debounce-detail-rendering
cwd:  /home/me/project
env:  BL_BRANCH BL_EVENT_ID BL_EVENT_ORIGIN BL_HOOK_DEPTH BL_ITEM_ID BL_ITEM_REF BL_PROJECT_ID BL_RESULT BL_ROOT BL_SOURCE HOME PATH
```

### `bl resume ID [SESSION] [--fork|--resume]`

Reopens an agent session that worked the item, with its agent's `resume` or
`fork` command from user config, in the session's recorded checkout. SESSION is
a session ID or the agent's own session ID; without it, the only resumable
session is used, and several fail with `usage` listing the candidates. A closed
item forks when `fork` is configured; `--fork` and `--resume` choose. The agent
gets the terminal, so `--json` writes its output to stderr. JSON `data` has
`event` (the `action.finished` event) and `detached`.

Refusals exit 4 and list the candidates in `error.details.candidates`:

| Reason | Meaning |
| --- | --- |
| `other-host` | The session ran on another host |
| `checkout-removed` | The recorded checkout no longer exists |
| `live` | A session on the same agent conversation still holds its claim, or a [contact](#contact) channel reports its agent running at any age; the message lists the channels |
| `no-agent-config` | The agent has no `resume` or `fork` command |

A command that fails exits 5 with its exit status and, under `--json`, its
stderr tail. See [Automation](automation.md#agent-sessions).

### `bl history ID [--limit N] [--cursor C]`

Prints the item's history, oldest first: its events, claims (from Worklease,
including other hosts), sessions folded from both, comments, and the commits
that touched its file (native and Backlog.md sources, following renames), and
provider activity (GitHub timeline, Linear history, Jira changelog), and its
linked changes. Each line
reads time, source label (`item`, `claim`, `agent`, `handoff`, `comment`,
`change`, `git`, `action`, `hook`, or the provider, such as `github`), and
summary. A change's row spans it and its events and landed commits follow it,
indented. An
edit's summary names each changed field, such as `labels +windows -human` or
`criteria 2/5 → 3/5`, and its `detail` holds what the summary abbreviates:
per field, lines marked `+ ` or `- ` (`…` for skipped unchanged lines) and
criteria marked `[x] ` or `[ ] `. A session's entries follow it, indented, and
its row names live contact channels, looked up when read. A provider that can't answer is a dim
line, never a failure. JSON returns `data.entries`, each with `id`, `time`,
`kind`, `summary`, `provider`, and optional `actor`, `session`, `group` (the
`id` of the entry it nests under), `span` (`start`, `end`, `state`, on group
heads), `detail`, and `resumable` (on a session row `bl resume` can reopen);
`data.providers` lists each provider's `state` (`ok`,
`partial`, or `unavailable`) and `reason`. `--limit` pages; pass `data.cursor`
back as `--cursor` while `data.more` is true. An unknown cursor is `not-found`.
A cursor is `conflict` when entries before it changed, such as one added to an
earlier session; page again from the start.
Commits come from `git log --follow` with external diff, textconv, and
fsmonitor off, in the minimal hook environment, cached by item version and HEAD
(at most the newest 1,000, reported as `partial`).

### `bl events [--follow|-f] [--since ID] [QUERY]`

Reads the project event log. JSON returns `data.events`; `--follow` prints one
envelope per line with `data.event`. `--since` is exclusive; an unknown ID is
`not-found`. See [Events](events.md).

## Project commands

### `bl init [--prefix P] [--path DIR]`

Creates `backlog.yaml` and the item directory (default prefix `BL`, path
`backlog`, relative to the control root). The config declares `draft`, `todo`,
`in-progress`, and `done` with a matching workflow. `project.id` is written only
when it comes from a shared `origin` remote. Run inside Git. An existing config
is never overwritten (exit 5, nothing created).

### `bl import backlog-md [PATH] [--to DIR | --in-place] [--dry-run]`

Imports Backlog.md files into a native project. PATH defaults to the current
directory and may be a repository root or backlog directory. `--to` defaults to
`backlog-import`. The destination must be empty or absent, even for a dry run
(exit 5 otherwise), and the source is unchanged. `--in-place` instead writes
into the source project and removes the committed Backlog.md files.
`--dry-run` prints the report without writing. See [Import](import.md).

### `bl config [show]`

Loads settings without running anything or contacting sources. JSON `data` holds
the effective non-secret settings plus `directory`, `paths`, `discovery`,
`hostLocal`, `provenance`, and `privileged`:

```json
{"ui":{"highContrast":false},"provenance":{"ui.highContrast":{"layer":"project","file":"/repo/backlog.yaml","line":4}},"privileged":[{"key":"hooks[0]","trusted":false,"origin":{"layer":"project","file":"/repo/backlog.yaml","line":8}}]}
```

Provenance layers are `built-in`, `discovery`, `user`, `project`, `local`,
`user-project`, `environment`, and `flag`. Implicit defaults (such as
`milestones[0].archived: false`) are `built-in`. `privileged` lists hooks,
actions, and program or network sources with their trust state. Credentials are
never printed. `backlog.yaml` is overlaid by `backlog.local.yaml`, then user
`projects.<id>` overrides. Both repository files use the project schema and
control-root routing; `root: checkout` selects the invoking checkout. Each local
value reports layer `local`, its file, and line. Either file marks a project.
Legacy `.config/backlog.yaml` is rejected with
`git mv .config/backlog.yaml backlog.yaml`.

### `bl trust [--show]`

`--show` prints the control root, digest, declarations, and trust markers
without granting anything. `bl trust` records consent under
`~/.local/state/backlog/trust/` (`$XDG_STATE_HOME`). JSON `data` has `root`,
`digest`, `granted`, `entries`, and any `hooks`, `actions`, and `sources`.

Consent covers the control-root path, project ID, and every field of the
project's effective shared and local privileged declarations (argv, cwd, conditions, subscriptions,
timeouts). Key order, comments, and other settings don't affect it. User config
is always trusted. Trusting runs nothing.

Untrusted programs and network sources stay inert everywhere. The TUI shows a
banner and still browses native sources.

### `bl doctor [--online]`

```text
pass  config        Configuration is valid
pass  trust         All effective privileged declarations are trusted
pass  hooks         Trusted hooks and actions resolve
warn  control-root  Default branch is unknown (control checkout: master)
                    Fix: Set the local origin/HEAD symbolic ref to the repository's default branch
skip  local-config  No backlog.local.yaml
pass  cache         Configured native caches are healthy or not yet created
pass  worklease     Worklease 1.10.1 (schema v2)
warn  claims        Active without a live claim: BL-7
                    Fix: Resume each with bl claim ID, or move it to todo, done, or canceled
pass  sources       Sources are configured; no network checks requested
pass  accounts      Provider source accounts are ready; reads verify them
```

Ten checks: `config`, `trust`, `hooks`, `control-root`, `local-config`, `cache`, `worklease`, `claims`, `sources`, `accounts`.
Each has a `status` (`pass`, `warn`, `fail`, `skip`), `message`, and `fix`. JSON
`data.checks` lists all of them. Any failure exits 5 (`rejected`); warnings
alone exit 0. Invalid config fails `config` and skips the checks that need it.

- `hooks` fails when a trusted hook or action has a `run[0]` that is not on
  `PATH` (or an absolute path that is not executable), an unknown placeholder,
  `{result.KEY}` outside a hook on `hook.finished` or `action.finished`,
  `{resource}` without coordination, an empty `run`, an invalid `timeout` or
  `when`, or an unknown `on` event type. Each finding names the hook or action.
  Checks that load already enforces, such as `phase` and `if`, are not
  repeated. Untrusted declarations are reported as inert (warn) and are not
  inspected. Item-dependent expansion is left to `bl action --dry-run`.
- `control-root` compares the branch with local `origin/HEAD`, then
  `init.defaultBranch`, then `main`/`master`. It never fetches.
- `local-config` warns when `backlog.local.yaml` is tracked or not ignored.
  Keep it untracked and ignore `/backlog.local.yaml`; `bl` never writes it or
  edits `.gitignore`, and auto-commit cannot include it.
- `cache` reads existing caches without repairing them.
- `worklease` needs `version --json` to report schema v2 and version 1.10.1 or
  later (1.13.0 on Windows). A missing binary warns, or fails when coordination
  is configured.
- `claims` warns about native and Backlog.md items in an active status whose
  claim is missing or expired, such as work an agent abandoned. `bl` never
  reclaims them: a person resumes, resets, or closes each one. It skips
  without coordination or when the claim list is unavailable.
- `sources` checks native and Backlog.md directories and prefixes. Remote
  sources are checked on read, not here; credential helpers never run.
- `accounts` fails when a GitHub, Linear, or Jira source's account is missing
  from user config or can't authenticate, by the same offline rules as
  [`bl auth status`](#bl-auth-status-provider-account-and-bl-auth-loginlogout-provider-account).
  It skips untrusted sources, which are inert.
- `--online` allows network probes after trust. None are defined yet.

### `bl check [--fix]`

Validates every active and archived native item. Findings have `code`,
`severity`, `path`, `line`, and `message` (JSON `data.findings`, `[]` when
clean). Errors exit 5; warnings don't.

`--fix` applies only the [repairs in the format spec](format.md#validation):
filename renames, `updated`-only and comment-only merge conflicts, and line
endings. It holds the write lock, then reports what remains.

### `bl renumber FROM TO`

```sh
bl renumber BL-9 BL-31
bl renumber 1 7
bl renumber tasks:archive/BL-9.md BL-32
```

Renames an item and rewrites every `parent`, `blocked_by`, `related`, and
`duplicate_of` reference to it in active and archived items. A numeric TO takes
the source's prefix. When an active and an archived item share an ID, select one
by path (`tasks:BL-9.md` or `tasks:archive/BL-9.md`). An existing target,
ambiguous FROM, or invalid item file stops the rename before anything changes.

### `bl adapter check [--timeout 5s] EXEC [ARG...]`

Runs an external adapter and checks negotiation, required capabilities, a
bounded list page, and a read of the first item. Put `--` before EXEC if its
arguments start with `-`. Timeout is at most one minute. Success prints
`Adapter protocol v1: PASS` (JSON `{"protocolVersion":1,"valid":true}`); failure
uses reason `adapter-check`. It doesn't trust configured sources. See the
[protocol](external-adapter-protocol.md).

### `bl instructions [TOPIC]`

Prints Markdown guidance matching the installed version. Topics: `overview`
(default), `workflow`, `query`, `format`, `json`, `tours`. Unknown topics exit 2. JSON
`data` has `topic`, `topics`, and `markdown`.

Guidance never needs a project. In one whose config sets
`history.commitTrailer: KEY` (for example `Refs`), `workflow` adds one line
asking agents to end each commit for an item with `KEY: ID`. Unset, it says
nothing about commits. A config that fails to load is ignored here.

### `bl auth status [PROVIDER [ACCOUNT]]` and `bl auth login|logout PROVIDER ACCOUNT`

```sh
bl auth status                     # every account in user config
bl auth login linear work          # read access
bl auth login linear work --write  # read and write
bl auth logout linear work         # delete local tokens and cache; revoke if supported
```

Works for accounts with `oauth: true` in user config, for `linear`
([PKCE](linear.md#oauth-login)) and `github` ([device login](github.md#oauth-login)).
Login needs a terminal; MCP, claims, hooks, and actions never start it. Logout
says when you must revoke access manually in provider settings. Tokens live in
owner-only XDG state. See [OAuth](oauth.md).

`status` lists each account's provider, name, user, method (`credential`,
`env`, `oauth`), and state, offline: a helper must be on `PATH` but never runs,
an environment variable must be set, and an OAuth account needs a stored grant
for its configured user that is refreshable or unexpired. It shows `read` or
`read+write` and when a non-refreshable grant expires. Reads still verify the
principal. Any account that can't authenticate exits 5 (`rejected`) and prints
a fix; an unmatched filter exits 3. JSON `data.accounts` has `provider`,
`account`, `user`, `method`, `ready`, `state`, `writes`, `expiresAt`, and `fix`.

For login and logout, JSON returns `provider`, `account`, write-consent state, and for logout whether
revocation was remote or manual. Failures use the [OAuth reason codes](oauth.md#errors)
and never include provider bodies or tokens.

## Output

```text
BL-1  todo         high  Debounce detail rendering
BL-2  in-progress        Add board layout
BL-3  todo               Third
```

- Columns align across rows. Priority is dropped when no row has one.
- IDs are bare, or `source:ID` with several sources, or `project source:ID` in
  a workspace.
- Rows fit the terminal width (80 columns when piped), counting wide characters.
- Grouped `ls` prints `value (count)` before each group, `none` for unset.
- `No items.` prints only on a terminal. `next` with nothing ready always says
  so, with a hint to use `--explain`: on stdout at a terminal, otherwise on
  stderr.
- Colors follow the terminal palette. `NO_COLOR`, `TERM=dumb`, and pipes print
  no escape sequences.
- Text output strips terminal escape sequences and control characters; table
  cells flatten tabs and newlines. JSON keeps original strings.

Read commands never run hooks, claim, or write; only `next --claim` does. An
unsupported source type or incomplete native load exits 6 (`unavailable`) rather
than return a partial ranking. A detected but unconfigured Backlog.md project is
read-only; a detected Beads checkout isn't loaded until configured.

Golden JSON lives in `internal/cli/testdata/{ls,show,next}.json`. Regenerate with
`task cli:test PKG=./internal/cli -- -update` and review the diff.

## Native read cache

`ls` and `next` keep a disposable SQLite cache in
`~/.cache/backlog/projects/<project-id-hash>/` (`$XDG_CACHE_HOME`), one database
per source. Files are revalidated by size, mtime, inode, and ctime; only changed
files are reparsed. A corrupt or incompatible cache is rebuilt with a warning,
rows from a different `bl` build are reparsed, and an unavailable cache falls
back to the files. Deleting the cache only slows the next read. `show` and
writes always read the files.

## Git auto-commit

```yaml
git:
  autoCommit: true   # default false; requires bl trust in project config
```

Each CLI command that changes native items (including `check --fix` and
`renumber`) makes one commit of only those files, in the control checkout. MCP
commits once per request; the [TUI](tui.md#git-commits) batches edits. The
message is `docs(backlog): <verb> <ID> <title>`: the command name (or mutation
for TUI and MCP) and the batch's last item.

`bl` never fetches, pulls, pushes, or bypasses hooks. It skips the commit with a
warning, keeping the file changes, when:

- a target file was already dirty, changed externally, or staged mid-batch;
- a merge, rebase, or cherry-pick is in progress, or HEAD is detached;
- the repository is busy, Git is missing, a hook fails, or Git takes over 30 s.

Skipped commits aren't retried. Hooks must not edit the backlog while the commit
holds the repository lock. `init`, import, user config, and remote writes never
auto-commit.
