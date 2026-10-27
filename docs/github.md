# GitHub Issues and Projects v2

## Setup

Project config (`backlog.yaml`):

```yaml
sources:
  - id: issues
    type: github
    host: github.com
    repo: acme/app
    account: personal
statuses:
  - {key: open, name: Open, category: todo}
  - {key: closed, name: Closed, category: done}
```

User config (`~/.config/backlog/config.yaml`), with a `gh` credential helper:

```yaml
accounts:
  github:
    - name: personal
      host: github.com
      user: alice
      credential: [gh, auth, token, --hostname, github.com, --user, alice]
```

Or use [device login](#oauth-login). Then run `bl trust`.

The helper's token must belong to `user`. Classic OAuth scopes from
`gh auth login`:

| Scope | Allows |
| --- | --- |
| `repo` | Private repositories and issue writes (`public_repo` for public only) |
| `read:project` | Projects v2 reads |
| `project` | Projects v2 status writes; required, or project writes fail closed |

```sh
gh auth refresh --hostname github.com --scopes repo,project
```

Authorize SAML SSO for the organization if required. Repository writes also need
TRIAGE, WRITE, MAINTAIN, or ADMIN permission; GitHub still decides each request.
Enterprise hosts use `/api/graphql` and `/api/v3` over HTTPS.

## OAuth login

Device login uses two owner-approved GitHub Apps, so read-only access is truly
read-only:

| App | Client ID | Permissions | Used by |
| --- | --- | --- | --- |
| `bl-cli-read` | `Iv23lixJ0srsgnPbExrP` | Metadata, Issues, organization Projects: read | `bl auth login` |
| `bl-cli` | `Iv23liwvINakcZA9rAst` | Issues, organization Projects: read/write | `bl auth login --write` |

1. Declare the account with `oauth: true` instead of `credential` or `env`:

   ```yaml
   accounts:
     github:
       - name: personal
         host: github.com
         user: YOUR_LOGIN
         oauth: true
   ```

2. Install `bl-cli-read` on the repositories you need, then:

   ```sh
   bl auth login github personal
   ```

   Open the URL shown and enter the code.

3. For writes, install `bl-cli` and approve its permissions, then:

   ```sh
   bl auth login github personal --write
   ```

   This authorizes `bl-cli` separately and checks it's the same GitHub user. If
   it fails, the read grant stays. Running plain login again switches back to
   read-only locally without revoking `bl-cli` on GitHub.

Permissions come from the App installations, not scopes; no `repo`,
`public_repo`, or `project` scope is requested. Before use and before each
write, `bl` rechecks installation and repository permissions, and GitHub
enforces the intersection with your own. A read App with any write permission is
refused. Project writes also need `allowWrites: true` and the project's
`viewerCanUpdate`. Organization policy or SSO can still deny access.

Supported for github.com and organization Projects v2. Personal Projects v2 and
GitHub Enterprise Server need a helper. Classic `repo` scopes include writes, so
they're not a read-only fallback.

Tokens live in owner-only XDG state. User tokens last eight hours and refresh
tokens six months; refresh rotates both under a cross-process lock. An expired
refresh token needs a new login. MCP, hooks, actions, and claims never log in.

```sh
bl auth logout github personal
```

Logout deletes local tokens and the account's cache. GitHub can't revoke without
an app secret, so remove **both** apps under Settings → Applications →
Authorized GitHub Apps, and uninstall them to withdraw repository access.
Fake-server tests cover the protocol and permission checks; a live login hasn't
been recorded.

## Label statuses

Without Projects v2, every open issue is `open` and ready. To give issues
statuses, map labels to categories:

```yaml
sources:
  - id: issues
    type: github
    repo: acme/app
    account: personal
    statusLabels:
      ready: todo
      in-progress: active
statuses:
  - {key: unknown, name: Triage, category: backlog}
  - {key: closed, name: Closed, category: done}
  - {key: ready, name: Ready, category: todo}
  - {key: in-progress, name: In Progress, category: active}
workflow: {start: in-progress, done: closed}
```

| Issue | Status | Category |
| --- | --- | --- |
| Closed | `closed` | done, or canceled for not planned |
| Open, one mapped label | the label | as mapped |
| Open, no mapped label | `unknown` | backlog |
| Open, several mapped labels | `unknown` | backlog, with a warning naming them |

Label names match case-insensitively, as on GitHub, and mapped labels stay in
`labels`. A closed issue is closed whatever its labels.

| Write | Effect |
| --- | --- |
| `status=LABEL` | Replaces the mapped labels with this one, keeping the rest |
| `status=unknown` | Removes the mapped labels |
| `status=closed` | Closes the issue as completed |
| `status=open` | Reopens it |

Each is one write. A label status on a closed issue is refused; reopen it first.

`bl start` and `bl done` resolve per source ([format](format.md#frontmatter-keys)).
A label source accepts `open`, `closed`, `unknown`, and its mapped labels, so
`done` closes the issue whatever `workflow.done` says, and `start` takes
`workflow.start` when it is a mapped active label, or the only one. A project
mixing native tasks with `workflow: {start: in-progress, done: done}` works for
both.

`statusLabels` with `githubProject`, a category other than `backlog`, `todo`,
`active`, `done`, or `canceled`, two labels differing only in case, or a label
named `open`, `closed`, or `unknown` fails config load.

## Project status

Bind one single-select field by ID. Find the IDs with:

```sh
gh project view NUMBER --owner OWNER --format json
gh project field-list NUMBER --owner OWNER --format json
```

Add to the source, and list the option IDs as statuses:

```yaml
    githubProject:
      owner: alice
      number: 12
      id: PVT_example
      fieldId: PVTSSF_example
      allowWrites: true       # default false
      options:
        option-todo-id: todo
        option-active-id: active
        option-done-id: done
statuses:
  - {key: open, name: Open, category: todo}
  - {key: closed, name: Closed, category: done}
  - {key: option-todo-id, name: Todo, category: todo}
  - {key: option-active-id, name: In Progress, category: active}
  - {key: option-done-id, name: Done, category: done}
workflow: {start: option-active-id, done: closed}
```

Option values are categories: `backlog`, `todo`, `active`, `done`, `canceled`.
With `allowWrites`, `bl start` and `bl done` resolve among `open`, `closed`, and
the option IDs; without it, start is refused and done closes the issue.
An option without a configured status name displays its project option name;
the stored status stays the option ID.

| Write | Effect |
| --- | --- |
| `status=closed` | Closes the issue as completed |
| `status=open` | Reopens it |
| `status=OPTION_ID` | Changes only the project field |

A closed issue is closed whatever its project option says. An unmapped or unset
option, or an issue not in the project, is `backlog` and never ready. Raw
project values stay in item fields.

The adapter assigns each status key its category; `statuses:` entries only name
them:

| Key | Category | When |
| --- | --- | --- |
| `open` | todo | Open issue without a project binding or status labels |
| `closed` | done, or canceled for not planned | Closed issue |
| `unknown` | backlog | Open issue not in the project, with an unmapped option, or without exactly one status label |
| option ID | as mapped in `options` | Open issue with a mapped option |
| label | as mapped in `statusLabels` | Open issue with that one status label |

With any GitHub source configured, a `statuses:` entry for one of these keys
with another category fails config load (`closed` accepts done or canceled), and
so does an option ID or label mapped to different categories by two GitHub
sources.
`bl doctor` reports the same error.

Inaccessible or incomplete reads fail rather than claim completeness. See
[Sync](#sync) for which edits a refresh sees when.

### Sprints

Sprints (design.md D31) are read only when a query, view, group, or
`ready.sprint` mentions them, and only for a project-bound source. The
Projects v2 iteration field is discovered from the project's fields, not
hardcoded: by `githubProject.iterationFieldId` if set, else the single
iteration field present. A project with none is not sprint-capable. Several
iteration fields with no configured choice is a refusal, not a guess:

```yaml
    githubProject:
      owner: alice
      number: 12
      id: PVT_example
      fieldId: PVTSSF_example
      iterationFieldId: PVTIF_example   # optional; autodetects if omitted
```

GitHub marks no iteration active, so `bl` classifies states itself each time
the catalog is fetched, never during query evaluation: an iteration in
`completedIterations` is closed; within `iterations` (current and upcoming),
the one with `startDate <= today < startDate + duration` is active and every
other one is future, even if its own dates have already elapsed. A break with
none in range leaves the whole list future, with no active sprint. Sprint keys
are the iteration's node ID.

## Sync

Two scans, as in [Linear](linear.md#sync):

| Scan | When | Reads |
| --- | --- | --- |
| Change window | Every refresh between reconciliations | Issues updated since the last scan's start minus two minutes, plus issues whose project item was updated since then |
| Reconciliation | Startup (no cached cursor), hourly, and on `R` in the TUI | Every issue with its relations, and every project item |

Requests per scan, excluding relation overflow pages and writes:

| Scan | Requests |
| --- | --- |
| Empty change window | 3 (identity check, repository, one issue page), plus 2 with a project (fields, one item page), regardless of issue count |
| Window with C changed issues | The above, plus ⌈C/100⌉ − 1 issue pages and ⌈P/10⌉ batched reads for P issues changed only in the project |
| Reconciliation of N issues and M project items | 3 + ⌈N/100⌉ (identity, repository, REST hint, issue pages), plus 1 + ⌈M/100⌉ with a project |

The project item filter accepts only dates (`updated:>=YYYY-MM-DD`; a timestamp
matches nothing), so the window reads items updated since the day before and
compares `updatedAt` itself. Its page count follows items edited in the last day
or two, not project size. Each changed issue's project status and iteration come
from its own `projectItems`.

Measured live for BL-120, polling 70 seconds after each mutation:

| Edit | Issue `updatedAt` | Project item `updatedAt` |
| --- | --- | --- |
| Title or other issue edit | Bumped | Bumped |
| Project status option | Unchanged | Bumped |
| Add or remove `blockedBy`, either side | Unchanged | Unchanged |
| Add or remove a sub-issue, either side | Unchanged | Unchanged |

So a change window sees issue and project edits, including new, closed and
relabelled issues, but no dependency or hierarchy change. Those, removal from
the project, deletions and transfers appear at the next reconciliation.
Unchanged rows keep their freshness; a change window never removes rows.
Single-issue reads don't wait: `bl next --claim` reads each candidate fresh,
and `bl show`, the TUI detail pane and write preflights replace the cached row.

Reconciliation is a complete enumeration and removes unseen rows. A REST ETag
`304` is only a hint. The cursor and its schedule survive restarts in the cache.

## Writes and claims

```sh
bl set issues:123 status=option-active-id --if-version VERSION
bl set issues:123 title='Clearer title'
bl edit issues:123 --section description --text 'New body'
bl set issues:123 labels=bug
bl set issues:123 labels+=docs
bl set issues:123 assignees-=alice
bl comment issues:123 'Progress update'
```

One field and one value per command. `labels=` and `assignees=` replace the
whole list. `+=` and `-=` use GitHub's add and remove endpoints, so a label or
assignee someone else added in the meantime stays; adding a present entry or
removing an absent one sends nothing. Description is the only editable section.
Comments append and can't be undone. Batches and archiving aren't available.

Before each write, `bl` refreshes credentials, checks the user and permission,
and compares a fresh composite version of the issue, its relationships, and its
project item. Missing or stale versions fail with `conflict`.

GitHub has no conditional write here, so an edit between that check and the
mutation can still race. Each mutation is sent once. If the request or the
reread after it fails, the write reports `unknown-outcome`: refresh and look
before editing again, especially before commenting. Reads after writes are
eventually consistent.

### Creating issues

```sh
bl new 'Fix login' +issues ^12 '<9' '#bug' @alice --body 'Steps…'
bl new 'Plan sync' --source issues --status option-active-id
```

A create sets title, body, labels, assignees, parent, blockers and status.
Status is `open` (the default when it's the first todo status), a status label,
which the issue POST adds in place of any other status label, or, with
`allowWrites`, a project option ID. Parent and blockers must be issues on the
same host, in this or another configured GitHub repository. Milestone, type,
priority, criteria, sprint and other template fields are rejected before
anything is sent. The TUI offers create unless the last refresh found the
repository read-only.

The issue POST is sent once. After it succeeds, each follow-up is sent once, in
order: `addSubIssue` for the parent, `addBlockedBy` per blocker, then
`addProjectV2ItemById` and the status option. If a follow-up or the read-back
fails, the create reports `unknown-outcome` naming the new issue and records no
undo; check it before creating again. The read-back can lag the project status.

Undo deletes the issue when GitHub reports you can delete it (a repository
admin, where the organization allows it) and nothing has changed since the
create. Otherwise undo closes it as not planned. The status line says which,
and a deleted issue offers no redo. Neither withdraws the notifications GitHub
already sent.

Claims use Worklease's `github` key from `HOST/OWNER/REPO` and the issue number,
so they don't depend on aliases, project, status, title, or credential. Node
IDs are checked on every read; transfers or binding changes fail closed. Use the
`resource` from `bl show --json`.

## Tests

```sh
task cli:test PKG='./internal/source/github ./internal/source/remote'
```

`FuzzProviderJSON` fuzzes provider JSON. Live tests must target a dedicated
disposable repository, such as `brettinternet/bl-provider-test`, never
`brettinternet/backlog`: this repository's issues are a `bl` source, so test
issues and status labels would enter the real backlog.

The live round-trip test needs that repository and a new disposable project
titled `bl-provider-test-…`. It creates one issue, comment, and project entry
and exercises every write above and two change windows. It then creates a child
issue in the project with a parent and blocker and undoes it (closing it, since
the project status moved its version), and creates a plain issue that undo
deletes. Cleanup deletes everything:

```sh
gh project create --owner OWNER --title bl-provider-test-roundtrip --format json
# Read its ID, number, field ID, and option IDs, then:
BL_PROVIDER_TESTS=1 \
BL_GITHUB_TEST_REPO=OWNER/REPO BL_GITHUB_TEST_USER=OWNER \
BL_GITHUB_TEST_PROJECT_NUMBER=NUMBER BL_GITHUB_TEST_PROJECT_ID=PVT_ID \
BL_GITHUB_TEST_FIELD_ID=PVTSSF_ID \
BL_GITHUB_TEST_TODO_OPTION=TODO_ID BL_GITHUB_TEST_ACTIVE_OPTION=ACTIVE_ID \
BL_GITHUB_TEST_LABEL=EXISTING_LABEL \
task cli:test PKG=./internal/source/github -- -run TestLiveRoundTrip -count=1 -v
# After confirming the project is empty:
gh project delete NUMBER --owner OWNER
```

The label must already exist. Logs include artifact IDs, never credentials. See
the [recorded run](github-verification.md).

`TestLiveLabelStatus` needs only the repository and user. It creates three
uniquely named `bl-test-*` labels and one issue, moves the issue through label
statuses, and deletes exactly those labels and the issue:

```sh
BL_PROVIDER_TESTS=1 BL_GITHUB_TEST_REPO=OWNER/REPO BL_GITHUB_TEST_USER=OWNER \
task cli:test PKG=./internal/source/github -- -run TestLiveLabelStatus -count=1 -v
```
