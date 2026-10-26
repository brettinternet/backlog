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

Each refresh lists every project item and every issue, with `blockedBy` and
sub-issue edges, so project-only edits and removed dependencies show up even
when the issue's `updatedAt` doesn't change. A REST ETag `304` is only a hint.
Inaccessible or incomplete reads fail rather than claim completeness.

## Writes and claims

```sh
bl set issues:123 status=option-active-id --if-version VERSION
bl set issues:123 labels=bug
bl set issues:123 assignees=alice
bl comment issues:123 'Progress update'
```

One field per command. Labels and assignees replace the whole list. Comments
append and can't be undone. Creating issues, editing title or body, `+=`/`-=`,
batches, and archiving aren't available.

Before each write, `bl` refreshes credentials, checks the user and permission,
and compares a fresh composite version of the issue, its relationships, and its
project item. Missing or stale versions fail with `conflict`.

GitHub has no conditional write here, so an edit between that check and the
mutation can still race. Each mutation is sent once. If the network or reread
fails afterward, the outcome is unknown: refresh and look before editing again,
especially before commenting. Reads after writes are eventually consistent.

Claims use Worklease's `github` key from `HOST/OWNER/REPO` and the issue number,
so they don't depend on aliases, project, status, title, or credential. Node
IDs are checked on every read; transfers or binding changes fail closed. Use the
`resource` from `bl show --json`.

## Tests

```sh
task cli:test PKG='./internal/source/github ./internal/source/remote'
```

`FuzzProviderJSON` fuzzes provider JSON. The live test needs an authorized test
repository and a new disposable project titled `bl-provider-test-…`. It creates
one issue, comment, and project entry, and deletes them during cleanup:

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
