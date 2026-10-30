# Linear

## Setup

Use immutable UUIDs for the organization, team, optional project, viewer, and
states, never names or display IDs. Name the source `id` after the team, such as
`eng` or `product`: it's the name people and agents filter by (`source:product`).
Project config (`backlog.yaml`):

```yaml
sources:
  - id: eng
    type: linear
    site: ORGANIZATION_UUID
    team: TEAM_UUID
    # project: PROJECT_UUID  # optional extra scope
    account: work
    allowWrites: true       # default false; changing it needs re-trust
statuses:
  - {key: TODO_STATE_UUID, name: Todo, category: todo}
  - {key: ACTIVE_STATE_UUID, name: In Progress, category: active}
  - {key: DONE_STATE_UUID, name: Done, category: done}
workflow: {start: ACTIVE_STATE_UUID, done: DONE_STATE_UUID}
```

User config (`~/.config/backlog/config.yaml`):

```yaml
accounts:
  linear:
    - name: work
      user: VIEWER_UUID
      credential: [security, find-generic-password, -s, linear-api, -w]
      # or: env: BL_WORK_LINEAR_API_KEY
```

Then run `bl trust`. Find the UUIDs with GraphQL:

```graphql
{ viewer { id organization { id } } }
{ teams { nodes { id name } } }
{ team(id: "TEAM_UUID") { states { nodes { id name type } } } }
```

Linear state types map to categories: `backlog` → backlog, `unstarted` → todo,
`started` → active, `completed` → done, `canceled` and `duplicate` → canceled.
Raw names, types, priority, and UUIDs stay in item fields. Display IDs like
`ENG-431` are separate from the issue UUID.

Status and assignee values are UUIDs, but `bl` displays the state and user
names: in the TUI list, board, detail, pickers, and filter completion, and in
CLI text. A configured status name wins. Stored values, `--json`, queries,
`assignee:me`, claims, and writes keep the UUID; pickers and completion insert
it, so you rarely need to look one up. `assignee:me` and the Mine view match
the account's `user` UUID, not `me.default`.

### API key

Create a personal API key in Linear Settings → Security & access → Personal API
keys, limited to the teams you need. Give it read access, plus issue creation,
issue update, and comment creation for writes. (The live test also needs issue and relation
create/delete in the Test team.) Workspace and team restrictions still apply.

Keep the key in a credential manager, never in config, arguments, screenshots,
or logs. The helper must print one line within 15 seconds and 4 KiB. Every
refresh and write checks that the key belongs to `user` and can see the
organization and team. See [remote sources](remote.md).

### OAuth login

Replace `credential`/`env` with `oauth: true` (keep `user`), then from a
terminal:

```sh
bl auth login linear work
bl auth login linear work --write  # adds write after read consent
bl auth logout linear work
```

Open the URL in a browser on the same machine. `bl` uses user authorization
(`actor=user`) with S256 PKCE and a random state, and listens on
`http://127.0.0.1:47653/oauth/callback` for up to five minutes; the port must be
free. There's no client secret or hosted callback, so remote or headless sessions
can't complete login. Unattended commands never start it.

Login asks only for `read`. `--write` adds `write` (never `admin`); writes also
need `allowWrites: true` and Linear permission. If Linear returns an older,
broader grant to a read-only login, login refuses it: revoke that grant in
Linear settings and retry. Each login and refresh checks the viewer UUID; source
use also checks the organization and team.

Tokens rotate under a cross-process lock and are stored, with granted scopes, in
owner-only XDG state before validation. Logout revokes the refresh grant,
deletes local tokens, and clears the account's cache; if revocation fails, it
says to revoke in Linear settings. See [OAuth](oauth.md).

## Sync

| Scan | When | Reads |
| --- | --- | --- |
| Change window | Every minute | Issues updated or archived/trashed since the last scan's start, minus two minutes |
| Reconciliation | Startup, hourly, and on `R` in the TUI | Every issue (including archived) and both relation directions, plus a 30-day overlap |

Pages hold at most **250** items. Details and initial relations are batched
**10 issues** at a time, **25 relations** per direction per issue, with further
relation pages at 250.

Requests per scan, excluding detail reads and writes:

| Scan | Requests |
| --- | --- |
| Empty change window | 2 (identity check, one page), regardless of cache size |
| Window with C changed issues | 1 + max(1, ⌈C/250⌉) + ⌈C/10⌉, plus relation overflow |
| Reconciliation of N issues | Identity, list and overlap pages, ⌈N/10⌉, plus overflow |

With 1,000 issues, an empty overlap, and no overflow, reconciliation costs **106
requests an hour**, instead of 3,000+ a minute. Large or relation-heavy teams can
still hit quotas; the scheduler honors both request and complexity limits and
refuses requests until the reset.

