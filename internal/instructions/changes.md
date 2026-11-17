# Linked changes

An item shows the worktrees, branches, pull requests (GitLab merge requests),
and landed commits that carry its work. Git is read locally; pull requests
come from a forge. Links are derived, never written to item files: a branch
named `bl-12-slug` or `feat/bl-12-slug`, an ID in a pull request title, or
`Closes BL-12` in its body links it to BL-12.

## Forges

Without `changes.forges`, a GitHub or GitLab `origin` remote is the forge when
exactly one account of that type is configured for its host. `bl config show
--json` lists the effective forges under `data.changes.forges`, each with
`type`, `host`, `repo`, `account`, `implicit` (true when derived from origin),
and `error` when it can't be read. With none, `data.changes.reason` says why
and `fix` what to add. `bl doctor --json` also checks that each forge's token
can read pull requests.

Minimal user config (`~/.config/backlog/config.yaml`) for GitHub:

```yaml
accounts:
  github:
    - {name: personal, host: github.com, user: YOUR_LOGIN, oauth: true}
```

Then a person runs `bl auth login github personal`; stop and ask instead of
running it. A GitLab account names its token instead: `env: GITLAB_TOKEN`.

To name the forge explicitly, or choose among several matching accounts, add
it to the project's `backlog.yaml`; a person must then run `bl trust`:

```yaml
changes:
  forges:
    - {type: github, host: github.com, repo: OWNER/REPO, account: personal}
```

## Token permissions

GitHub needs read access to pull requests: the `repo` scope (or
`public_repo`), or device login with the `bl-cli-read` app installed on the
repository. Checks and closing issues need their own read access; without it a
pull request still links by branch, title, and body. Check run logs need
Actions: read. GitLab needs the `read_api` scope.

## Reading changes

```sh
bl changes ID --json
bl changes ID --detail --json
```

`data.changes` lists each change, furthest first, with `id`, `stage`
(`worktree`, `branch`, `pr`, `landed`), `state` (`open`, `draft`, `merged`,
`closed`, `unknown`, `stale`), `link` (`branch`, `title`, `closes`,
`provider`, `trailer`, `merge`, `ref`), and when known `reason`, `checks`
(`passing`, `failing`, `pending`, `stale`), `review` (`approved`, `changes`,
`requested`), `branch`, `head`, `title`, `url`, `number`, `checkRuns`, and
`unresolved`. `--detail` adds, for an open pull request, `merge` with a
`verdict` and `reasons` in fix-first order, `reviewers`, `threads`, and the
failing check runs' steps, annotations, and log excerpt.

`data.providers` lists `git` and each forge with a `state` and `reason`: `ok`
answered fully; `partial` answered but may miss some, such as commits older
than the newest 1,001 on the default branch, or a forge's older pull requests,
which each later call reads more of; `unavailable` couldn't answer,
so a missing change may still exist and cached pull requests read `stale`.
Never read a missing change as absent while its provider is `partial` or
`unavailable`.
