# Design

**Status:** Accepted direction. This is the plan of record for `bl`. Backlog tasks cite decision IDs (`D#`). If you change a decision, update this register and the affected sections in the same commit. Until the task that ships an interface freezes it, command names, configuration keys, and wire shapes here are illustrative.

Companion specs:

- [format.md](format.md): native item files
- [query.md](query.md): filter, sort, and group language
- [tui.md](tui.md): layouts, keymap, and interaction rules

## 1. Purpose

`bl` is the fastest, most ergonomic way for a person or a coding agent to see, triage, edit, and pick up work in a project backlog. The native backend is Markdown files in Git. Other trackers plug in through adapters. Hooks, actions, and an event stream let other tools, such as Worklease, Hum, Tell, Herdr, and CI, coordinate around it.

It replaces Backlog.md for the owner's projects. These are the problems it fixes:

| Backlog.md problem | `bl` answer |
| --- | --- |
| Slow at scale: `task list --json` takes 2 s at 10k tasks, and dependencies need one `task view` process per task | Parse files in-process with a stat-validated cache (D23). No per-item process anywhere. |
| Views freeze after idling. Repeated `j` lags on long lists. | No I/O or O(N) work on the UI loop. Detail rendering is async. Idle soak and held-key budgets are acceptance criteria (D14, §11). |
| Each view is a separate CLI command, and the web UI is the "good" UI | One TUI with tabbed views and list, board, and tree layouts. No web UI (D1). |
| Filters, sort, and search are clunky | One query language everywhere, plus single-key filter, sort, and group menus (D13). |
| Weak "what's next" for agents | Deterministic ready ranking by priority, rank, and unblock impact, with explanations (D12). |
| Long CLI commands for agents | Short verbs, numeric IDs, `--json`, and stdin bodies, plus MCP (D19). |
| Editing means rewriting the whole document | Pickers and single-key edits, `.` repeat, undo, quick-add, and per-section `$EDITOR` editing (tui.md). |
| Title slugs in filenames, status folders, HTML section markers, dotted subtask IDs, branch scanning | `ID.md` filenames, a flat directory, plain headings, parent as a field, and a shared control root (D5–D7). |

Success looks like this:

- Nothing in the TUI waits on I/O.
- Every common edit is at most two keystrokes from the list.
- An agent selects, claims, starts, updates, and finishes work with five short commands.
- Every view lives in one program.

## 2. Scope

In scope:

- Viewing, filtering, searching, and triaging items
- Field and content edits, creation, dependencies, and hierarchy
- Readiness and next-item selection
- Agent CLI and MCP
- Hooks, actions, and events
- Optional Worklease claims
- Backends: native, Backlog.md, Beads, GitHub Issues, Linear, Jira Cloud, and external adapters

Non-goals:

