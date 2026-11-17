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
| `pr` | A pull request, or a GitLab merge request |
| `landed` | Commits on the default branch's first-parent history |

A worktree, its branch, the pull request from that branch, and the merge that
landed it are one change, so it moves from `worktree` to `landed` and collapses
once merged. A pull request whose merge commit, squash, or rebase GitHub
records on the default branch is landed. Merge requests ([GitLab](gitlab.md))
read as pull requests throughout, labelled `!N` rather than `#N`.

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
| `ref` | The item's `refs` lists the pull request, merge request, or commit URL |

The `{branch}` placeholder already names branches this way, so a worktree
created with `wt switch --create {branch}` links at once. An ID that names more
than one item, as in a workspace, links each of them as unknown. A refs URL
for a pull request the forge hasn't listed, or a commit outside the default
branch's history, shows as unknown rather than disappearing.

Only pull requests in `origin`'s repository join a local branch by head or a
`Merge pull request #N` subject by number; another forge's pull requests link
by their head branch name, title, and body alone.

A [`github-pulls` source](github.md#pull-requests-as-items) lists the pull
requests these rules link to no item as items of their own (D44).

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
| `forges[].type` | `github` or `gitlab` |
| `forges[].host` | Default `github.com` or `gitlab.com` |
| `forges[].repo` | `owner/repo`, or a GitLab project path such as `group/subgroup/app` |
| `forges[].account` | A configured account; default the account of a GitHub source reading the same repository, else the only account of the forge's type for the host |

Without `forges`, a GitHub or GitLab `origin` remote is the forge when exactly
one account of that type is configured for its host. Forges reuse accounts,
credentials, request queues, and redaction from [GitHub
sources](github.md#linked-changes); a GitLab account with a `read_api` token
serves only its forge ([GitLab](gitlab.md)). Project-declared forges do nothing
until `bl trust`. [`bl doctor`](cli.md#bl-doctor---online) reports each
forge, whether its token can read pull requests, checks, and closing
references, and why an origin gave none. [`bl config show`](cli.md#bl-config-show)
lists the forges that resolved, and `bl instructions changes` gives agents
this setup.

## Tours

Writing a [tour](cli.md#bl-tour-id-file) costs agent tokens, so the project
opts in:

```yaml
tours:
  mode: require
  stages: [pr]
```

| Field | Meaning |
| --- | --- |
| `mode` | `off` (default) says nothing about tours; `suggest` asks agents for a tour of each matching change; `require` also refuses done while one lacks a tour for its current head |
| `stages` | Which open changes match: `worktree`, `branch`, `pr` (default all three) |

A matching change is open or draft, has a head, and is at one of `stages`;
landed and closed changes never match. Under `suggest`, `bl instructions
workflow` asks for a tour of each matching change at its head, citing the
criteria its steps satisfy, and `bl done` and MCP `update` with `operation:
"done"` return `tourRequest` naming each matching change still without one.
Under `require`, the workflow says so, and any move into a done-category
status, from the CLI, MCP, or the TUI, is refused with `rejected` naming each
such change, its head, and `bl tour ID FILE`. A tour carried forward to the
head with no step outdated counts as current under both modes. An item already
done is never refused. With `off`, nothing outside `bl instructions tours` mentions tours.

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
- For a [tour](tui.md) step on a line outside every hunk, `cat-file` of its
  file at the change's head when there is no worktree to read it from or a
  filter driver is configured.

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
refresh keeps the cached list, plus whatever it read, and marks it stale. On
GitHub, a page that fails with a server error or a GraphQL error, as slow
pages on a large repository do, is read again at half the size, down to 12,
and an interrupted read resumes after its last page on the next refresh, so a
first refresh of thousands of pull requests converges.

Open pull requests are what in-progress items link to, so on GitHub a refresh
reads all of them first, then at most 3 pages of closed and merged ones, and
fewer once the token has under a tenth of its hourly limit left, leaving that
for interactive reads and other tools. Later refreshes continue from where the
last stopped until they reach the newest 1,000 or the end. Until then the
forge reports `partial`, such as `reading older pull requests, 300 read, will
resume`, and its pull requests are current, not stale; a failed closed and
merged page that can resume is `partial` too. A failed open pass is
`unavailable`, with the cached pull requests stale. The list read skips the
merge state, review decision, and checks, which together quadruple a page's
time. After linking, one aliased GraphQL query reads them for the open pull
requests linked to unfinished items, active items' first, at most 50 per
query, plus every open pull request of a repository a `github-pulls` source
lists. The TUI reads them with each refresh, and refreshes early when the
linked set gains a pull request. Any other GitHub pull request shows no list checks, review, or merge
state, and `checks:`, `review:`, and `merge:` don't match through one. Its
change view still reads them. The
token needs read access to pull requests, or GitLab's `read_api` scope; a pull
request whose checks or closing references it can't read still links by
branch, title, and body.

A pull request's detail (description, merge state, checks with their exact
state and timing, reviewers with their latest review, comments, and review
threads) is read for the item shown, never on a list refresh: in the Changes
tab and change view, by `bl changes`, and by MCP `changes`, for open pull
requests outside the change view. One GraphQL read per pull request goes
through the account's request queue, redacted and stripped of terminal
sequences before it is kept in a `.reviews.json` file beside `.pulls.json`, and
is reused for a minute while the head doesn't move; `R` reads it again. A
failed read keeps the last one, which shows with why it may be out of date.
It reads the first 100 checks, comments, reviews, and threads.

A check run's detail (its first 50 steps and annotations, output title and
summary, and, for a failed GitHub Actions job, a log excerpt) is read only for
the check shown and the first five failing runs `--detail` reports, through
the same queue. The log comes from signed storage without the account's
credential, and needs the token's Actions: read
([GitHub](github.md)). It streams, keeping at most 100 lines of 2 KiB each,
stops at the first `##[error]` line, and gives up past 32 MiB. Timestamps and
terminal sequences are stripped and secrets redacted before a completed run's
detail is kept, by check run, in a `.checks.json` file beside `.pulls.json`;
a running job is read again each time.

From that detail, one verdict says whether the pull request can land and
what's left, in fix-first order: failing checks, requested changes, unresolved
threads, required review, pending checks, conflicts, behind the base, branch
protection, and a stale read. The forge's merge state decides; without one,
the verdict comes from mergeability, checks, and the review decision. A stale
read, stale checks, or a draft never reads ready. Exact check states (`failed`,
`timed out`, `cancelled`, `queued`, `skipped`, and so on) map onto the
`passing`, `failing`, `pending`, and `stale` categories above, so `checks:`
queries keep their meaning. bl models only what every forge has; merge
queues, auto-merge, merge method, required checks, and code owners stay in
the browser (`O`) or an action (`!`) ([design](design.md) D46).

The TUI reads local data every five seconds, which starts no process unless the
default branch moved, and forges every minute, on a worker; `R` reloads both.
While a forge is `partial`, forges load again five seconds after the last load
instead, publishing after each, until none is; a rate-limited account sends
nothing before its retry time.
On each per-minute forge refresh it also reads the detail of the open pull
request a change view or the Changes tab shows, one read per shown pull request
a minute, on the detail worker through the account's request queue. Nothing
reads a pull request that isn't shown, and the reads stop when the view closes
or the selection moves.
`bl changes`, MCP `changes`, `bl history`, and `bl ls` or MCP `query` with a
change term load once per call, so on a large repository they report the forge
`partial` until enough calls or the TUI have read its older pull requests.

## Surfaces

| Surface | Shows |
| --- | --- |
| TUI list | The most advanced change, such as `⇡#45 ✓` ([TUI](tui.md#layout)) |
| TUI Changes tab | Every change, furthest first, with its actions and URL ([TUI](tui.md#detail)) |
| TUI change view | A verdict strip, and an overview of merge reasons, checks, and reviewers, files with `+/−`, highlighted diff, commits, and conversation, with review threads inline in the diff ([TUI](tui.md#detail)) |
| History | One group per change: its span, events, and landed commits |
| Queries | `has:change`, `has:pr`, `change:`, `checks:`, `review:`, `merge:` ([Query](query.md#fields)) |
| `bl changes ID` | The same list as text or JSON; `--detail` adds the verdict, check timing, reviewers, and review threads ([CLI](cli.md#bl-changes-id)) |
| MCP `changes` | The same data as `bl changes --json` ([MCP](mcp.md#tools)) |
| Actions | `{change.number}`, `{change.url}`, `{change.branch}`, `{change.head}`, `{change.base}`, `{change.path}` ([Automation](automation.md#placeholders-and-environment)) |

Each response lists `providers`: `git` and each forge with `ok`, `partial`, or
`unavailable` and a reason, so a missing change can be told from a provider
that couldn't answer.
