# Linear

## Setup

Use immutable organization, team, optional project, and viewer UUIDs, not team
names or display identifiers. Configure the account only in the user file
(`~/.config/backlog/config.yaml`):

```yaml
accounts:
  linear:
    - name: work
      user: VIEWER_UUID
      credential: [security, find-generic-password, -s, linear-api, -w]
      # Alternatively, replace credential with env: BL_WORK_LINEAR_API_KEY
```

Create a personal API key in Linear Settings → Security & access → Personal API
keys. Give it read access to the selected team. For writes, enable issue update
and comment creation permissions (or the full write permission offered by your
workspace). Restrict the key's team access to the teams needed. The test setup
also needs create/delete issue and relation permissions in the disposable Test
team. Organization/team restrictions and user permissions still apply; a helper
cannot grant access the key lacks. OAuth tokens are not supported by this adapter:
personal API keys use raw Authorization, not a Bearer prefix.

Store the key in your credential manager, never in committed config, command
arguments, screenshots or logs. The helper is an argv array, runs without a shell,
and must print one token line. It is bounded to 15 seconds and 4 KiB. The returned
credential must authenticate as `user`; every refresh and write checks the viewer,
organization and team. Credentials remain in memory and are redacted before cache,
events or hooks. See [remote.md](remote.md) for the shared boundary.

In the committed `.config/backlog.yaml`:

```yaml
sources:
  - id: eng
    type: linear
    site: ORGANIZATION_UUID
    team: TEAM_UUID
    # project: PROJECT_UUID  # optional additional scope
    account: work
    allowWrites: true       # default false; requires re-trust when changed
statuses:
  - {key: TODO_STATE_UUID, name: Todo, category: todo}
  - {key: ACTIVE_STATE_UUID, name: In Progress, category: active}
  - {key: DONE_STATE_UUID, name: Done, category: done}
workflow: {start: ACTIVE_STATE_UUID, done: DONE_STATE_UUID}
```

Run `bl trust` before contacting a committed network source. Obtain UUIDs through
Linear's GraphQL API using your private helper: `viewer { id organization { id } }`,
`teams { nodes { id name } }`, and `team(id:TEAM_UUID) { states { nodes { id name type } } }`.
Configure status keys and workflow transitions with state UUIDs from that team.
`backlog`, `unstarted`, `started`, `completed`, and `canceled`/`duplicate` map to
backlog, todo, active, done, and canceled categories respectively. Raw state names,
types, priority, organization/team/project UUIDs and issue UUIDs remain in fields.
Display IDs such as `ENG-431` stay separate from immutable issue identity.

## Sync and relations

Pages never request more than **250** items. Each refresh independently enumerates
the team's issues (including archived issues) and both relation directions for
every in-scope issue, even when `updatedAt` is unchanged. A second scan overlaps
the previous committed scan's start by **30 days**, retaining the Worklease
v1.9.3 policy. This recovers issues moved ahead of an already traversed cursor.
The overlap is not a retention limit: full enumeration still includes older issues.
The shared remote worker polls each minute; network work never runs on the UI loop.

A moving `updatedAt` traversal and filtered visibility cannot prove absence.
Coverage remains partial; unseen cached rows are retained as stale, not deleted
or considered ready. Cursors commit with rows only after successful scans.
Missing/repeated cursors, unknown states/relations and incomplete nested reads
fail closed. Labels over the 250-label complete-read limit are refused. Relations
and comments have separately paged, bounded reads.

Only incoming `blocks` relations are hard prerequisites (`blockedBy`). Outgoing
`blocks` populate `blocks`, never this issue's prerequisites. `related`, `similar`
and `duplicate` are informational. Parent is hierarchy only. Cross-team/project
edges retain explicit external refs, so an unloaded blocker remains unknown,
never silently satisfied. Relation scans run independently because relation edits
do **not** reliably bump issue `updatedAt`.

## Writes and claims

```sh
bl set eng:ENG-431 status=ACTIVE_STATE_UUID --if-version VERSION
bl set eng:ENG-431 assignees=VIEWER_UUID --if-version VERSION
bl set eng:ENG-431 priority=high --if-version VERSION
bl set eng:ENG-431 labels=bug --if-version VERSION
bl comment eng:ENG-431 'Progress update' --if-version VERSION
```

Writes require explicit `allowWrites: true`, an active freshly verified principal,
and provider authorization. The opt-in is **not** evidence of API-key scope or
permission: Linear can still refuse a mutation. With the gate off, Set and Comment
capabilities are unavailable. Unknown operations and fields are denied. Creation,
archive, batch and arbitrary body/title editing are not advertised.

Each Set changes one field in one mutation. State must be a state UUID of the bound
team. Assignees replace the single slot with zero or one user UUID; an empty list
clears it. Priority accepts urgent/high/medium/low or empty (no priority), mapping
to Linear 1/2/3/4/0. Labels replace the list using exact names resolved to team or
workspace label UUIDs; missing/ambiguous names are rejected. Empty labels clear
all labels. Comments append once and are not undoable.

Missing or stale expected versions report `conflict`. A fresh composite fingerprint
covers issue fields and independently read relationships, including relation-only
changes. Linear has no compare-and-swap for these mutations: another writer can
still race between preflight and dispatch. Mutations are never automatically
retried. After a network or read-back failure, refresh and inspect before another
edit, especially a comment. No unsupported atomicity or recovery journal is implied.

Claims use Worklease's `linear` policy with the organization UUID as source and
issue UUID as item, independent of source aliases, mutable identifiers, team moves,
status, title and credentials. Use the resource returned by `bl show --json`.

## Verification

```sh
task cli:test PKG='./internal/source/linear ./internal/source/remote ./internal/project ./internal/engine ./internal/worklease'
```

Fake HTTPS regressions cover the 250 page cap, recovery of an actually omitted
moving-cursor row, relation-only reconciliation, directional blockers, conflicts,
capability/identity gates and state/assignee/priority/label/comment round trips.
Provider JSON has a fuzz target. Shared scheduler tests cover Linear's request and
complexity quotas with millisecond reset epochs and preserve GitHub Bearer auth.

The opt-in live test accepts only the explicitly authorized **Test** team. It
creates two synthetic issues, a blocks relation and a comment; edits only those
issues; explicitly deletes the relation, comment and issues in cleanup; logs
non-secret artifact IDs and cleanup results. It requires existing
todo/active/done states and at least one existing workspace/team label. It does not
change team configuration or existing issues/labels.

```sh
BL_PROVIDER_TESTS=1 BL_LINEAR_TEST_TEAM=Test \
  task cli:test PKG=./internal/source/linear -- -run '^TestLiveRoundTrip$' -count=1 -v
```

The default helper is the Keychain argv above. `BL_LINEAR_TEST_HELPER` may contain
an alternative JSON argv array referencing a private credential manager, **never
a token**. Without `BL_PROVIDER_TESTS=1`, no live request is made. A skipped test
does not satisfy the live acceptance criterion. See [linear-verification.md](linear-verification.md)
for the passing authorized run and cleanup receipts.
