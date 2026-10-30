# Linked changes

An item shows the worktrees, branches, pull requests, and landed commits that
carry its work (design.md D43). Links are derived on each refresh and kept in
memory and the cache, never written to item files. `bl` never fetches, merges,
rebases, or manages branches or CI.

## Model

A change is one line of work, shown at its furthest stage:

| Stage | Meaning |
| --- | --- |
| `worktree` | A local branch checked out in a worktree, not pushed |
| `branch` | A local branch without a worktree, or one on an already-fetched remote |
| `pr` | A pull request |
| `landed` | Commits on the default branch's first-parent history |

A worktree, its branch, the pull request from that branch, and the merge that
landed it are one change, so it moves from `worktree` to `landed` and collapses
once merged. A pull request whose merge commit, squash, or rebase GitHub
records on the default branch is landed.

| State | Meaning |
| --- | --- |
| `open`, `draft` | Not yet merged or closed |
| `merged` | Merged; every landed change |
| `closed` | Closed without merging |
| `unknown` | The link names several items, or a refs URL bl can't resolve |
| `stale` | The forge's last refresh failed, so the pull request may be out of date |

Checks are `passing`, `failing`, `pending`, or `stale`; review is `approved`,
`changes` (changes requested), or `requested`. Unknown and stale never read as
merged, and stale checks never read as passing. An open pull request's checks
are stale when they ran on a different commit than the local branch head. Each
change carries a `reason` when it's unknown or stale.

An open pull request also lists `checkRuns`, each check run and commit status
on its head with `name`, `state` (`passing`, `failing`, `pending`, `skipped`,
or `stale`), and `url`, and `unresolved`, its unresolved review thread count.

## Links

| Link | Rule |
| --- | --- |
| `branch` | The branch name, or its last `/` segment, starts with an item ID followed by `-` or its end, ignoring case: `bl-12-slug` and `feat/bl-12-slug` link to BL-12. The longest matching ID wins. |
| `title` | A word of the pull request title is an item ID containing a letter |
| `closes` | The pull request body names an ID after `Closes`, `Fixes`, `Resolves`, or `Refs` |
| `provider` | GitHub reports that the pull request closes a GitHub-source item |
| `trailer` | A landed commit's `history.commitTrailer` value is an item ID |
| `merge` | A first-parent merge subject names the branch: `Merge branch 'X'`, `Merge remote-tracking branch 'origin/X'`, or `Merge pull request #N from owner/X` |
| `ref` | The item's `refs` lists the pull request or commit URL |

The `{branch}` placeholder already names branches this way, so a worktree
created with `wt switch --create {branch}` links at once. An ID that names more
than one item, as in a workspace, links each of them as unknown. A refs URL
for a pull request the forge hasn't listed, or a commit outside the default
branch's history, shows as unknown rather than disappearing.

Only pull requests in `origin`'s repository join a local branch by head or a
`Merge pull request #N` subject by number; another forge's pull requests link
by their head branch name, title, and body alone.

## Configuration

```yaml
changes:
  git: true
  forges:
    - type: github
      host: github.com
      repo: acme/app
      account: personal
```

| Field | Meaning |
| --- | --- |
| `git` | Read local branches, worktrees, and landed commits (default `true`) |
| `forges[].type` | `github` |
| `forges[].host` | Default `github.com` |
| `forges[].repo` | `owner/repo` |
| `forges[].account` | A configured account; default the account of a GitHub source reading the same repository, else the only account for the host |

Without `forges`, a GitHub `origin` remote is the forge when exactly one GitHub
account is configured for its host. Forges reuse accounts, credentials, request
queues, and redaction from [GitHub sources](github.md#linked-changes), and
project-declared forges do nothing until `bl trust`.

## Git

`bl` reads refs, packed refs, worktrees, and reflogs from `.git` without
starting Git. It runs Git only where files can't answer:

- One `git log --first-parent` of the default branch's newest 1,001 commits
  when its head moves, cached by head commit. Older commits are not read, and
  the `git` provider reports `partial`.
- For the open changes of the item shown in the TUI or `bl changes`,
  `rev-list --left-right --count` against the default branch and
  `status --porcelain` in each worktree. Refreshing the index runs any filter
  driver on changed files, so when the repository's own config defines a clean
  or process filter, the dirty count stays unknown and `status` doesn't run.
- For the TUI change view's change, `log` of its commits and `diff --raw
  --numstat`, plus `diff --patch --no-ext-diff --no-textconv` on its Diff tab
  or for a History commit, against the merge base (and the working tree for a
  worktree, unless a filter driver is configured) or a landed merge's first
  parent.

Every run passes `--no-pager --no-optional-locks --literal-pathspecs -c
core.fsmonitor=false -c log.showSignature=false` with `GIT_NO_LAZY_FETCH=1`
and `GIT_TERMINAL_PROMPT=0` in the minimal hook environment, so it never
fetches, prompts, takes optional locks, or runs an fsmonitor, external diff,
or textconv. The default branch is `origin/HEAD`, else `main`, else `master`;
without one nothing reads as landed.

## Forge refresh

Each refresh reads every open pull request, whose checks change without
touching its update time, and every other one updated since the last refresh,
through the account's request queue. The newest 1,000 updated are kept,
redacted, in a `.pulls.json` file beside the account's provider cache. A failed
refresh keeps the cached list and marks it stale. The token needs read access
to pull requests; a pull request whose checks or closing references it can't
read still links by branch, title, and body.

A pull request's detail (description, checks, reviewers, comments, and review
threads) is read for the item shown, never on a list refresh: in the Changes
tab and change view, by `bl changes`, and by MCP `changes`, for open pull
requests outside the change view. One GraphQL read per pull request goes
through the account's request queue, redacted and stripped of terminal
sequences before it is kept in a `.reviews.json` file beside `.pulls.json`, and
is reused for a minute while the head doesn't move; `R` reads it again. A
failed read keeps the last one, which shows with why it may be out of date.
It reads the first 100 checks, comments, reviews, and threads.

The TUI reads local data every five seconds, which starts no process unless the
default branch moved, and forges every minute, on a worker; `R` reloads both.
`bl changes`, MCP `changes`, `bl history`, and `bl ls` or MCP `query` with a
change term load once per call.

## Surfaces

| Surface | Shows |
| --- | --- |
| TUI list | The most advanced change, such as `⇡#45 ✓` ([TUI](tui.md#layout)) |
| TUI Changes tab | Every change, furthest first, with its actions and URL ([TUI](tui.md#detail)) |
| TUI change view | One change's overview with checks and reviewers, files with `+/−`, highlighted diff, commits, and conversation ([TUI](tui.md#detail)) |
| History | One group per change: its span, events, and landed commits |
| Queries | `has:change`, `change:`, `checks:`, `review:` ([Query](query.md#fields)) |
| `bl changes ID` | The same list as text or JSON ([CLI](cli.md#bl-changes-id)) |
| MCP `changes` | The same data as `bl changes --json` ([MCP](mcp.md#tools)) |
| Actions | `{change.number}`, `{change.url}`, `{change.branch}`, `{change.head}`, `{change.base}`, `{change.path}` ([Automation](automation.md#placeholders-and-environment)) |

Each response lists `providers`: `git` and each forge with `ok`, `partial`, or
`unavailable` and a reason, so a missing change can be told from a provider
that couldn't answer.
