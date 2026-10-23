# GitHub Issues and Projects v2

## Setup

Configure credentials only in the user config (`~/.config/backlog/config.yaml`):

```yaml
accounts:
  github:
    - name: personal
      host: github.com
      user: alice
      credential: [gh, auth, token, --hostname, github.com, --user, alice]
```

Use a classic OAuth token authenticated by `gh auth login`: `repo` for private
repositories and issue writes (`public_repo` suffices for public-only writes),
`read:project` for Projects v2 reads, and **`project`** for project status writes.
Authorize organization SAML SSO when required. Refresh through
`gh auth refresh --hostname github.com --scopes repo,project`; never put a token
in project config or command arguments. The helper's principal must match `user`.
Project writes require the response's OAuth scopes to include `project`; missing
scope evidence fails closed (fine-grained tokens without this header are not
supported for project writes). Repository writes also require the current
`viewerPermission` to be TRIAGE, WRITE, MAINTAIN or ADMIN; provider permissions
remain authoritative for each operation.

In `.config/backlog.yaml`:

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

Run `bl trust` before contacting a committed network source. Enterprise hosts use
HTTPS `/api/graphql` and `/api/v3`. The source opens lazily on its worker, never
on the Bubble Tea update loop. Account and configuration partition the disposable
shared cache; cached startup rows remain stale until refreshed.

## Project status

Find IDs with `gh project view NUMBER --owner OWNER --format json` and
`gh project field-list NUMBER --owner OWNER --format json`. Bind exactly one
single-select field by immutable ID. For example, add this to the source:

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
```

Mapping values are `backlog`, `todo`, `active`, `done`, or `canceled`. Add matching
option IDs to project `statuses` for CLI/TUI transitions, and select the desired
IDs in `workflow.start` / `workflow.done` when using `start` / `done`:

```yaml
statuses:
  - {key: open, name: Open, category: todo}
  - {key: closed, name: Closed, category: done}
  - {key: option-todo-id, name: Todo, category: todo}
  - {key: option-active-id, name: In Progress, category: active}
  - {key: option-done-id, name: Done, category: done}
workflow: {start: option-active-id, done: closed}
```

Project status changes do not change issue state. `status=closed` closes completed;
`status=open` reopens; `status=OPTION_ID` changes only the bound project field.
Closed issues remain terminal regardless of an outdated project option.
Unmapped/unset status or an issue absent from the project is conservatively
`backlog`, never ready. Raw project status and binding IDs remain in item fields.
Each refresh independently enumerates all project items and all issues, including
`blockedBy` and parent/sub-issue edges. A REST newest-issue ETag/304 is only a
hint: it never skips reconciliation. This catches project-only edits and removed
dependencies even when issue `updatedAt` is unchanged. Inaccessible or incomplete
reads fail rather than claiming complete coverage or deleting unseen rows.

## Writes and claims

```sh
bl set issues:123 status=option-active-id --if-version VERSION
bl set issues:123 labels=bug
bl set issues:123 assignees=alice
bl comment issues:123 'Progress update'
```

Use one field assignment per command. Batch, issue creation, arbitrary body/title
editing, additive/removal mutations and archive operations are unavailable.
Labels and assignees replace their respective lists in one REST request. Comments
append one new issue comment and are not undoable. Every mutation refreshes credentials, verifies the
principal, checks repository capability and a **fresh composite version** of the
issue, relationships and project item (including project item `updatedAt`).
Missing or stale expected versions fail with `conflict`; unknown operations and
unconfigured project options are refused before dispatch.

GitHub provides no conditional/CAS write for these operations. A concurrent edit
between fresh preflight and mutation can still race; `If-Match` is not advertised
or assumed. Mutations are sent exactly once, never replayed automatically. A
network/read-back failure after dispatch has an uncertain outcome: refresh and
inspect before issuing another edit, especially a comment. GitHub read-back is
eventually consistent. No multi-request operation is advertised as atomic.

Claims use Worklease's `github` policy on `HOST/OWNER/REPO` and the decimal issue
number, independent of source aliases, project membership, status, title and
credential. Repository and issue node identities are verified on every read;
redirects, transfers or binding drift fail closed. Use only the resource reported
by `bl show --json` for coordination.

## Verification

```sh
task cli:test PKG='./internal/source/github ./internal/source/remote'
```

Provider JSON has a fuzz target (`FuzzProviderJSON`). The opt-in live test requires
an explicitly authorized test repository and a newly created disposable project
whose title begins `bl-provider-test-`; it creates only a synthetic issue, comment
and project entry and deletes those artifacts in cleanup. Create/delete the
disposable project separately; do not point it at existing user projects.

```sh
gh project create --owner OWNER --title bl-provider-test-roundtrip --format json
# Inspect its new ID/number and field/option IDs, then:
BL_PROVIDER_TESTS=1 \
BL_GITHUB_TEST_REPO=OWNER/REPO BL_GITHUB_TEST_USER=OWNER \
BL_GITHUB_TEST_PROJECT_NUMBER=NUMBER BL_GITHUB_TEST_PROJECT_ID=PVT_ID \
BL_GITHUB_TEST_FIELD_ID=PVTSSF_ID \
BL_GITHUB_TEST_TODO_OPTION=TODO_ID BL_GITHUB_TEST_ACTIVE_OPTION=ACTIVE_ID \
BL_GITHUB_TEST_LABEL=EXISTING_LABEL \
task cli:test PKG=./internal/source/github -- -run TestLiveRoundTrip -count=1 -v
# After confirming zero remaining items:
gh project delete NUMBER --owner OWNER
```

The label must already exist; the test changes only its own issue's labels.
Cleanup logs contain artifact IDs, never credentials. Live cleanup evidence for
BL-38 is in [github-verification.md](github-verification.md).
