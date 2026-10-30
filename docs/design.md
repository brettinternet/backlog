# Design

**Status:** Plan of record. Backlog tasks cite decision IDs (`D#`). Changing a decision means updating this register and the affected sections in the same commit. Interfaces not yet frozen in [cli.md](cli.md) are illustrative here.

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
- Linked changes: worktrees, branches, pull requests, and landed commits (D43)

Non-goals:

- A web UI
- A claim authority (Worklease's job)
- An agent runtime or scheduler (actions hand off to existing tools)
- A docs or wiki manager
- Tracker administration: workflows, teams, schemas, permissions
- Cross-system atomic transactions
- A sync server or daemon
- In-process plugins
- Storing or reading agent transcripts (D40)
- Merging, branch management, and CI administration (D43)

## 3. Decision register

| ID | Decision |
| --- | --- |
| D1 | Scope is in §2. Anything listed there as a non-goal needs a register change before work starts. |
| D2 | Ship one static Go binary, `bl` (`CGO_ENABLED=0`), module `github.com/brettinternet/backlog`. CLI framework: `urfave/cli/v3`. Mise pins the Go toolchain. This matches Worklease and Hum, so their adapters and conventions port directly. |
| D3 | Build the TUI on Bubble Tea v2, Lip Gloss v2, and Bubbles v2 (`charm.land/...`), qualified by [BL-14's measurements](spikes/tui-stack.md): viewport-only navigation, a 120 FPS renderer cap to meet the ≤ 16 ms PTY budget, coalesced detail requests, and asynchronous Glamour v2 Markdown rendering. Retain D14's worker/cache discipline; BL-25 qualifies the full application's remaining §11 budgets and one-hour soak. Fall back to v1 only with recorded evidence. |
| D4 | The native format ([format.md](format.md)) is the primary backend and the only one `bl` fully owns. Backlog.md is supported by `bl import backlog-md` and a file-level adapter that reads and writes Backlog.md files directly. Both cover task files, drafts, archives, milestone files, and the configured label list. Docs and decisions are not managed (§2). `bl` never shells out to the Backlog.md CLI and does not imitate its CLI or MCP. |
| D5 | **Control root.** Invoked from a linked worktree, `bl` reads and writes the backlog in the repository's primary worktree, so every checkout and agent sees one live backlog. Resolve the primary worktree by reading `.git` and `commondir` files, without spawning `git`. Both `backlog.yaml` and `backlog.local.yaml` load from the control root. `root: checkout` in either file in the primary or invoking checkout opts out; routing reads primary shared, primary local, invoking shared, then invoking local. A bare repository with no primary worktree falls back to the invoking checkout with a warning. `bl doctor` warns when the control checkout is not on the default branch. |
| D6 | **IDs** are `PREFIX-N`. They are allocated under an exclusive lock, with a counter in the Git common directory that all worktrees share, seeded from the highest existing ID. `bl check` detects duplicates across clones and `bl renumber` repairs them, rewriting references. Imported `PREFIX-N` IDs are kept verbatim, including zero padding and dotted suffixes; drafts outside the prefix and slug IDs are renumbered, keeping the old ID in `legacy_id`. `bl` never allocates dotted IDs. |
| D7 | **Files.** Each item lives at `backlog/<ID>.md` in one flat directory. Archived items move to `backlog/archive/`, which default loads skip. Titles never appear in filenames, and status never moves files. |
| D8 | **Writes** are atomic: temp file, fsync, rename, all under the repository write lock. They are minimal-diff: untouched lines stay byte-identical, and unknown frontmatter keys, comments, and Markdown are preserved. |
| D9 | **Git.** `bl` never fetches, pulls, or pushes. With `git.autoCommit` on, `bl` commits only the paths it wrote, leaves other staged changes alone, and skips with a warning during a merge, a rebase, a cherry-pick, or on a detached HEAD. Default off. Native writes commit once per CLI command; TUI edits batch after two seconds idle and flush on quit, outside the update loop. Project-enabled Git execution requires trust (D16). Dirty or externally changed target paths skip the batch rather than absorb another writer's edits; Git failures warn without reverting file writes. |
| D10 | **Model.** The shared item model (§4.2) also carries raw provider fields. Per-source capabilities gate every operation. An unknown capability counts as unavailable, and the UI offers only available actions. |
| D11 | **Status categories** are `backlog`, `todo`, `active`, `done`, and `canceled`. The provider status itself is always displayed, never only its category: its configured status name, else the provider's display name for an opaque key (Linear state, GitHub Projects option), else the raw value. Opaque assignee IDs (Linear user UUID, Jira accountId) likewise display the provider's user name. Display names are display only: stored values, queries, `@me`, claims, and writes keep the stable ID, and pickers and completion show the name but insert the ID. "Me" is per source, in that source's assignee form: a provider source's account `user` (GitHub login, Linear viewer UUID, Jira accountId), else `me.default`. It drives `assignee:me`, `is:mine`, the readiness assignee gate, `@me`, and assign-me. The category drives readiness, done-hiding, board column order, and the status icon. |
| D12 | **Readiness and next** follow §6: deterministic ranking with a machine-readable explanation for every included and excluded candidate. |
| D13 | **One query language** ([query.md](query.md)) serves the TUI filter bar, CLI arguments, saved views, and MCP. Sources may push down predicates they support. The result never depends on pushdown. |
| D14 | **UI loop discipline.** No I/O and no work proportional to backlog size happen on the Bubble Tea update loop. Filter, sort, and group projections run on a worker over immutable snapshots, with latest-wins cancellation. Detail and Markdown rendering are async and cached by item version, width, and theme. |
| D15 | **Edits are optimistic.** A change applies locally at once and shows as pending until the source confirms. Failures and conflicts stay visible. Every write first checks the item version (a content hash for files, the provider version or a composite content fingerprint for remote sources). Native writes check atomically. Where a remote API has no compare-and-swap (GitHub issue/Projects v2, Linear mutations, and Jira Cloud), a fresh provider read immediately before one non-retried mutation rejects observed conflicts but cannot eliminate a concurrent preflight-to-write race; no unsupported CAS guarantee is implied. A remote create has no version to read: it validates its template, sends one non-retried mutation, and reports `unknown-outcome` when a failure after dispatch leaves the result unknown; its undo archives the new item. GitHub has no archive, so undo deletes the issue when the viewer can delete it and its version still matches the one create returned, else closes it as not planned; the result says which, and a delete leaves no redo. A create needing a follow-up mutation (Linear blocker relations; GitHub parent, blockers and project status) sends each once after the item exists; if one or the read-back fails, the `unknown-outcome` refusal carries the created item's ref, which the remote wrapper keeps while scrubbing provider text, so CLI, MCP and TUI callers learn what to check. Such a create records no undo. The TUI checks against the version it displayed. CLI and MCP callers may pass the version they read (`--if-version`, §7), so a write based on stale data, such as a criterion number that shifted, fails instead of landing on the wrong target. Session undo and redo apply inverse mutations. |
| D16 | **Configuration layers** (§5): built-ins, then user config, then shared project config (`backlog.yaml`), then private project config (`backlog.local.yaml`), then user per-project overrides, then environment, then flags. Both repository-file layers that would run a program, contact a network service, or enable `git.autoCommit` are inert until the user runs `bl trust`. Trust is bound to a digest of those entries. |
| D17 | **Credentials** never appear in committed config. Helper/environment accounts remain user-configured and memory-only. OAuth is a separate per-account user-only opt-in (`oauth: true`), with access/refresh tokens in an owner-private XDG state store (0700 directory, 0600 atomic records), never the config, disposable cache, argv, logs, events, MCP output, hooks, or actions. Refresh is serialized across processes per account; each rotated pair is fsynced before releasing its lock and the provider principal is reverified. OAuth is available only for in-tree qualified public-client flows (Linear user authorization with S256 PKCE and github.com GitHub App device authorization with separate owner-approved read-only/write Apps); unsupported providers remain helper/environment-only. Account status (`bl auth status`, doctor's `accounts` check) is offline: it reads config, environment presence, and stored grants, and never runs helpers, refreshes, or contacts a provider; reads verify the principal. |
| D18 | **Extension seams:** external source adapters (JSON-RPC 2.0 over stdio), the coordination seam (D20), hooks, actions, the event log, and MCP. No in-process plugins, scripting runtime, or SDK until two external consumers need one. Worklease's Python-era SDK shipped before any adapter existed and had to be removed. |
| D19 | **Agent interface.** Short verbs, numeric ID shorthand, and `--json` on every command, with one versioned envelope and stable exit codes (§7). Read command names `ls`, `show`, and read-only `next`, and write command names `new`, `set`, `start`, `done`, `note`, `comment`, `ac`, `edit`, `archive`, and `unarchive`, are frozen with their data shapes in [cli.md](cli.md). `list` and `create` are aliases for `ls` and `new`. Long text comes from stdin. MCP tools mirror the CLI operations and return their `--json` data shapes. `bl instructions` prints guidance matched to the installed version. |
| D20 | **Coordination.** `bl` knows about claims, not about Worklease. Claims are a typed engine input, not a hook: per-item claim state, plus acquire, release, list, and per-item history with the outcomes `acquired`, `contended` (with holder), `uncertain`, and `unavailable`. Readiness and `next --claim` branch on those outcomes, and exit-code hooks cannot express them. Worklease is the only implementation, through its stable `--json` CLI (§9). `bl` never implements claims, and Worklease never depends on `bl`. A public coordinator protocol on the BL-42 framing waits for a second claim system (D18). **Claim rules attach to status transitions in the engine**, so the CLI, TUI, and MCP behave identically. Entering an active category acquires first, and on contention nothing is written. Entering a closed category releases the session's claim after the write. Workflow around claims, such as launching agents or creating worktrees, uses hooks and actions, not built-in behavior. **One key per item.** In a `bl` project, every client claims the key `bl` reports for the item (§9). Worklease's `backlog-md` policy derives a different, host-local key, and claims on different keys never contend. **One handle per session.** `bl` runs Worklease from the control root (D5) with an explicit caller context, so a session finds its claim from every worktree and from MCP. Resource keys use Worklease's matching provider policy (`github`, `linear`) so claims contend with any other Worklease client on the same issue. Every other source uses `generic` with a stable source identity and item key: the project ID and item ID for native and Backlog.md items, and provider-stable IDs for remote items (never a key that changes when an issue moves). A host-local project ID never claims through a remote authority, because its keys could not contend across hosts (§9). |
| D21 | **Terminal and process safety.** Terminal control sequences are stripped from all displayed provider text. Hooks and actions receive context through environment variables and stdin JSON, never through a shell. Argv and `cwd` placeholders are limited to `{id}`, `{ref}`, `{source}`, `{url}`, `{root}`, `{branch}`, `{resource}`, and `{result.KEY}` (§8); agent resume and fork commands (D40) add `{agentSession}`, `{session}`, `{agent}`, `{cwd}`, and `{host}`. Every expanded value is checked: no control characters, no leading `-`, at most 1 KiB. A failed check refuses the run. |
| D22 | **Local state.** The ID allocator and write lock live in the Git common directory. Events, UI state, and trust records live in XDG state. The cache lives in XDG cache and is disposable. Nothing `bl` keeps outside the repository is required for correctness. |
| D23 | **Cache.** A disposable SQLite cache (`modernc.org/sqlite`, WAL) gives every source a warm start, provides FTS search, and holds remote sync cursors. Native sources validate cached rows by file stat and reparse only changed files. Refresh is single-flight per source across processes. |
| D24 | **Backends**, in delivery order: native, Backlog.md (file adapter), Beads (`bd` 1.3.0 JSON CLI), GitHub Issues (with Projects v2 status), Linear, Jira Cloud, external. Port from Worklease v1.9.3 where it applies, but drop Worklease's receipt and recovery journal; D15 replaces it. |
| D25 | **Worklease becomes claims-only.** Worklease TASK-156 removes its work queue. Its provider probe evidence stays referenced at tag `v1.9.3` (§10). |
| D26 | **Budgets.** The §11 budgets are acceptance criteria, measured on the reference host (Apple M5 Max, 64 GiB; updated with owner approval in BL-12) with the deterministic fixture generator. CI tracks relative regressions only and never asserts absolute latency. In BL-25 the owner clarified idle CPU at 10k as ≤ 2% of one core (not host-normalized), retaining the two-second native stat sweep. |
| D27 | **Automation model.** Hooks react to item changes, derived readiness changes, and the outcomes of other hooks and actions. Chains are explicit (`on: action.finished`), carry results as bounded JSON, and are capped by a causal depth of 3. The event log is the only dedupe point, so each event is emitted once across processes without a daemon (§8). |
| D28 | **Evidence and merges.** Evidence for a criterion is written under that criterion, never in Notes by number, so it can't point at the wrong one after a renumber. A file with conflict markers fails to parse with `merge-conflict`. `bl check --fix` resolves the two conflicts that every edit and comment produce, `updated` and appended comments, and leaves anything else for you. `bl` doesn't install a Git merge driver, because a driver needs per-clone Git config and runs without the project's lock. |
| D29 | **Drafts.** A draft is an item in a `backlog`-category status, so it is open but never ready. `bl init` and `bl import backlog-md` configure a `draft` status, and `bl new --status draft` creates one without a moment in which it is ready to claim. Native IDs never change on promotion, so the importer numbers Backlog.md drafts in the source prefix, as `backlog draft promote` would. A Backlog.md source keeps its folders: changing a status to or from draft moves the file between `drafts/` and `tasks/` and allocates an ID there, as `draft promote` and `task demote` do, and also rewrites references, which Backlog.md 1.52 leaves dangling. |
| D30 | **Workspaces.** Compose independently resolved project contexts, including statuses, trust, events and claims. In composed views only, source keys are `url.QueryEscape(project.id)/source-id`, giving stable identity even when projects reuse source and item IDs. A `source:` query term matches a composed key in full or by its bare source ID. Bare relation IDs stay project/source local; explicit `source:ID` edges resolve only when unique in the loaded scope. Ambiguous or absent targets remain unknown. Workspace scope is a worker projection, not a merged project configuration. |
| D31 | **Sprints.** Linear cycles and Jira sprints share one model: an item carries one opaque sprint key (its display name in `Names`), and each source publishes a sprint catalog of key, name, state (`active`, `future`, `closed`) and dates. `current` is the active sprint and `next` the earliest future one, per source, resolved from the catalog rather than the clock, so a sprint starting changes results without any issue update. The catalog refreshes on every sync, independent of the incremental issue cursor. Sprint work is demand-gated: sources request sprint data only when a query, view, group or `ready.sprint` mentions sprints, and that demand is sticky in the shared cache so a sprint-free process never strips cached keys. Item versions exclude the sprint, so write CAS is identical in both modes. A `sprint:` term or `group:sprint` with no capable source is an error, and so is a project-level `ready.sprint` when the project has none (each workspace project is checked on its own). `ready.sprint` gates only items from capable sources, so other items in a mixed project stay eligible and a user-wide default is inert where no source has sprints. Hook `if:` refuses `sprint:` terms, and event readiness keeps capable-source items ineligible under `ready.sprint`, because events carry no catalog. Jira reads one request per distinct board (at most 10) for open sprints, not one per sync; the Agile API is on the same host, so no re-trust. GitHub Projects v2 iteration fields fit the same model: a project is sprint-capable only with an iteration field, chosen by `githubProject.iterationFieldId` if set, else the single iteration field present; several fields with no configured choice is a refusal, not a guess. GitHub marks no iteration active, so the adapter classifies states itself when the catalog is fetched (query evaluation stays clock-free, same as Linear and Jira): a `completedIterations` entry is always closed; within `iterations` (current and upcoming, in list order), the first entry with `startDate <= today < startDate + duration` is active and every other entry is future, even one whose own dates have already elapsed — a break with no entry in range leaves the whole list future, never active. `bl set ID sprint=current|next|none|NAME|KEY` and the TUI sprint picker resolve the value against a freshly loaded catalog (closed, unknown or ambiguous targets refuse) on Linear and Jira sources only. Because versions exclude the sprint, a sprint write also carries the sprint the caller observed (the TUI's displayed value, else a fresh read), and the provider forces a sprint read on its preflight and refuses `conflict` if it differs. Linear writes `issueUpdate(cycleId)`; Jira posts to `/rest/agile/1.0/sprint/{id}/issue`, or `/rest/agile/1.0/backlog/issue` for none. Writing the GitHub sprint and native sprints are not implemented. |
| D32 | **GitHub status keys** have fixed categories, assigned by the adapter: `open` todo, `closed` done (canceled when closed as not planned), `unknown` backlog, `draft` backlog when any GitHub source has a project (D38), and each Projects v2 option ID its mapped category. A `statuses:` entry can't change them, so with any GitHub source configured, an entry for one of these keys with another category fails config load, naming the key, both categories, and the fix; `closed` accepts done or canceled. An option ID mapped to different categories by two GitHub sources fails the same way. Keys no GitHub source assigns are unconstrained. `bl doctor` reports the load error. |
| D33 | **Create target.** One engine function resolves where a new item goes for the CLI, TUI, and MCP; the first match wins: an explicit source (`--source`, MCP `source`, or a `+ID` quick-add token, which must agree), then `defaults.source` from any config layer, then the only source whose capabilities allow Create. Anything else, including an unknown or non-creatable source, is a usage error listing the sources that can create, so adding a remote source that can't create never breaks creates that omit the source. TUI `n` preselects the resolved source and asks only when nothing resolves; `N` stays in the parent's source. |
| D34 | **GitHub label statuses.** A GitHub source without Projects v2 may map issue labels to categories (`statusLabels`). An open issue with exactly one mapped label has that label's key as its status; none stays `open` (todo), so labels only add statuses and an unlabeled issue means the same with or without them; several is `unknown` (backlog), the key Projects mode uses for an unmapped option, so every GitHub key keeps one category (D32). Several mapped labels fail closed with a load warning naming them rather than picking one. A closed issue is `closed` whatever its labels. Names match case-insensitively, as on GitHub. `status=LABEL` replaces the mapped labels in the single label write that keeps the others; `status=open` reopens a closed issue, else removes them; a label status on a closed issue is refused rather than combined with a reopen, so each status write stays one mutation. To keep unlabeled issues from being ready, leave `open` out of `ready.statuses`. Making unlabeled issues `unknown` was rejected: adding one label status silently moved every plain issue from todo to backlog. `statusLabels` with `githubProject`, an invalid category, labels differing only in case, or a label named `open`, `closed` or `unknown` fails config load. |
| D35 | **Workflow resolution per source.** Each source reports the status keys it accepts, with categories, through one provider-neutral interface: GitHub derives them from config (`open`, `closed`, plus `statusLabels` keys or, with `allowWrites`, `githubProject.options`), Linear reports the bound team's states, and Jira the issue's discovered transitions. Native, Backlog.md, and external sources accept every configured status unless `sources[].statuses` narrows them; that key on a GitHub, Linear, or Jira source fails config load. `start` and `done` resolve in one engine function shared by the CLI, MCP (`update` `operation: start|done`), TUI `S`/`D`, and `bl next --claim --start`: the project `workflow` key (or the one key it includes, D36) if the item's source accepts it in the active or done category, else that source's only status in the category, else the source's default for the category (GitHub's `closed` for done, so closing wins over a project's Done option), else `rejected` naming the source, before any claim is acquired. A status write a reporting source doesn't accept is refused the same way, as is any write, create, or editor save giving a narrowed source a status outside `sources[].statuses`, and a reported category gates claims (D20). The TUI status picker and `<`/`>` offer only the item's accepted statuses, in config order (an included key at its entry's position, D36) then the source's undeclared keys; a step with no accepted status in its direction is disabled naming the source. No per-source workflow override until an ambiguity needs one. |
| D36 | **Status entries include other sources' keys.** A `statuses` entry may list keys from other sources under `includes`, so items across sources share one board column, `status:` term, status sort and group position, and write target. It's a projection only: items keep and display their own key (D11), and queries for a member key match only that key. Writing an entry, from `status=`, board moves, quick-add, or `workflow` (D35), resolves in the engine to the one key among the entry and its includes that the item's source accepts, before any claim is acquired; none or several is `rejected` naming the source. The TUI resolves on the source worker, never on the update loop (D14). A key belongs to at most one entry, a member can't itself be an entry, and a GitHub-assigned member category must match its entry's (D32); each fails config load. Matching by display name was rejected because a provider rename would silently split a column. |
| D37 | **Project-scoped GitHub sources.** A GitHub source with `githubProject` and no `repo` reads every issue in the project, across repositories, with item IDs `OWNER/REPO#N` (refs between its issues use the same form) and the claim key a repository source would derive (D20), so both sources claim an issue alike. Pull requests are skipped and drafts load as drafts (D38); items the account can't read come back redacted and each load counts them in one warning. Writes target the item's repository and recheck permission there before dispatch. Sync reads project items updated since the last scan plus each previously seen repository's issues updated since then, because a comment bumps the issue but not its project item (measured, [github-verification.md](github-verification.md#bl-129-live-verification)); reconciliation enumerates the project. It creates and deletes only drafts (D33), can't convert them, and `statusLabels` (D34) needs a `repo`. With both `repo` and `githubProject`, the source stays repository-scoped and `repo` is the create target. Making the project the scope for every bound source was rejected: it would change existing IDs and claim keys and drop issues outside the project. |
| D38 | **GitHub project drafts.** A Projects v2 draft issue in the bound project loads as an item with ID its project item node ID (`PVTI_…`), status `draft` in the backlog category (D29, D32), and title, description, and assignees; it has no labels, comments, relations, milestone, or sprint, and writing them is `rejected`. With no issue number it has no claim key (D20), so it can't be claimed. Every draft write needs `allowWrites`. Edits use `updateProjectV2DraftIssue` and `bl new --status draft` uses `addProjectV2DraftIssue`, both after the usual preflight (D15); undo deletes only an unchanged created draft. Setting another status converts it into an issue in `repo` with `convertProjectV2DraftIssueItemToIssue`, then sets the option or closes the issue; the item's ref becomes `#N`, and a failure after the conversion is `unknown-outcome` naming the new issue, since the draft ID no longer resolves. A project-only source (D37) has no conversion target and refuses. Keying drafts by DraftIssue ID was rejected: the project item ID is what every project mutation takes, and the conversion keeps it. Skipping drafts, as before, hid board ideas from `bl`. |
| D39 | **TUI sessions.** With no `--session`, `BL_SESSION`, or `WORKLEASE_SESSION_ID`, the TUI coordinates each item under its own session, `tui:` plus a hash of the item's claim resource, so a person can start, finish, claim, and release without knowing sessions exist, and can hold several items at once: a Worklease session holds one claim. The name derives from the resource because Worklease recovers a held claim or pending acquire only through the same selector: reopening the TUI in any terminal resumes every claim, every source of an item (D37) finds the same claim, a key change keeps it (D20), and the name fits Worklease's 128-byte session limit. Handles are already scoped by Worklease home and control root, and the `agentId` still names the holder. An item's claim is mine only under that item's session. Every TUI window shares these sessions, which is right for one person; simultaneous operations on one item can return `handle-in-use`. An explicit session coordinates every item under it, as for an agent loop. The CLI and MCP still require an explicit session. A single shared `tui` session was replaced (BL-133) because it limited a person to one claim; a claim held under it lapses within one lease. A per-process or per-TTY session was rejected: a closed pane would strand its claim until the lease expired, with no way back to it. |
| D40 | **Item history.** An item's history is composed from local sources that degrade one at a time: events, claim history, comments, and the Git history of the item's file. Nothing is written to item files, and nothing in it is needed for correctness. Each event a `bl` command appends under a caller session carries a `session` object: the session ID, the detected agent kind and its harness session ID, the host, and the checkout and branch `bl` ran in, read without starting Git (D5). Values pass D21's checks; a failing value is dropped, never failing the write. Agent kinds come from `agents:`, keyed by kind, whose `session` names the environment variable that detects the agent and holds its session ID. Built-ins cover Claude Code, Codex, and Pi. Only built-in and user layers may set `agents:`, because the same entries hold the `resume` and `fork` argv that reopen a session, so `bl doctor` reports the key as ignored in project and local layers. `bl resume ID [SESSION]` and the TUI `!` menu reopen a recorded agent session with that argv, without a shell, in the session's recorded checkout; an attached command gets the terminal and no timeout, and `detach` runs it in the background like a detached action. A closed item forks when `fork` is configured, so a finished transcript isn't extended against code that moved on. Resume refuses `other-host`, `checkout-removed`, `live` (a claim held and unexpired by any session on the same agent conversation, or a contact channel reports its agent running, looked up for every candidate session whatever its age, so two processes would write one transcript; the refusal lists the session's contact channels; without claim support, contact alone decides), and `no-agent-config`, and ends in `action.finished` named `resume` or `fork` with the session. MCP never reads the caller from server environment (§7): callers pass `agent` and `agentSession`. TUI `tui:` sessions (D39) record only their ID, so they never look resumable, and `sync` events record none. `bl show` folds the item's events and claim history into one entry per session, read through an indexed `ref` column in the existing disposable event index rather than by scanning every log; the index file name carries its schema version, so an older `bl` sharing the state directory keeps its own index. A handoff (`bl handoff`, MCP `append` kind `handoff`) is a `session.handoff` event holding at most 4 KiB of text; it needs a session but no claim, because PR and review sessions often hold none, and `bl show` keeps each session's newest. Handoffs stay local unless `history.handoff: comment`, set in any layer, which also posts the text as a comment under one line naming the agent kind and date, with D15 version checks; host, checkout, branch, and session IDs are never published, and a source that can't comment records locally and reports `comment: unsupported`. The project's `history.commitTrailer: KEY` makes `bl instructions workflow` ask for a `KEY: ID` trailer on each commit for an item; unset, `bl` gives no commit guidance, so it imposes no Git workflow. Contact channels (a Herdr pane, a Claude Code peer name, user-configured ones such as hcom) are looked up from a session's recorded `agent`, `agentSession`, and `host` each time `bl show` reads a held or recently seen session, never recorded on events, item files, or Worklease claims: handles change during a session, pane IDs are reused, and some sit beside secrets. Each resolver reports `ok` with its channels or `unavailable` with a reason, independently, within one 100 ms budget. Built-in resolvers run only for sessions on this host; the Claude Code resolver reads only `sessionId`, `name`, `status`, `kind`, and `peerProtocol` from its session files, never key files or messaging sockets, and reports an unknown `peerProtocol` as unsupported. User config's `contact:` adds argv resolvers, run without a shell in the hook environment with `{agent}`, `{agentSession}`, `{session}`, `{host}`, and `{cwd}` under D21's value checks, or disables a built-in with `false`; project and local layers can't set it. `bl` lists channels but never sends messages itself; the agent or person chooses whether to use one. `internal/history` merges providers into one ordered list for the TUI History tab, `bl history`, and MCP `history`. Each provider reports `ok`, `partial`, or `unavailable` with a reason, never failing the others. Entries carry stable IDs, deduplicated across providers (a claim seen in both the event log and Worklease appears once), and an entry may head a group with a span; a session's entries nest under its session row through the same generic group path any later provider (changes, CI) uses. Remote sources with the read-only `read-activity` capability add provider activity: the GitHub issue timeline (labels, assignees, close and reopen, renames, cross-references, linked and referencing commits, and Projects v2 status changes when bound to a project), Linear issue history, and the Jira changelog. It is fetched only when History opens or `bl history` runs, never during sync, through the account's request queue, redacted, and cached by item version in the principal's cache directory, keeping at most 1,000 entries, the newest on GitHub and Jira (`partial` past that). Each entry keeps the provider's ID and name; provider entries are never fuzzily deduplicated against local ones. A failed or rate-limited fetch is that provider's status line. Git history is one `git log --follow` per item file, without external diff, textconv, or fsmonitor, in D21's minimal environment, cached by item version and HEAD. Session rows carry contact channels looked up for every session when the history is read, on the TUI's worker, and say `resumable` or the check resume would refuse; `!` on one offers resume and fork for that session; a resumable row says so in a `resumable` field, and the TUI shows `! resume` on it only while it is selected, because key hints on every row would clutter the timeline. A `bl history` cursor fingerprints the entries before it and refuses with `conflict` when they changed, since an entry added to an earlier session sorts before the cursor and paging on would skip it. Every rendering labels each entry with its source (`item`, `claim`, `agent`, `git`, `comment`, the provider, and so on) rather than prefixing summaries. An edit's summary names each changed field compactly, and its detail carries what the summary abbreviates as plain marked lines (`+ `, `- `, `[x] `, `[ ] `), so the CLI, MCP, and TUI share one text form and the TUI only colors it; a structured diff type was rejected as more surface than one consumer needs. Workspace items resolve every provider in their own project (D30). Storing or reading transcripts was rejected: they are the harness's data, large, and private. |
| D41 | **Platforms.** `bl` builds, tests, and runs on macOS, Linux, and Windows (amd64 and arm64). Platform code sits behind small packages: `filelock` (`flock`, or `LockFileEx` on a temp-directory file for directory locks, since Windows can't lock a directory handle), `atomicfile` (D8 rename, which on Windows retries a target held open for about 1.3 s, then refuses `unavailable` with the original whole; directory fsync is a no-op there), `editor` (`$EDITOR` through `/bin/sh`, or split into argv on Windows, default `notepad`), and `proctree` (process group, or Job Object). Cache file identity on Windows is volume serial, file index, size, last write time and NTFS change time. The OAuth store checks an owner-only DACL instead of mode bits. Claims on Windows need Worklease 1.13.0, its first Windows release (BL-109); the coordinator probes `worklease version --json` there until a probe succeeds. With an older or missing Worklease, claim commands, active transitions and `--run` writes refuse `unavailable` naming that version, and `bl doctor` fails its Worklease check naming it; other status writes skip claim checks and need no session, and reads warn that claims are unavailable. Windows CI runs `go test ./...`, then the real-Worklease coordination tests against the pinned Windows release; tests needing a Unix facility skip with the reason. Releases publish Windows `.zip` archives, installed by `install.ps1` with the same checksum checks as `install.sh`. |
| D42 | **Assignee means owner.** An item's assignees are the people accountable for it, not the agent executing it. The claim records the executor and its lease, and history records each session that touched the item (D40). Agents working on a person's behalf leave assignees unchanged, so the item stays in that person's `assignee:me` and `is:mine`, and readiness never reports it `waiting` as assigned to others. Assigning the agent was rejected: it duplicated the claim without its lease, so the assignee outlived the work, and it hid the owner's items from Mine and readiness. |
| D43 | **Linked changes.** Items show linked changes: worktrees, branches, pull requests (and later merge requests), and landed commits. Links are derived into memory and the cache, plus explicit `refs` URLs, and never written to item files. A change is shown at its furthest stage, so a worktree, its branch, its pull request, and the merge that landed it are one change that collapses once merged. Links come from a branch name (or last `/` segment) starting with an item ID, pull request titles, closing keywords and the forge's closing references, the `history.commitTrailer`, and first-parent merge subjects, with only `origin`'s pull requests joined to local branches and merge subjects; an ID naming several items links each as `unknown`, and a refs URL bl can't resolve stays `unknown` rather than disappearing. Unknown and stale never read as merged, and stale checks never read as passing: a forge whose refresh failed keeps its cached pull requests as `stale`, and an open pull request's checks are stale when they ran on another commit than the local branch head. Git is read from `.git` without starting Git (D5); the only processes are one bounded first-parent `git log` of the default branch (1,000 commits) when its head moves, cached by head commit, and, for the item shown, `rev-list --count`, `merge-base`, `diff --raw --numstat` from the merge base, and `status --porcelain` with bounded reads of untracked files to count their lines (working tree reads skipped when repository config defines a filter driver, which they would run), plus, for the change view's change, `log` of its commits, its landed commits' diffs, and on its Diff tab `diff --patch --no-ext-diff --no-textconv`, all on workers (D14), with `--no-pager --no-optional-locks --literal-pathspecs`, fsmonitor and signature display off, `GIT_NO_LAZY_FETCH=1`, `GIT_TERMINAL_PROMPT=0`, and D21's minimal environment. Forges are configured under `changes.forges`, separately from sources, and reuse accounts, credentials, request queues, redaction, and trust; without one, a GitHub `origin` with exactly one account for its host is the forge. Each refresh reads every open pull request, whose checks change without touching its update time, and any other updated since the last, keeping the newest 1,000 in a redacted `.pulls.json` beside the account's provider cache. The TUI shows a list column, a Changes tab, a change view (Overview, Files, Diff, Commits; a pull request without a local head reads the forge's pull request files, patches, and commits; `enter` on a History commit opens its diff), and History groups; queries gain `has:change`, `change:`, `checks:`, and `review:` (refused in hooks, since events carry no changes); `bl changes` and MCP `changes` return the same data. Actions naming `{change.*}` placeholders run only on a linked change. `bl` never fetches, merges, rebases, or manages branches or CI. Review writes are capability-gated, and tours are rendered from external files, never generated; both and GitLab are later phases of BL-103. |

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
| `internal/history` | Item history composed from events, claims, comments, Git, providers, and linked changes (D40) |
| `internal/changes` | Linked changes: local and forge data, linking, and the tracker that refreshes them off the UI loop (D43) |
| `internal/git` | Reading refs, worktrees, and reflogs from `.git`, and running Git safely where files can't answer |
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
| `assignees`, `labels`, `milestone`, `due` | Lists and strings. `due` is a date. `milestone` is a key that the source's milestone catalog may name (§5). |
| `parent`, `children` | Hierarchy. `children` is derived. |
| `blockedBy`, `blocks`, `related`, `duplicateOf` | Typed edges. Only `blockedBy` is a hard prerequisite. Inverses are derived. |
| `refs`, `files` | Links or repository paths, and the declared file scope |
| `description`, `criteria`, `dod`, `plan`, `notes`, `summary`, `comments` | Body sections. Remote sources usually supply only `description` and `comments`. |
| `created`, `updated`, `url`, `version` | `version` is opaque: a content hash for files, the provider version otherwise. |
| `fields` | Raw provider fields kept for display and round-trip |
| `names` | Provider display names of opaque `status` and `assignees` values, for display only (D11) |

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

Native deltas may supply immutable per-item storage shared with the snapshot
store. Sources and consumers never mutate that shared storage; adapters without
it are copied defensively. Public snapshot reads still return independent
collections. Shared items have individual allocations, not pointers into a full
backlog array that would pin old versions after edits.

Coverage is explicit: complete, partial, or unknown. Partial or stale coverage never proves that an item was deleted, and never makes an unknown blocker look satisfied (Worklease D23 and D27 lessons). Each source gets its own worker, so one slow or rate-limited source never stalls another, and the header shows a badge per source.

Remote sources use the [shared remote boundary](remote.md): cached rows and cursors
commit atomically, bounded warm rows paint before network I/O, and incomplete
refreshes retain unseen rows as stale. Only a fresh complete enumeration evicts
unseen rows; complete incremental responses retain unchanged rows. Stale items
are not ready and stale blockers cannot satisfy dependencies. Read lists, Go To,
and `next` may use fresh partial observations; unknown coverage and error findings
still fail closed. A change window can't see edits that leave the provider's
change timestamp alone, so single-item reads reconcile on demand: `next --claim`
reads a remote candidate fresh and skips it unless it is still ready, a fresh
read from `show`, detail, or a write preflight replaces that cached row (or
marks it stale when gone), and a manual refresh (`R`) runs a full
reconciliation ([linear.md](linear.md#sync), [github.md](github.md#sync)). Workspace reranking preserves per-row staleness. This applies
D12/D23 without treating partial inventories as complete for events or deletions.
Host/account HTTP queues and user-only credential resolution follow D17 and D23.

### 4.4 Mutations

1. Validate against capabilities.
2. Run `before` hooks, which can veto (§8).
3. Apply optimistically to the snapshot and mark the item pending.
4. Call `source.Apply` with the expected version.
5. On success, replace the item with the returned version, append an event, and run `after` hooks.
6. On failure, revert and show the error. On a version conflict, show both versions and never retry automatically.

Undo applies the stored inverse mutation through the same pipeline.

## 5. Projects, configuration, and trust

**Loading.** `internal/project` resolves XDG paths, reads Git metadata without spawning Git, finds the control root and identity, and loads the D16 layers with per-value provenance. Mappings merge by key; lists replace, including with an empty list. An omitted boolean differs from an explicit `false`. `BL_HIGH_CONTRAST` and `--high-contrast` are the environment and flag overrides. `root: checkout` in the invoking or primary checkout's config opts out of the shared control root.

Config files are strict single-document YAML up to 1 MiB. Unknown keys, duplicate keys or milestone keys, and aliases or anchors are rejected with file, line, and key. Loading never writes, runs programs, or contacts providers. Credentials stay in memory and never appear in output. See [cli.md](cli.md#bl-config-show) for `config show`, `trust`, and `doctor`.

**Workspaces** list project paths in user config (`~/` expands; relative paths resolve from the user config directory; duplicate project IDs are rejected). Each project loads its own configuration. Writes, trust, and claims route to the item's own project and source (D30). Saved-view tabs add up across layers without changing list replacement in effective settings ([tui.md](tui.md#views-and-layouts)).

**Discovery.** Walk up from the working directory to the Git root, then resolve the control root (D5). Either `backlog.yaml` or `backlog.local.yaml` at the control root marks a project. The old `.config/backlog.yaml` path is rejected: run `git mv .config/backlog.yaml backlog.yaml`. Without either file, detect `backlog/config.yml` or `backlog.config.yml` (Backlog.md) and `.beads/` (Beads). Open Backlog.md read-only, and detect Beads, suggesting `bl import` or an explicit source. Backlog.md source behavior and supported edits are specified in [backlog-md.md](backlog-md.md). Outside any repository, open the user's default workspace, if configured.

**Identity.** Shared and local files settle `project.id` before the user `projects.<id>` lookup, in one pass. `project.id` defaults to the normalized `origin` remote (`github.com/owner/repo`), ignoring transport, credentials, port, and `.git`. If there is no remote, or `origin` is a local path or `file://` URL, it falls back to the control root path, marked host-local, and claims show as host-local. The project ID keys caches, trust, events, and claim resources.

**Files.**

```yaml
# backlog.yaml (committed). Non-secret project facts only.
version: 1
project: {id: github.com/brettinternet/backlog, name: backlog}
sources:
  - {id: tasks, type: native, path: backlog, prefix: BL}
  # - {id: eng, type: linear, site: ORGANIZATION_UUID, team: TEAM_UUID, account: work} # requires `bl trust`; see linear.md
statuses:
  - {key: draft, name: Draft, category: backlog}
  - {key: todo, name: To Do, category: todo}
  - {key: in-progress, name: In Progress, category: active}
  - {key: done, name: Done, category: done}
workflow: {start: in-progress, done: done}
milestones:
  - {key: m-0, name: Foundation, archived: true}
  - {key: m-2, name: Agents and automation, description: "MCP, events, hooks, actions, and claims"}
labels: [tui, perf, blocked, deferred]   # suggestions; items may carry others
ready: {statuses: [todo], excludeLabels: [blocked, deferred]}
defaults: {type: task, dod: ["task ci passes on the final commit"]}   # applied by `bl new` to native and Backlog.md sources
views:
  - {name: Next, query: "is:ready", sort: next}
  - {name: Mine, query: "assignee:me is:open"}
  - {name: Sprint, query: "sprint:current", layout: board}   # Linear or Jira sources
coordination: {worklease: {}}
hooks: []     # requires `bl trust`
actions: []   # requires `bl trust`
```

```yaml
# ~/.config/backlog/config.yaml (user). Identity, accounts, overrides.
me: {default: "@brett"}
accounts:
  linear: [{name: work, user: VIEWER_UUID, credential: [security, find-generic-password, -s, linear-api, -w]}]
  github: [{host: github.com, user: brettinternet, credential: [gh, auth, token, --hostname, github.com, --user, brettinternet]}]
projects:
  github.com/acme/app:              # private sources for a repo you cannot commit config to
    sources: [{id: jira, type: jira, site: acme.atlassian.net, project: APP, account: work}]
workspaces:
  - {name: mine, projects: [~/dev/me/worklease, ~/dev/me/hum, ~/dev/me/backlog]}
keys: {}       # keymap overrides (tui.md)
```

**Catalogs.** A source may supply a milestone catalog and a label list. The native source takes both from project config. The Backlog.md source reads its milestone files (including `archive/milestones/`) and the `labels` list in its `config.yml`. A catalog never restricts values. An item may name an undeclared milestone or label, and `bl` keeps it as written. Pickers and completion offer declared, unarchived entries first, then values in use. Milestone group headers and the detail pane show the name next to the key, and milestone groups follow catalog order. `bl` never edits a catalog. Change project config or Backlog.md's milestone files instead.

`backlog.local.yaml` uses the project schema and is not a credential store; accounts remain in user config. It overrides shared config with the same mapping-merge and list-replace rules. `bl` never writes it or edits `.gitignore`, so auto-commit cannot include it. Ignore `/backlog.local.yaml`; `bl doctor` warns if the file is tracked or not ignored. The repository author can commit it, so it receives no extra trust.

Precedence follows D16. `bl config show` prints the effective config and where each value came from. Each file has a JSON Schema under `docs/schemas/` for editor completion.

**Trust.** Privileged entries are hooks, actions, external adapter executables, and network-backed sources. Along with `git.autoCommit`, when they come from either repository-file layer (`project` or `local`), they stay inert until `bl trust` records the SHA-256 of the canonical privileged subset for that control root. Any change to those entries needs re-trust, the same model as `mise trust`. Untrusted entries show as a banner and in `bl doctor`. User config is always trusted.

## 6. Readiness and next

An item is a **candidate** when all of the following hold:

1. Its category is in `ready.statuses` (default `[todo]`) and it is not archived.
2. Every `blockedBy` target is known, and its category is in `ready.satisfiedBy` (default `[done, canceled]`).
3. It has no open children (`ready.skipContainers`, default true).
4. It carries no label from `ready.excludeLabels`.
   With `ready.sprint` (`current` or `next`), an item from a sprint-capable
   source is in that sprint (D31). Project config with `ready.sprint` and no
   such source is refused.
5. It is not assigned only to other people.
6. With Worklease enabled, no other holder has claimed it.

Readiness is one of these values, and each has a reason:

| Readiness | Meaning |
| --- | --- |
| `ready` | All candidate conditions hold |
| `blocked` | A known unsatisfied blocker |
| `waiting` | Assigned to or claimed by someone else |
| `unknown` | A missing, unresolvable, or stale blocker |
| `ineligible` | Status, container, an excluded label, or, for sprint-capable sources, outside `ready.sprint` |

A known unsatisfied blocker means `blocked`, even when other edges are unknown.

**Ranking** is total and deterministic:

1. Priority, urgent first, unset last
2. `rank` ascending, ranked items before unranked ones
3. Unblock impact: the count of open items transitively blocked by this one, descending
4. `created` ascending
5. Natural ID order

Each result explains itself. For example, `high · rank a3 · unblocks 4 (BL-9, BL-12, +2)`.

The pure engine's input contract, reason codes, tie fallbacks, and graph semantics are documented in [readiness.md](readiness.md).

`bl next [QUERY] [-n N] [--explain]` narrows the candidates with a query. `--explain` also lists every excluded item with its reason. The TUI **Next** view shows the same order and explanations.

## 7. Agent interface

The read commands (`ls`, `show`, `next`) and write commands (`new`, `set`, `start`, `done`, `note`, `comment`, `ac`, `edit`, `archive`, `unarchive`) are frozen; [cli.md](cli.md) specifies their flags and JSON shapes. Bare `bl` prints help off a terminal; `bl tui` exits 6 there, and the TUI with `--json` is a usage error. Help is plain text, even with `--json`. Global `-C DIR` sets the invocation directory without changing the process working directory.

Commands:

```text
bl                                   open the TUI
bl tui [QUERY] [-V NAME]             open the TUI narrowed by QUERY, on tab NAME
bl ls [QUERY] [--view V]             list items
bl show ID [--section S]             show one item
bl history ID [--limit N] [--cursor C]   an item's timeline (D40)
bl next [QUERY] [-n N] [--explain] [--claim [--start] --session S]
bl new TITLE [TOKENS] [--status S] [--body -] [--ac TEXT]...   quick-add tokens: tui.md
bl set ID... FIELD=V FIELD+=V FIELD-=V
                                     e.g. status=active priority=high label+=bug blocked_by+=BL-7
bl start ID | bl done ID             workflow transitions
bl note ID TEXT|- | bl comment ID TEXT|-
bl ac ID (check|uncheck) N... | bl ac ID check N --evidence TEXT
bl ac ID add TEXT | bl ac ID set N TEXT | bl ac ID rm N   --dod targets definition of done
bl edit ID [--section S]             open $EDITOR
bl edit ID --section S --text TEXT|- replace a section without an editor
bl archive ID... | bl unarchive ID...
bl claim ID | bl release ID | bl heartbeat    claims (§9)
bl handoff ID TEXT|- --session S     record this session's handoff (D40)
bl init | bl import backlog-md [PATH] | bl check [--fix] | bl renumber FROM TO
bl events [--follow] | bl action NAME ID | bl trust | bl config show | bl doctor
bl instructions [TOPIC] | bl mcp | bl auth status|login|logout
```

- **ID shorthand.** `12`, `012`, and `BL-12` resolve across configured native sources, with numeric forms using each source's prefix. Padding is preserved in output. An ambiguous ID exits 2 and lists source-qualified matches; use `source:ID` to disambiguate. Missing IDs exit 3.
- **Versions.** `bl show --json` and every write's JSON report the item's `version`. Any single-item write accepts `--if-version V` and fails with `conflict` (exit 4), writing nothing, when the item has changed since. The flag is optional. People at the CLI rarely need it, and the TUI always sends the version it displayed. Agents pass it whenever a write depends on what they read, especially `bl ac ID check N`, because criterion numbers shift when one is added or removed. `--evidence TEXT` records the evidence under criterion N (format.md, criteria) and takes exactly one N. A write that names several IDs refuses `--if-version` with `usage` (exit 2).
- **JSON envelope.** `--json` (`-j`) prints one envelope:
  - success: `{"schemaVersion":1,"ok":true,"command":"next","data":{...}}`
  - error: `{"schemaVersion":1,"ok":false,"command":"set","error":{"reason":"conflict","message":"...","details":{}}}`
- **Exit codes.**

  | Code | Meaning | Reasons |
  | --- | --- | --- |
  | 0 | Success | No error |
  | 1 | Internal error | `internal` (also the fallback for unknown reasons) |
  | 2 | Usage error | `usage`, `session-required` |
  | 3 | Not found | `not-found` |
  | 4 | Conflict | `conflict`, `claim-held`, `run-mismatch`, `other-host`, `checkout-removed`, `live`, `no-agent-config` |
  | 5 | Rejected (validation or hook veto) | `rejected`, `invalid-config`, `invalid-value` |
  | 6 | Unavailable (capability, trust, or source) | `unavailable` |
  | 7 | Contention (claimed elsewhere) | `contention` |

  Exit codes and reasons are a public contract with tests. Error `details` is always an object (empty when there is no additional metadata). `-C` failures include `flag` and `path`; unexpected arguments include `argument`. Help remains plain text, even with `--json`.
- **MCP tools:** `next`, `query`, `show`, `create`, `update`, `append`, `criteria`, `action`, `events`, `history`, `claim`, `release`, and `heartbeat`. They share types, validation, claim rules, and result shapes with the CLI, and the server applies one write at a time. Write tools take an optional `version` with the same meaning as `--if-version`. `criteria` takes an optional `evidence` for a single check and an optional `list` (`criteria` or `dod`, like `bl ac --dod`), and `create` an optional `status`, like `bl new --status`. `next` takes `claim`, `start`, and `session`, like `bl next --claim --start`. `events` reads like `bl events --since` without follow, returning at most `limit` events (default 100) and a `cursor` to poll from. Every call that can acquire, renew, or release a claim takes the caller context (§9): `next` with `claim`, `claim`, `release`, `heartbeat`, and an `update` that changes status while coordination is on. That context is a required `session`, plus an optional `run` for a worker under `worklease run`. Without it, the call is refused before anything is written. MCP never reads the caller context from the server's environment or an earlier call, because one `bl mcp` process can outlive the agent loop that started it.

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
| `session.handoff` | `bl handoff` or MCP `append` with `kind: "handoff"` records a session's handoff text (D40) |
| `hook.finished`, `action.finished` | An after hook or an action exits. The event holds the name, `outcome` (`ok`, `failed`, `timeout`, or `refused`), the exit code, the duration, `result`, and on failure a bounded `message` with the refusal reason or stderr tail. |

- **Origin:** `tui`, `cli`, `mcp`, `hook`, `action`, or `sync`.
- **Session:** an event from a `bl` command run with a caller session holds `session`: `id`, `agent`, `agentSession`, `host`, `cwd` (the invoking checkout), and `branch` (D40). TUI per-item sessions record only `id`; `sync` events have none.
- **Cause and depth:** an event produced by a hook or action, directly or through a `bl` call it makes, names its `cause` event, with `depth` one higher. Hooks don't run for events deeper than 3. `BL_HOOK_DEPTH` carries the depth into `bl` subprocesses.
- **Exactly once.** Any `bl` process that loads a newer snapshot diffs it against the cache's previous state and proposes the resulting events. Each event is appended under the log lock and skipped if the log already holds one with the same type, ref, and producing versions. Only the process that appends an event runs its hooks. No daemon is needed. Changes made while nothing runs are emitted by the next `bl` invocation, and `bl events --follow` keeps a watcher running.
- **Baseline root.** The log is keyed by `project.id`, but the previous state is kept per control root. Another root with the same ID, such as a copied `backlog.yaml` or a `root: checkout` worktree, builds its own baseline without emitting events rather than reporting the difference between two directories as item changes, and running `bl` there never discards this root's unemitted changes.
- `bl events [--follow] [--json] [--since ID] [QUERY]` reads and tails the log.

**Hooks** subscribe to event types. This example covers all three: `x` creates a worktree, a chained hook starts a claimed agent inside it, and readiness changes send notifications.

```yaml
actions:
  - name: work
    key: ctrl+x
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
| `{agentSession}`, `{session}`, `{agent}`, `{cwd}`, `{host}` | Agent resume and fork commands only (D40): the recorded session's harness ID, `bl` session ID, agent kind, checkout, and host |

A hook or action whose placeholders cannot expand doesn't run. A hook records a `refused` outcome, and an action is offered as disabled with the reason.

**Environment.** Hooks and actions get `BL_ITEM_ID`, `BL_ITEM_REF`, `BL_SOURCE`, `BL_PROJECT_ID`, `BL_ROOT`, `BL_BRANCH`, `BL_RESOURCE` (when available), `BL_EVENT_ID`, `BL_HOOK_DEPTH`, and `BL_RESULT`. The triggering event arrives on stdin as JSON. The process starts in a minimal environment with credentials removed (D17, D21). An attached agent resume or fork gets the terminal on stdin instead, plus `TERM`, `COLORTERM`, and `LANG`.

**External source adapters** speak the frozen [external adapter protocol v1](external-adapter-protocol.md): initialize and manifest, capabilities, paged list, read, apply with expected version, and cancellation, all with per-frame and per-response bounds. Its [versioned JSON Schema](external-adapter/v1/schema.json) defines the wire shapes. It derives from Worklease's external adapter protocol v1 without the receipt and recovery journal. `bl adapter check EXEC [ARG...]` explicitly executes an adapter for bounded conformance checks; configured committed adapters remain inert until trusted.

## 9. Coordination and Worklease

These features turn on when `coordination.worklease` is configured and `worklease` is on `PATH`. `bl` uses only Worklease's stable `--json` schema-v2 CLI.

**Evidence boundary.** Lifecycle, handle, run, and list responses must expose
public resource identities. The adapter refuses to infer exact membership from
work keys, prefixes, or credentials: unusable/redacted grants are uncertain and
retained, inspections unresolved, and the bulk overlay unknown. Exact pending
replay delegates retained identity/options to Worklease's remote bare-handle
acquire. If the CLI cannot prove or replay a state, recovery refuses rather than
reading private files or inventing request inputs.

- **Resource key:** `bl show --json` reports each item's `resource`, and that is the only key other tools may use for a `bl` item (D20). GitHub and Linear items use `worklease key --provider github|linear` with the provider's own source and item identity. Native and Backlog.md items use `worklease key --provider generic --source <project.id> --item <ID>`. Other remote items use `generic` with the adapter's stable source identity and item key, for example the Jira site and numeric issue ID. `bl` derives keys in-process and tests them against vectors produced by `worklease key`.
- **Work key.** Each acquire passes `--work-key` with the project ID and the item's display ID, for example `github.com/brettinternet/backlog BL-76`, so `worklease` shows which project holds a claim. Remote items keep the project prefix too. It is a label only: `bl` never derives the resource from it or matches claims on it. Control and format characters are dropped and the project ID is shortened first to keep it within Worklease's 1 to 1024 byte limit. A host-local project ID is the control root path, but it only reaches the local authority (see Remote authority).
- **Agent.** When `bl` knows the caller's agent kind (CLI detection or MCP `agent`, D40), each acquire passes it as `--agent`, local or remote, so a claim's `agentId` and claim history `agent` name the agent kind, for example `claude`. A remote installation label already names the host, so the host is never encoded into it. Without a kind, or with one Worklease would refuse (over 128 bytes or with control or format characters), `bl` omits the flag and Worklease records its default, the login user. Together with an undecorated session ID, a Worklease-only reader can tell which agent holds a claim and find its session.
- **Process context.** Worklease keys its contextual handle by the checkout's top level and the session, so a claim made in the primary checkout is invisible from a linked worktree. `bl` therefore runs every `worklease` command from the control root (D5), and an agent can claim in one checkout and finish in another. It removes `WORKLEASE_SESSION_ID`, `WORKLEASE_HANDLE`, and `WORKLEASE_RUN_ID` from the child environment and passes the session explicitly. Authority selection (`WORKLEASE_HOME`, `WORKLEASE_PROFILE`) passes through. Manage `bl` claims only through `bl`, because a direct `worklease` call from a linked worktree selects a different handle.
- **Remote authority.** Hosts coordinate through one remote Worklease authority when each enrolls and selects it (`worklease enroll`, then the default profile, a repository binding, or `WORKLEASE_PROFILE`) and every clone shares the `origin`-derived project ID. Native and Backlog.md keys are portable `coordination:generic:` keys, which a remote authority admits by default. A host-local project ID would give each host a different key that never contends, so before acquiring such a key (claim, `next --claim`, or an active transition) `bl` asks `worklease --json profile show` for the selected authority and refuses anything but the builtin local profile with `unavailable` (exit 6), before writing. An unreadable answer counts as remote. The check runs only for host-local project keys, once per process. Heartbeat and release are never refused, and `bl doctor` reports the same finding.
- **Caller context.** A session plus an optional run ID. The CLI and TUI read the session from `--session`, `BL_SESSION`, or `WORKLEASE_SESSION_ID`; otherwise the CLI is unscoped and the TUI uses a session per item (D39) and the run from `WORKLEASE_RUN_ID`. MCP passes `session` and `run` on each call (§7).
- **Run-managed claims.** A context with a run owns an item's claim only when `worklease runs show RUN --json` reports a live run whose held claim covers the item's resource under the context's session. `bl` never renews or releases that claim, because the run renews it and releases it when the worker exits. A run that cannot be verified refuses the operation with `run-mismatch` (exit 4) before anything is written.
- **Sessions.** Because every claim resolves from the control root, two loops in one project need distinct sessions even in different worktrees. A shared handle fails the second loop with `handle-in-use`, not contention. `next --claim` therefore refuses an unscoped session (exit 2, `session-required`). A session holds at most one claim. Acquiring while its handle holds another active claim fails with `claim-held` (exit 4) and names the held item. It is never reported as contention.
- **Sessions.** `bl show` (CLI and MCP) reads `worklease --json history --resource KEY` for the item's resource and folds each retained claim with the item's events by session ID (D40). Each session lists its agent, host, checkout, branch, first and last seen times, the status categories it saw, its claim, and whether it ended `done`, `released`, or `lapsed`, so a released item still leads back to the sessions that worked it. A claim-only session, such as one from another host, shows its claim fields. A history or event-read failure is reported beside the item, never as a `show` failure. Sessions are only as traceable as the IDs callers pass, so instructions ask for the harness's own session or run ID, unchanged, with no prefix or suffix.
- **Overlay.** `worklease --json list` polls while the TUI is visible and after each claim operation. Expiry rechecks are scheduled locally, because expiry emits no event. Columns show `mine`, `agent@host`, or expired. The `is:claimed` and `claimed:me` queries use the overlay.
- **Claim and release.** `bl claim ID` acquires with no wait. On contention it exits 7 and names the holder. Release carries the claim ID whose exact item resource was verified, using `worklease release --if-claim-id ID` with the explicit contextual session. The provider must compare that ID after selecting and locking the current handle credentials, before any mutation. A replaced handle must fail without releasing its new claim; re-inspecting a mutable handle is not a substitute. Coordination requires a provider build with this guard; a CLI that rejects the flag fails closed, retaining the claim and reporting release failure.
- **Status transitions** follow the same rules from `bl start`, `bl done`, `bl set status=`, the TUI, and MCP `update` (D20):
  - **Entering an active category** acquires, then writes. Contention rejects the transition with exit 7 and writes nothing. A failed write keeps the new claim and reports both outcomes. If the session already holds the item's claim, or a verified run owns it, the acquire is skipped. A transition of several items into an active category is refused, because a session holds one claim.
  - **Entering a closed category** (`done` or `canceled`) writes, then releases the session's claim on that item. A failed release leaves the status written and reports both outcomes. Claims held by others and run-managed claims are never released.
- **`next --claim`.** Inside a verified run, it returns the run's item and never walks; a closed run item fails with `claim-held`. Otherwise it first checks the session's handle with Worklease. If the handle holds or is still acquiring a claim on an open item, that item is returned with `resumed: true`, and a pending acquire is replayed exactly. That is how a loop recovers after a crash, a compaction, or an uncertain acquire. If the handle holds a claim on a closed item, `next` fails with `claim-held`. Otherwise it walks the §6 order from one snapshot and acquires each candidate with no wait. It skips contended items, holds at most one claim, stops on an uncertain acquire, and never loops (Worklease D28). `--start` then applies the active-transition rule. Two loops starting at once get different items, and neither sees an error.
- **Heartbeat.** `bl heartbeat` and MCP `heartbeat` renew the session's claim and report `expiresAt`. A caller renews at least once per half TTL. `bl heartbeat -- COMMAND` keeps renewing at half the remaining lease while a long blocking command, such as a test gate, runs, then exits with its status, so a loop needs no supervisor or second process (BL-95). The TUI renews each of its claims the same way while it runs, a workspace claim through its own project, at half the first remaining lease, from a command off the update loop; quitting lets the claim lapse within one lease. For a run-managed claim, they report `run-managed` and change nothing.
- **Expired session claims.** When the session's ready handle names a claim that the authority reports inactive and past its expiry, and no supervisor run holds it, `bl` archives the handle (`worklease handle archive`, offline, no authority mutation) and treats the session as empty. Claim, `next --claim`, active transitions, and release then proceed with the same session, so an agent never needs a manual Worklease step or a new session after its lease lapsed. Pending or recovery state, an inactive claim that has not expired, and a failed archive stay unresolved and refuse as before.
- **Lapsed claims.** An item in an active category whose claim expired or is missing is never reclaimed, because an item that keeps killing its agent would loop and starve the queue. `bl doctor` lists these items so a person can resume, reset, or close each one.
- Agent launch and worktree setup are ordinary hooks and actions, shipped as documented examples, for example `worklease run --resource {resource}` or `wt switch --create {branch}`.
- Another claim system later implements the same coordination seam. Nothing in hooks, actions, or events names Worklease.

## 10. Backends

| Source | Access | Identity | Edges | Writes | Evidence |
| --- | --- | --- | --- | --- | --- |
| native | Files | `ID` | `blocked_by` and parent fields | All | [format.md](format.md) |
| Backlog.md | Files, no CLI | Task ID, filename slug tolerated | `dependencies`, `parent_task_id` | Status, fields, sections, AC, comments. Milestone files and the label list are read-only. | Worklease §3 Backlog.md 1.52.0 |
| Beads | `bd` 1.3.0 `--json --readonly --sandbox` | Issue ID | Bulk `dependencies`. `blocks` is hard. | `bd update` and comment | [Mapping and limits](beads.md); Worklease §3 Beads |
| [GitHub](github.md) | GraphQL and REST over HTTPS, one serial queue per host and account | `owner/repo#N` | `blockedBy` and sub-issues | Title, description, state, labels, assignees, comments, Projects v2 or label status | Worklease §3 GitHub and Projects |
| Linear | GraphQL over HTTPS | Organization and issue UUID, identifier displayed | Directional `blocks` | Create (with parent and blockers), state, assignee, priority, labels, cycle, comments, archive and unarchive (explicit source `allowWrites`) | Worklease §3 Linear; [setup and sync](linear.md) |
| [Jira Cloud](jira.md) | REST v3 with API-token helper | Canonical site origin + numeric issue ID; movable key retained in fields | Configured directional hard links | Discovered per-issue transitions, confirmed single-assignee replacement, plain-text ADF comments | [BL-40 observations and unknowns](providers/jira.md); partial visible-scope coverage, no proven CAS |
| external | JSON-RPC stdio | Adapter-defined | Adapter-defined | Capability-declared | BL-42 |

"Worklease §3" means [worklease `docs/work-queue-tui-proposal.md` §3 at v1.9.3](https://github.com/brettinternet/worklease/blob/v1.9.3/docs/work-queue-tui-proposal.md), which holds live probe results:

- Linear: page cap of 250, moving-cursor gaps, and relation adds that don't bump `updatedAt`
- GitHub: ETag and quota behavior, and Projects v2 updates that don't bump the issue's `updatedAt`
- Beads: embedded Dolt and bulk dependencies

Port the adapter code from `internal/queue` at that tag.

**OAuth** (D17) is opt-in per user account. Linear uses user authorization with S256 PKCE and a loopback callback; github.com uses device login with separate read-only and write GitHub Apps. Both request read access first and write only after explicit consent. Jira stays on helpers because its 3LO needs a client secret; external adapters own their authentication. [oauth.md](oauth.md) holds the provider evidence; [linear.md](linear.md#oauth-login) and [github.md](github.md#oauth-login) cover setup.

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
| Idle for 1 h, 10k | ≤ 2% of one CPU core, no goroutine or heap growth, input still responsive |
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
| m-0 Foundation | Native format, project/config, the agent CLI, next, check, import, cache, benchmarks, and `bl instructions`. Ends by moving this repository's own backlog onto `bl` (BL-26). Its agents use the native claimed loop in AGENTS.md, with Worklease coordination enabled. |
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