Creating or retyping a relation bumps only the target issue's `updatedAt`.
Deleting one bumps both issues. The change window therefore picks up the
change on the target within a minute. Queries and relation rows derive the
source side from it: `blocks:` and "blocking" follow a loaded target's
blockers, and "related" includes inbound links but drops a pair linked by
blocks (BL-93). Until the next reconciliation, the source issue's raw `blocks`
and `related` fields in item output can be stale.

Archiving or trashing an issue bumps neither its own `updatedAt` nor any
related issue's, and restoring it (`issueUnarchive`) doesn't either: measured
live for BL-99 against plain archive, trash, and restore, polling every 10s
for 90s after each mutation with a watermark independent of any field the
mutation itself could move. Only `archivedAt`/`trashed` change. A plain
`filter:{updatedAt:{gte:$since}}` would never match an archive, trash, or
restore by itself, so `incrementalQuery` instead filters on
`{or:[{updatedAt:{gte:$since}},{archivedAt:{gte:$since}}]}`: live measurement
(BL-99, confirmed again for BL-102) shows this surfaces a fresh archive or
trash within a minute at no extra request cost — `x-complexity` stayed 351 for
a 250-item page whether the filter matched zero or one row, and whether it was
the plain or OR form, because complexity is charged by page size, not result
count. linear.go's `Load` then re-reads a trashed row's blocked issues
(`row.Trashed && !reconcile`, BL-92) deliberately, not only when that row
happens to be in the window for an unrelated reason such as an edit in the
same minute. The watermark that drives `$since` always advances from
wall-clock scan start, never from a row's `updatedAt` or `archivedAt`, so a
trashed row's old `archivedAt` can't move it backward; once the window's lower
bound (watermark minus the two-minute overlap) passes that `archivedAt`, the
`gte` comparison stops matching and the row drops out of the window instead of
re-reading forever. There is no equivalent fix for restore: `issueUnarchive`
clears `archivedAt` back to null, leaving no timestamp to filter on — live
measurement confirms a restored issue drops back out of the OR-filtered window
immediately. Both directions wait for the next hourly reconciliation to
correct the blocked issue's `BlockedBy`/`Edges` meanwhile. `bl next --claim`'s
fresh per-candidate read (BL-89) bounds the damage either way: a candidate
blocked by a blocker still cached as archived reads unknown and is skipped as
`changed`, and one a stale cache still shows blocked is skipped too once the
fresh read clears it. Deletions and moves out of scope never appear in a
change window.
Single-issue reads don't wait for reconciliation:

- `bl next --claim` reads each candidate fresh before acquiring it, and skips
  it (`changed` in `--explain`) if that read shows it closed, archived, gone,
  or newly blocked.
- A fresh read from `bl show`, the TUI detail pane, or a write's preflight
  replaces the issue's cached row, or marks it stale if the issue is gone. The
  TUI updates the row at once.
- `R` in the TUI runs a full reconciliation. The minute poll keeps the cost
  of a change window.

The 30-day overlap recovers issues missed by a moving cursor
(as in Worklease v1.9.3); it isn't a retention limit. The schedule survives
restarts with the cursor. Changing archive visibility or loading an old cursor
forces reconciliation.

A moving `updatedAt` scan can't prove absence, so coverage stays partial.
Reconciliation marks unseen rows stale, not deleted or ready. Cursors commit
only after a successful scan. Missing or repeated cursors, unknown states or
relation types, and incomplete nested reads fail closed. Teams with more than
250 labels are refused.

| Relation | Meaning |
| --- | --- |
| Incoming `blocks` | Blocker (`blockedBy`); the only hard edge |
| Outgoing `blocks` | `blocks` |
| `related`, `similar`, `duplicate` | Informational |
| Parent | Hierarchy only |

Edges to other teams or projects stay as external references, so an unloaded
blocker is unknown, never satisfied.

### Cycles

Cycles are sprints (design.md D31), read only when a query, view, group, or
`ready.sprint` mentions them. Each scan's first page also lists the team's
cycles ending within the last 26 weeks or later, so no request is added and a
cycle starting is seen without any issue update. Issues carry `cycle{id}`; an
unnamed cycle displays as `Cycle N`. Enabling cycles forces reconciliation so
cached issues gain their cycle. Assigning an issue to a cycle is assumed to bump
its `updatedAt`; reconciliation corrects it within the hour otherwise.

## Writes and claims

```sh
bl new 'Fix login !high #bug @me' --source eng --body 'Steps' --sprint current
bl set eng:ENG-431 status=ACTIVE_STATE_UUID --if-version VERSION
bl set eng:ENG-431 assignees=VIEWER_UUID --if-version VERSION
bl set eng:ENG-431 priority=high --if-version VERSION
bl set eng:ENG-431 labels=bug --if-version VERSION
bl set eng:ENG-431 sprint=next --if-version VERSION
bl comment eng:ENG-431 'Progress update' --if-version VERSION
bl archive eng:ENG-431 --if-version VERSION
bl unarchive eng:ENG-431 --if-version VERSION
```

