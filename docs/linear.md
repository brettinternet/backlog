# Linear

## Setup

Use immutable UUIDs for the organization, team, optional project, viewer, and
states, never names or display IDs. Project config (`backlog.yaml`):

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

### API key

Create a personal API key in Linear Settings → Security & access → Personal API
keys, limited to the teams you need. Give it read access, plus issue update and
comment creation for writes. (The live test also needs issue and relation
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
| Change window | Every minute | Issues updated since the last scan's start, minus two minutes |
| Reconciliation | Startup and hourly | Every issue (including archived) and both relation directions, plus a 30-day overlap |

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

Relation edits don't reliably bump `updatedAt`, so they appear at the next
reconciliation. The 30-day overlap recovers issues missed by a moving cursor
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

## Writes and claims

```sh
bl set eng:ENG-431 status=ACTIVE_STATE_UUID --if-version VERSION
bl set eng:ENG-431 assignees=VIEWER_UUID --if-version VERSION
bl set eng:ENG-431 priority=high --if-version VERSION
bl set eng:ENG-431 labels=bug --if-version VERSION
bl comment eng:ENG-431 'Progress update' --if-version VERSION
```

Writes need `allowWrites: true`, a freshly verified user, and Linear's
permission; `allowWrites` doesn't prove the key can write. Without it, set and
comment are unavailable. Create, archive, batches, and title or body edits
aren't offered.

| Field | Accepts |
| --- | --- |
| `status` | A state UUID from the bound team |
| `assignees` | Zero or one user UUID; empty clears |
| `priority` | `urgent`, `high`, `medium`, `low` → 1–4; empty → 0 (none) |
| `labels` | Exact team or workspace label names; replaces all; empty clears. Missing or ambiguous names fail. |
| Comment | Appends once; can't be undone |

Each write changes one field in one mutation. A fresh fingerprint of the issue
and its relations is checked first; missing or stale versions fail with
`conflict`. Linear has no compare-and-swap, so another writer can still race
between check and write. Mutations are never retried; after a network or reread
failure, refresh and look before editing again.

Claims use Worklease's `linear` key from the organization UUID and issue UUID,
so they survive renames, team moves, and status changes. Use the `resource` from
`bl show --json`.

## Tests

```sh
task cli:test PKG='./internal/source/linear ./internal/source/remote ./internal/project ./internal/engine ./internal/worklease'
```

Fake HTTPS tests cover the 250-item page cap, recovering an issue missed by a
moving cursor, relation-only changes, blocker direction, conflicts, capability
and identity checks, and every write. Provider JSON is fuzzed. Scheduler tests
cover Linear's request and complexity quotas.

The live test runs only against a team named **Test**. It creates two issues, a
blocking relation, and a comment, edits only those, and deletes them all,
logging artifact IDs. It needs existing todo, active, and done states and at
least one label, and changes no team settings or existing data:

```sh
BL_PROVIDER_TESTS=1 BL_LINEAR_TEST_TEAM=Test \
  task cli:test PKG=./internal/source/linear -- -run '^TestLiveRoundTrip$' -count=1 -v
```

It uses the Keychain helper above; `BL_LINEAR_TEST_HELPER` may hold another JSON
argv (**never a token**). Without `BL_PROVIDER_TESTS=1`, nothing contacts Linear,
and a skipped test isn't a pass. See the [recorded run](linear-verification.md).