- A web UI
- A claim authority (Worklease's job)
- An agent runtime or scheduler (actions hand off to existing tools)
- A docs or wiki manager
- Tracker administration: workflows, teams, schemas, permissions
- Cross-system atomic transactions
- A sync server or daemon
- In-process plugins

## 3. Decision register

| ID | Decision |
| --- | --- |
| D1 | Scope is in §2. Anything listed there as a non-goal needs a register change before work starts. |
| D2 | Ship one static Go binary, `bl` (`CGO_ENABLED=0`), module `github.com/brettinternet/backlog`. CLI framework: `urfave/cli/v3`. Mise pins the Go toolchain. This matches Worklease and Hum, so their adapters and conventions port directly. |
| D3 | Build the TUI on Bubble Tea v2, Lip Gloss v2, and Bubbles v2 (`charm.land/...`). A spike (BL-14) confirms the stack against §11 before TUI feature work. Fall back to v1 only with recorded evidence. |
| D4 | The native format ([format.md](format.md)) is the primary backend and the only one `bl` fully owns. Backlog.md is supported by `bl import backlog-md` and a file-level adapter that reads and writes Backlog.md files directly. `bl` never shells out to the Backlog.md CLI. |
| D5 | **Control root.** Invoked from a linked worktree, `bl` reads and writes the backlog in the repository's primary worktree, so every checkout and agent sees one live backlog. Resolve the primary worktree by reading `.git` and `commondir` files, without spawning `git`. `root: checkout` in project config opts out. A bare repository with no primary worktree falls back to the invoking checkout with a warning. `bl doctor` warns when the control checkout is not on the default branch. |
| D6 | **IDs** are `PREFIX-N`. They are allocated under an exclusive lock, with a counter in the Git common directory that all worktrees share, seeded from the highest existing ID. `bl check` detects duplicates across clones and `bl renumber` repairs them, rewriting references. Imported IDs are kept verbatim, including zero padding and dotted suffixes. `bl` never allocates dotted IDs. |
| D7 | **Files.** Each item lives at `backlog/<ID>.md` in one flat directory. Archived items move to `backlog/archive/`, which default loads skip. Titles never appear in filenames, and status never moves files. |
| D8 | **Writes** are atomic: temp file, fsync, rename, all under the repository write lock. They are minimal-diff: untouched lines stay byte-identical, and unknown frontmatter keys, comments, and Markdown are preserved. |
| D9 | **Git.** `bl` never fetches, pulls, or pushes. With `git.autoCommit` on, `bl` commits only the paths it wrote, leaves other staged changes alone, and skips with a warning during a merge, a rebase, or on a detached HEAD. Default off. |
| D10 | **Model.** The shared item model (§4.2) also carries raw provider fields. Per-source capabilities gate every operation. An unknown capability counts as unavailable, and the UI offers only available actions. |
| D11 | **Status categories** are `backlog`, `todo`, `active`, `done`, and `canceled`. The raw provider status is always displayed. The category drives readiness, done-hiding, and board column order. |
| D12 | **Readiness and next** follow §6: deterministic ranking with a machine-readable explanation for every included and excluded candidate. |
| D13 | **One query language** ([query.md](query.md)) serves the TUI filter bar, CLI arguments, saved views, and MCP. Sources may push down predicates they support. The result never depends on pushdown. |
| D14 | **UI loop discipline.** No I/O and no work proportional to backlog size happen on the Bubble Tea update loop. Filter, sort, and group projections run on a worker over immutable snapshots, with latest-wins cancellation. Detail and Markdown rendering are async and cached by item version, width, and theme. |
| D15 | **Edits are optimistic.** A change applies locally at once and shows as pending until the source confirms. Failures and conflicts stay visible. Every write first checks the item version (a content hash for files, the provider version or `updatedAt` for remote sources) and never silently overwrites. Session undo and redo apply inverse mutations. |
| D16 | **Configuration layers** (§5): built-ins, then user config, then committed project config, then user per-project overrides, then environment, then flags. Committed config that would run a program or contact a network service is inert until the user runs `bl trust`. Trust is bound to a digest of those entries. |
| D17 | **Credentials** never appear in committed config. They come from a user-configured credential-helper argv or an environment variable per account, are held only in memory, and are never logged or passed to hooks. |
| D18 | **Extension seams:** external source adapters (JSON-RPC 2.0 over stdio), the coordination seam (D20), hooks, actions, the event log, and MCP. No in-process plugins, scripting runtime, or SDK until two external consumers need one. Worklease's Python-era SDK shipped before any adapter existed and had to be removed. |
| D19 | **Agent interface.** Short verbs, numeric ID shorthand, and `--json` on every command, with one versioned envelope and stable exit codes (§7). Long text comes from stdin. MCP tools mirror the CLI operations. `bl instructions` prints guidance matched to the installed version. |
| D20 | **Coordination.** `bl` knows about claims, not about Worklease. Claims are a typed engine input, not a hook: per-item claim state, plus acquire, release, and list with the outcomes `acquired`, `contended` (with holder), `uncertain`, and `unavailable`. Readiness, `next --claim`, and claim-gated Start branch on those outcomes, and exit-code hooks cannot express them. Worklease is the only implementation, through its stable `--json` CLI (§9). `bl` never implements claims, and Worklease never depends on `bl`. A public coordinator protocol on the BL-42 framing waits for a second claim system (D18). Workflow around claims, such as releasing on done, launching agents, or creating worktrees, uses hooks and actions, not built-in behavior. Resource keys use Worklease's matching provider policy (`github`, `linear`) so claims contend with any other Worklease client on the same issue. Every other source uses `generic` with a stable source identity and item key: the project ID and item ID for native and Backlog.md items, and provider-stable IDs for remote items (never a key that changes when an issue moves). |
| D21 | **Terminal and process safety.** Terminal control sequences are stripped from all displayed provider text. Hooks and actions receive context through environment variables and stdin JSON, never through a shell. Argv and `cwd` placeholders are limited to `{id}`, `{ref}`, `{source}`, `{url}`, `{root}`, `{branch}`, `{resource}`, and `{result.KEY}` (§8). Every expanded value is checked: no control characters, no leading `-`, at most 1 KiB. A failed check refuses the run. |
| D27 | **Automation model.** Hooks react to item changes, derived readiness changes, and the outcomes of other hooks and actions. Chains are explicit (`on: action.finished`), carry results as bounded JSON, and are capped by a causal depth of 3. The event log is the only dedupe point, so each event is emitted once across processes without a daemon (§8). |
| D22 | **Local state.** The ID allocator and write lock live in the Git common directory. Events, UI state, and trust records live in XDG state. The cache lives in XDG cache and is disposable. Nothing `bl` keeps outside the repository is required for correctness. |
| D23 | **Cache.** A disposable SQLite cache (`modernc.org/sqlite`, WAL) gives every source a warm start, provides FTS search, and holds remote sync cursors. Native sources validate cached rows by file stat and reparse only changed files. Refresh is single-flight per source across processes. |
| D24 | **Backends**, in delivery order: native, Backlog.md (file adapter), Beads (`bd` 1.3.0 JSON CLI), GitHub Issues (with Projects v2 status), Linear, Jira Cloud, external. Port from Worklease v1.9.3 where it applies, but drop Worklease's receipt and recovery journal; D15 replaces it. |
| D25 | **Worklease becomes claims-only.** Worklease TASK-156 removes its work queue. Its provider probe evidence stays referenced at tag `v1.9.3` (§10). |
| D26 | **Budgets.** The §11 budgets are acceptance criteria, measured on the reference host (Apple M1 Max, 32 GiB) with the deterministic fixture generator. CI tracks relative regressions only and never asserts absolute latency. |

## 4. Architecture

```text
 native files   Backlog.md files   bd CLI   GitHub   Linear   Jira   external (stdio JSON-RPC)
       \               |              |        |        |       |       /
        source adapters: Load / Watch / Read / Apply, capabilities, raw fields
                                     |
   engine: per-source sync and scheduler -> immutable snapshot -> projections (worker)
           mutations (optimistic, version-checked, undo) -> events -> hooks
                 |                      |                         |
           SQLite cache            readiness/next          Worklease overlay
                                     |
                  TUI (Bubble Tea)   CLI (--json)   MCP (stdio)
```

### 4.1 Packages

| Package | Owns |
| --- | --- |
| `cmd/bl` | Entry point and build metadata |
| `internal/model` | Item, Ref, StatusCategory, Priority, Edge, Mutation (with inverse), Capabilities |
| `internal/format` | Native file parsing and serialization with minimal-diff edits |
| `internal/project` | Project discovery, control root, identity, layered config, trust |
| `internal/source` | Source interface and registry; one subpackage per backend (`native`, `backlogmd`, `beads`, `github`, `linear`, `jira`, `external`) |
| `internal/query` | Query language: parse, evaluate, sort, group, and the quick-add token parser |
| `internal/engine` | Snapshot store, projections, readiness/next, mutation pipeline, undo, events |
| `internal/cache` | SQLite cache and FTS |
| `internal/hooks` | Hook and action execution, the event log |
| `internal/worklease` | Claim overlay and claim operations through the Worklease CLI |
| `internal/tui` | Bubble Tea application |
| `internal/cli` | Commands and the JSON envelope |
| `internal/mcp` | MCP stdio server |

Dependencies point downward. `tui`, `cli`, and `mcp` depend on `engine`. Only `engine` and `source/*` touch sources. Only `cli` and `mcp` format output.

### 4.2 Item model

| Field | Notes |
| --- | --- |
| `ref` | `{source, id}`. `id` is the provider's display ID (`BL-12`, `ENG-431`, `#42`), with a stable provider key kept privately. |
| `title`, `type` | `type` is free text from config: task, bug, feature, epic, spike, decision, chore. |
| `status`, `category` | Raw status key or name, plus the D11 category |
| `priority` | `urgent`, `high`, `medium`, `low`, or unset. Raw value kept. |
| `rank` | Manual order key (lexicographic string) |
| `assignees`, `labels`, `milestone`, `due` | Lists and strings. `due` is a date. |
| `parent`, `children` | Hierarchy. `children` is derived. |
| `blockedBy`, `blocks`, `related`, `duplicateOf` | Typed edges. Only `blockedBy` is a hard prerequisite. Inverses are derived. |
| `refs`, `files` | Links or repository paths, and the declared file scope |
| `description`, `criteria`, `dod`, `plan`, `notes`, `summary`, `comments` | Body sections. Remote sources usually supply only `description` and `comments`. |
| `created`, `updated`, `url`, `version` | `version` is opaque: a content hash for files, the provider version otherwise. |
| `fields` | Raw provider fields kept for display and round-trip |

### 4.3 Source contract

```go
type Source interface {
    Info() Info                                      // id, kind, display, capabilities
    Load(ctx context.Context, since Cursor) (Delta, error) // full or incremental, with coverage
    Watch(ctx context.Context, changed func()) error // optional change signal
    Read(ctx context.Context, id string) (Item, error)   // full detail, including comments
    Apply(ctx context.Context, m Mutation) (Item, error) // checks m.ExpectedVersion
}
```

Coverage is explicit: complete, partial, or unknown. Partial or stale coverage never proves that an item was deleted, and never makes an unknown blocker look satisfied (Worklease D23 and D27 lessons). Each source gets its own worker, so one slow or rate-limited source never stalls another, and the header shows a badge per source.

### 4.4 Mutations

1. Validate against capabilities.
2. Run `before` hooks, which can veto (§8).
3. Apply optimistically to the snapshot and mark the item pending.
4. Call `source.Apply` with the expected version.
5. On success, replace the item with the returned version, append an event, and run `after` hooks.
6. On failure, revert and show the error. On a version conflict, show both versions and never retry automatically.

Undo applies the stored inverse mutation through the same pipeline.

## 5. Projects, configuration, and trust

**Discovery.** Walk up from the working directory to the Git root, then resolve the control root (D5). The project config is `.config/backlog.yaml` at the control root. Without it, detect `backlog/config.yml` or `backlog.config.yml` (Backlog.md) and `.beads/` (Beads). Show them read-only and suggest `bl import` or an explicit source. Outside any repository, open the user's default workspace, if configured.

**Identity.** `project.id` defaults to the normalized `origin` remote (`github.com/owner/repo`). If there is no remote, it falls back to the control root path, marked host-local, and claims show as host-local. The project ID keys caches, trust, events, and claim resources.

**Files.**

```yaml
# .config/backlog.yaml (committed). Non-secret project facts only.
version: 1
project: {id: github.com/brettinternet/backlog, name: backlog}
sources:
  - {id: tasks, type: native, path: backlog, prefix: BL}
  # - {id: eng, type: linear, team: ENG}            # network source: requires `bl trust`
statuses:
  - {key: todo, name: To Do, category: todo}
  - {key: in-progress, name: In Progress, category: active}
  - {key: done, name: Done, category: done}
workflow: {start: in-progress, done: done}
ready: {statuses: [todo], excludeLabels: [blocked, deferred]}
defaults: {type: task, dod: ["task ci passes on the final commit"]}   # applied by `bl new`
views:
  - {name: Next, query: "is:ready", sort: next}
  - {name: Mine, query: "assignee:me is:open"}
coordination: {worklease: {}}
hooks: []     # requires `bl trust`
actions: []   # requires `bl trust`
```

```yaml
# ~/.config/backlog/config.yaml (user). Identity, accounts, overrides.
me: {default: "@brett"}
accounts:
  linear: [{name: work, credential: [security, find-generic-password, -s, linear-api, -w]}]
  github: [{host: github.com, user: brettinternet, credential: [gh, auth, token, --hostname, github.com, --user, brettinternet]}]
projects:
  github.com/acme/app:              # private sources for a repo you cannot commit config to
    sources: [{id: jira, type: jira, site: acme.atlassian.net, project: APP, account: work}]
workspaces:
  - {name: mine, projects: [~/dev/me/worklease, ~/dev/me/hum, ~/dev/me/backlog]}
keys: {}       # keymap overrides (tui.md)
```

Precedence follows D16. `bl config show` prints the effective config and where each value came from. Each file has a JSON Schema under `docs/schemas/` for editor completion.

**Trust.** Privileged entries are hooks, actions, external adapter executables, and network-backed sources. When they come from committed config, they stay inert until `bl trust` records the SHA-256 of the canonical privileged subset for that control root. Any change to those entries needs re-trust, the same model as `mise trust`. Untrusted entries show as a banner and in `bl doctor`. User config is always trusted.

## 6. Readiness and next

An item is a **candidate** when all of the following hold:

1. Its category is in `ready.statuses` (default `[todo]`) and it is not archived.
2. Every `blockedBy` target is known, and its category is in `ready.satisfiedBy` (default `[done, canceled]`).
3. It has no open children (`ready.skipContainers`, default true).
4. It carries no label from `ready.excludeLabels`.
5. It is not assigned only to other people.
6. With Worklease enabled, no other holder has claimed it.

Readiness is one of these values, and each has a reason:

| Readiness | Meaning |
| --- | --- |
| `ready` | All candidate conditions hold |
| `blocked` | A known unsatisfied blocker or an excluded label |
| `waiting` | Assigned to or claimed by someone else |
| `unknown` | A missing, unresolvable, or stale blocker |
| `ineligible` | Status or container |

A known unsatisfied blocker means `blocked`, even when other edges are unknown.

**Ranking** is total and deterministic:

1. Priority, urgent first, unset last
2. `rank` ascending, ranked items before unranked ones
3. Unblock impact: the count of open items transitively blocked by this one, descending
4. `created` ascending
5. Natural ID order

Each result explains itself. For example, `high · rank a3 · unblocks 4 (BL-9, BL-12, +2)`.

`bl next [QUERY] [-n N] [--explain]` narrows the candidates with a query. `--explain` also lists every excluded item with its reason. The TUI **Next** view shows the same order and explanations.

## 7. Agent interface

Commands (final names freeze in BL-7 and BL-8):

```text
bl                                   open the TUI
bl ls [QUERY] [--view V]             list items
bl show ID [--section S]             show one item
bl next [QUERY] [-n N] [--explain] [--claim [--start] --session S]
bl new TITLE [TOKENS] [--body -] [--ac TEXT]...   quick-add tokens: tui.md
bl set ID... FIELD=V FIELD+=V FIELD-=V
                                     e.g. status=active priority=high label+=bug blocked_by+=BL-7
bl start ID | bl done ID             workflow transitions
bl note ID TEXT|- | bl comment ID TEXT|-
bl ac ID (check|uncheck) N... | bl ac ID add TEXT | bl ac ID rm N
bl edit ID [--section S]             open $EDITOR
bl archive ID... | bl unarchive ID...
bl claim ID | bl release ID          Worklease
bl init | bl import backlog-md [PATH] | bl check [--fix] | bl renumber FROM TO
bl events [--follow] | bl action NAME ID | bl trust | bl config show | bl doctor
bl instructions [TOPIC] | bl mcp
```

- **ID shorthand.** `12`, `012`, and `BL-12` all resolve within the default source. Use `source:ID` when IDs are ambiguous.
- **JSON envelope.** `--json` (`-j`) prints one envelope:
  - success: `{"schemaVersion":1,"ok":true,"command":"next","data":{...}}`
  - error: `{"schemaVersion":1,"ok":false,"command":"set","error":{"reason":"conflict","message":"...","details":{}}}`
- **Exit codes.**

  | Code | Meaning |
  | --- | --- |
  | 0 | Success |
  | 1 | Internal error |
  | 2 | Usage error |
  | 3 | Not found |
  | 4 | Conflict |
  | 5 | Rejected (validation or hook veto) |
  | 6 | Unavailable (capability, trust, or source) |
  | 7 | Contention (claimed elsewhere) |

  Exit codes and reasons are a public contract with tests.
- **MCP tools:** `next`, `query`, `show`, `create`, `update`, `append`, `criteria`, `action`, `claim`, and `release`. They share types and validation with the CLI.

## 8. Extensions

`bl` stays naive about workflow. It reports what changed, runs what you configured, and records what happened. Claims are the one exception, because they are decision inputs (D20).

**Events.** The project event log in XDG state holds one JSON line per event:

```json
{"schemaVersion":1,"id":"01JB…","type":"item.updated","time":"2026-10-21T17:06:00Z",
 "project":"github.com/brettinternet/backlog","source":"tasks","ref":"tasks:BL-12",
 "changes":{"status":{"from":"todo","to":"in-progress"}},"item":{…summary after…},
 "actor":"@brett","origin":"tui","cause":null,"depth":0}
```

| Type | Emitted when |
| --- | --- |
| `item.created`, `item.updated`, `item.archived` | A mutation succeeds, from any origin, or sync sees an external edit (origin `sync`) |
| `item.readiness` | An item's readiness changes because of item data: its status, blockers, children, labels, or assignees. `changes.readiness` holds `from`, `to`, and the reason. Claims never trigger it, because Worklease's own events cover claims. |
| `claim.acquired`, `claim.released` | A claim operation made through `bl` succeeds |
| `hook.finished`, `action.finished` | An after hook or an action exits. The event holds the name, `outcome` (`ok`, `failed`, `timeout`, or `refused`), the exit code, the duration, and `result`. |

- **Origin:** `tui`, `cli`, `mcp`, `hook`, `action`, or `sync`.
- **Cause and depth:** an event produced by a hook or action, directly or through a `bl` call it makes, names its `cause` event, with `depth` one higher. Hooks don't run for events deeper than 3. `BL_HOOK_DEPTH` carries the depth into `bl` subprocesses.
- **Exactly once.** Any `bl` process that loads a newer snapshot diffs it against the cache's previous state and proposes the resulting events. Each event is appended under the log lock and skipped if the log already holds one with the same type, ref, and producing versions. Only the process that appends an event runs its hooks. No daemon is needed. Changes made while nothing runs are emitted by the next `bl` invocation, and `bl events --follow` keeps a watcher running.
- `bl events [--follow] [--json] [--since ID] [QUERY]` reads and tails the log.

**Hooks** subscribe to event types. This example covers all three: `x` creates a worktree, a chained hook starts a claimed agent inside it, and readiness changes send notifications.

```yaml
actions:
  - name: work
    key: x
    when: "is:ready"
    run: [wt, switch, --create, "{branch}", --base, main, --no-cd, --format=json]
hooks:
  - name: agent
    on: action.finished
    if: "action:work outcome:ok"           # query over the item, plus event terms
    phase: after                            # before | after
    cwd: "{result.path}"                    # from wt's JSON stdout
    run: [worklease, run, --detach, --name, "{id}", --ref, "{ref}", --resource, "{resource}", --, pi, -p, "Implement {id}"]
    timeout: 30s
  - name: notify-ready
    on: item.readiness
    if: "is:ready"
    run: [tell, notify, "Ready: {id}"]
```

| Phase | Behavior |
| --- | --- |
| `before` | Only for `item.*` mutation events. Runs synchronously with the proposed event on stdin. A non-zero exit or a timeout vetoes the mutation (exit 5) and shows the first stderr line. |
| `after` | Runs without blocking the caller. After hooks on one event run concurrently, with a bound. Order dependent steps by chaining on `hook.finished`. The CLI hands after hooks to a detached `bl` runner, so commands return at once. Nothing retries automatically. |

`cwd` defaults to the directory the chain's originating command ran in. When sync started the chain, it defaults to the control root.

**Actions** are named commands run on one item from the TUI (`!` menu or a bound key), the CLI (`bl action NAME ID`), or MCP. Each declares `name`, `run`, optional `cwd` and `key`, a `when` query, `confirm`, and `detach`. A non-detached action suspends the TUI the way `$EDITOR` does. Every action ends in an `action.finished` event, so hooks can chain after it.

**Results.** A hook or action may write one JSON object of at most 8 KiB to the path in `BL_RESULT` (the Worklease `WORKLEASE_RUN_RESULT` pattern). It becomes `result` in the finished event, and string fields expand as `{result.KEY}` in hooks triggered by that event. If a hook writes no result and its stdout is a single JSON object of at most 8 KiB, that object is the result, so JSON-printing tools such as `wt --format=json` chain without a wrapper.

**Placeholders** (D21) expand inside argv elements and `cwd`:

| Placeholder | Value |
| --- | --- |
| `{id}`, `{ref}`, `{source}`, `{url}`, `{root}` | Item ID, `source:ID`, source ID, provider URL, control root |
| `{branch}` | Lowercase ID plus a title slug of ASCII letters, digits, and `-`, at most 48 characters, cut at a word boundary. "Debounce detail rendering" becomes `bl-12-debounce-detail-rendering`. `yb` copies the same value. |
| `{resource}` | The item's claim resource key (§9). Unavailable without coordination. |
| `{result.KEY}` | A top-level string in the triggering event's `result` |

A hook or action whose placeholders cannot expand doesn't run. A hook records a `refused` outcome, and an action is offered as disabled with the reason.

**Environment.** Hooks and actions get `BL_ITEM_ID`, `BL_ITEM_REF`, `BL_SOURCE`, `BL_PROJECT_ID`, `BL_ROOT`, `BL_BRANCH`, `BL_RESOURCE` (when available), `BL_EVENT_ID`, `BL_HOOK_DEPTH`, and `BL_RESULT`. The triggering event arrives on stdin as JSON. The process starts in a minimal environment with credentials removed (D17, D21).

**External source adapters** speak a versioned JSON-RPC 2.0 stdio protocol: initialize and manifest, capabilities, paged list, read, apply with expected version, and cancellation, all with per-frame and per-response bounds. BL-42 freezes it. It derives from Worklease's external adapter protocol v1 without the receipt and recovery journal.

## 9. Coordination and Worklease

These features turn on when `coordination.worklease` is configured and `worklease` is on `PATH`. `bl` uses only Worklease's stable `--json` schema-v2 CLI.

- **Resource key:** GitHub and Linear items use `worklease key --provider github|linear` with the provider's own source and item identity. Native and Backlog.md items use `worklease key --provider generic --source <project.id> --item <ID>`. Other remote items use `generic` with the adapter's stable source identity and item key, for example the Jira site and numeric issue ID. `bl` derives keys in-process and tests them against vectors produced by `worklease key`.
- **Process context:** `bl` runs `worklease` from the invoking working directory, not the control root, so Worklease's contextual handle (keyed by worktree root and session) matches the agent's own later `worklease heartbeat`, `release`, and `verify` calls. The session comes from `--session`, `BL_SESSION`, or `WORKLEASE_SESSION_ID`.
- **Overlay.** `worklease --json list` polls while the TUI is visible and after each claim operation. Expiry rechecks are scheduled locally, because expiry emits no event. Columns show `mine`, `agent@host`, or expired. The `is:claimed` and `claimed:me` queries use the overlay.
- **Claim and release.** `bl claim ID` acquires with no wait. On contention it exits 7 and names the holder.
- **Start.** Claim first, then change status. These are separate outcomes: a failed status change keeps the claim and reports it. If the session already holds the item's claim, for example inside `worklease run --resource {resource}`, Start skips the acquire.
- **`next --claim`.** Walks the §6 order from one snapshot and acquires each candidate with no wait. It skips contended items, holds at most one claim, stops on an uncertain acquire, and never loops (Worklease D28).
- The caller owns heartbeat and release. Everything else is ordinary hooks and actions, shipped as documented examples rather than built-in behavior: release on `done`, launch an agent with `worklease run --resource {resource}`, or open a worktree for a claimed item.
- Another claim system later implements the same coordination seam. Nothing in hooks, actions, or events names Worklease.

## 10. Backends

| Source | Access | Identity | Edges | Writes | Evidence |
| --- | --- | --- | --- | --- | --- |
| native | Files | `ID` | `blocked_by` and parent fields | All | [format.md](format.md) |
| Backlog.md | Files, no CLI | Task ID, filename slug tolerated | `dependencies`, `parent_task_id` | Status, fields, sections, AC, comments | Worklease §3 Backlog.md 1.52.0 |
| Beads | `bd` 1.3.0 `--json --readonly --sandbox` | Issue ID | Bulk `dependencies`. `blocks` is hard. | `bd update` and comment | Worklease §3 Beads |
| GitHub | GraphQL and REST over HTTPS, one serial queue per host and account | `owner/repo#N` | `blockedBy` and sub-issues | State, labels, assignees, comments, Projects v2 status | Worklease §3 GitHub and Projects |
| Linear | GraphQL over HTTPS | Organization and issue UUID, identifier displayed | Directional `blocks` | State, assignee, priority, labels, comments | Worklease §3 Linear |
| Jira Cloud | REST v3 with API token | Issue ID, key displayed | Issue links | Workflow transitions, fields, comments | Probe pending (BL-40) |
| external | JSON-RPC stdio | Adapter-defined | Adapter-defined | Capability-declared | BL-42 |

"Worklease §3" means [worklease `docs/work-queue-tui-proposal.md` §3 at v1.9.3](https://github.com/brettinternet/worklease/blob/v1.9.3/docs/work-queue-tui-proposal.md), which holds live probe results:

- Linear: page cap of 250, moving-cursor gaps, and relation adds that don't bump `updatedAt`
- GitHub: ETag and quota behavior, and Projects v2 updates that don't bump the issue's `updatedAt`
- Beads: embedded Dolt and bulk dependencies

Port the adapter code from `internal/queue` at that tag.

## 11. Performance

All budgets are measured on the reference host with the fixture generator (BL-12). D26 applies.

| Operation | Budget (p95) |
| --- | --- |
| TUI first paint, warm cache, 10k items | ≤ 100 ms |
| TUI first paint, cold native load, 10k | ≤ 400 ms, list visible before the load completes |
| Navigation key to rendered frame (Update+View), 50k | ≤ 4 ms in-process; ≤ 16 ms to PTY output |
| Held `j` at 40 Hz for 5 s, 10k | No accumulated lag; the selection stops within one frame of key release |
| Filter keystroke to updated list | 10k ≤ 30 ms; 50k ≤ 80 ms, never blocking input |
| One external file change to visible update, 10k | ≤ 150 ms after the filesystem event |
| `bl show ID` | ≤ 30 ms |
| `bl ls` and `bl next`, warm, 10k | ≤ 150 ms |
| Native single-field write, excluding hooks and Git | ≤ 50 ms |
| Idle for 1 h | ~0 % CPU, no goroutine or heap growth, input still responsive |
| Peak RSS | 10k ≤ 150 MiB; 50k ≤ 400 MiB |

Known hazards and their required mitigations:

1. **O(N) work on the UI loop.** Worklease measured 350 ms at 50k for a full re-projection on the loop. Mitigate with worker projections, prepared rows, and claim patches applied in place (D14).
2. **Per-item subprocesses.** Never spawn one per item. Spawn one bulk `bd` call per refresh.
3. **Markdown rendering.** It is slow and allocation-heavy. Render only the selected item, asynchronously, cached by version and width. Debounce while the selection is moving.
4. **File watching on macOS.** fsnotify's kqueue backend opens one file descriptor per file in a watched directory, so 10k–50k items means tens of thousands of descriptors and slow startup. Watch directory entries only (atomic renames change them), add a low-rate stat sweep for in-place edits, and degrade to polling on `EMFILE`.
5. **Held-key backlog.** Coalesce repeated navigation, keep `View` O(viewport), and never render detail synchronously on navigation.
6. **Idle freezes.** Never let background goroutines block on UI sends. Use latest-value mailboxes, bounded channels, and context-cancelled tickers. Prove it with a soak test.
7. **Remote quotas.**
   - Linear complexity and request quotas
   - GitHub secondary limits
   - Jira cost limits

   Mitigate with one serialized scheduler per account, honoring `Retry-After` and reset headers, plus ETags and overlapping reconciliation scans.
8. **Cross-process cache contention.** Use WAL with a busy timeout and single-flight refresh per source.
9. **Huge items.** Load comments and history lazily, and cap rendered body size with a "show more" control.
10. **Width errors.** Measure cells with `x/ansi` for CJK, emoji, and controls.
11. **Process spawning in hot CLI paths.** Do not run `git` for root resolution, and never run `worklease` per row.

## 12. Testing

Follow [development.md](development.md#tests). Test the native format with golden and fuzz tests. Test query and readiness as table-driven unit tests. Test the TUI through model `Update`/`View` and a small number of PTY tests. Test adapters against fake HTTP or fake CLIs. Real-provider round trips are opt-in (`BL_PROVIDER_TESTS=1`). Benchmarks are `Benchmark*` functions or gated by environment variables and never assert absolute time in the default run.

## 13. Milestones

| Milestone | Outcome |
| --- | --- |
| m-0 Foundation | Native format, project/config, the agent CLI, next, check, import, cache, benchmarks, and `bl instructions`. Ends by moving this repository's own backlog onto `bl` (BL-26). |
| m-1 TUI | Stack spike, engine, list/detail/board/tree, filters and views, editing, search, performance and soak verification |
| m-2 Agents and automation | MCP, events, trust and doctor, hooks, actions, Worklease, Git auto-commit |
| m-3 Backends | Sync framework, Backlog.md, Beads, GitHub, Linear, Jira, external protocol, workspaces |
| m-4 Release and adoption | Release pipeline, documentation, migration of the owner's other repositories |

## 14. Prior art

- **Backlog.md:** the format and workflow this replaces (§1).
- **[Worklease](https://github.com/brettinternet/worklease) v1.9.3 work queue:** a provider-backed queue TUI with a decision register, provider probes, a benchmark harness, and adapters. Reuse its evidence and code; avoid its scope creep into tracker features inside a claim tool.
- **[ltui/jtui/sctui](https://github.com/runpantheon/ltui):** per-tracker Textual apps with a JSON cache-first startup, a background refresh, and single-key edits. Borrow the feel: instant cached paint, `y` to yank a branch name, `x` to hand off to an agent. Skip the separate app per tracker.
- **[Beads](https://github.com/gastownhall/beads):** dependency-aware agent tracker with hash IDs. `bl` prefers human-friendly sequential IDs with a worktree-shared allocator (D6).
- **lazygit, Helix, Linear:** which-key popups, a command palette, and single-key triage.