Writes need `allowWrites: true`, a freshly verified user, and Linear's
permission; `allowWrites` doesn't prove the key can write. Without it, create,
set, comment, archive, and unarchive are unavailable. Batches and title or body
edits aren't offered.

| Field | Accepts |
| --- | --- |
| `status` | A state UUID from the bound team |
| `assignees` | Zero or one user UUID; empty clears |
| `priority` | `urgent`, `high`, `medium`, `low` → 1–4; empty → 0 (none) |
| `labels` | Exact team or workspace label names; replaces all; empty clears. Missing or ambiguous names fail. |
| `sprint` | `current`, `next`, `none` (or empty), a cycle name, or a cycle UUID from the catalog → `cycleId`. Closed cycles are refused. |
| Comment | Appends once; can't be undone. Attributed to the account's `user`; no `me.default` is needed, and any other `--author` is `rejected`. |
| Create | `issueCreate` with title, body, status (else the team's default state), one assignee, priority, labels, sprint, and parent (`parentId`), each checked like `set` before the mutation. Each blocker then gets one `issueRelationCreate` (`blocks`), and the result is read back. Parent and blockers must resolve to Linear issues in this source or under its out-of-team alias; other sources' refs are refused before dispatch. Other native fields (milestone, criteria, DoD, …) are refused; `defaults.type` and `defaults.dod` are not applied. Undo archives the new issue. |
| Archive, unarchive | `issueArchive` / `issueUnarchive`; each undoes the other. A no-op when the issue is already in the target state. Trashed issues are refused. |

Archived issues accept only unarchive. A write's result replaces the issue's
cached row and archive flag, so an unarchive shows at once even though it
bumps no timestamp a change window could see.

Each write changes one field in one mutation. A fresh fingerprint of the issue
and its relations is checked first; missing or stale versions fail with
`conflict`. Create has no version to check. A create that fails after dispatch
reports `unknown-outcome`: look for the issue before creating it again. When the
issue exists but a blocker relation or the read-back fails, the error names it
(CLI `error.details.created`, MCP `created`, and the TUI message); the create
isn't undoable, and it and its relations are never retried. Parent and
blockers can't yet be changed on an existing issue. Linear has no compare-and-swap, so another writer can still race
between check and write. Versions exclude the cycle, so a sprint write also
re-reads it and fails with `conflict` if it differs from what the caller saw.
Mutations are never retried; after a network or reread
failure, refresh and look before editing again.

Claims use Worklease's `linear` key from the organization UUID and issue UUID,
so they survive renames, team moves, and status changes. Use the `resource` from
`bl show --json`.

## Activity

History (`bl history`, the TUI History tab) adds the issue's history: state,
assignee, label, priority, cycle, and parent changes. Other changes, such as
description edits, are skipped. Linear folds one actor's edits made close
together into one record, even into the creation. Requests go through the
account queue only when History opens, keeping the first 1,000 records Linear
returns, and are
cached by item version. A key or login with `read` suffices, verified live
([BL-140](linear-verification.md#bl-140-history-read-pass)).

## Tests

```sh
task cli:test PKG='./internal/source/linear ./internal/source/remote ./internal/project ./internal/engine ./internal/worklease'
```

Fake HTTPS tests cover the 250-item page cap, recovering an issue missed by a
moving cursor, relation-only changes, blocker direction, conflicts, capability
and identity checks, and every write. Provider JSON is fuzzed. Scheduler tests
cover Linear's request and complexity quotas.

The live test runs only against a team named **Test**. It creates three issues
(one through `bl`'s own create, in an open cycle if the team has one, and a
child of it, created with a parent and a blocker), a blocking relation, and a
comment, edits only those, and deletes them all,
logging artifact IDs. It needs existing todo, active, and done states and at
least one label, and changes no team settings or existing data:

```sh
BL_PROVIDER_TESTS=1 BL_LINEAR_TEST_TEAM=Test \
  task cli:test PKG=./internal/source/linear -- -run '^TestLiveRoundTrip$' -count=1 -v
```

`TestLiveActivity` creates one issue in the Test team, waits four minutes so
the edit isn't folded into the creation, moves it to an active state, reads its
history, and deletes it:

```sh
BL_PROVIDER_TESTS=1 BL_LINEAR_TEST_TEAM=Test \
  task cli:test PKG=./internal/source/linear -- -run '^TestLiveActivity$' -count=1 -v
```

It uses the Keychain helper above; `BL_LINEAR_TEST_HELPER` may hold another JSON
argv (**never a token**). Without `BL_PROVIDER_TESTS=1`, nothing contacts Linear,
and a skipped test isn't a pass. See the [recorded run](linear-verification.md).
