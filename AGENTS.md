@AGENTS.local.md

# Agents

## Product

- `docs/design.md` is the plan of record. Before changing behavior, read its decision register. If a change alters a decision, update the register in the same commit.
- `docs/format.md`, `docs/query.md`, and `docs/tui.md` specify the native format, the query language, and the TUI. When behavior changes, update the matching spec in the same commit.
- The budgets in design.md §11 are acceptance criteria. Never put I/O or work proportional to backlog size on the Bubble Tea update loop (D14).
- Port provider adapters from Worklease `v1.9.3` (`internal/queue`) where design.md §10 says so. Read them from that tag, for example with `git -C ../worklease show v1.9.3:internal/queue/linear.go`.

## Tooling

- Install the toolchain and hooks with `task init`.
- Use project `task` targets instead of reconstructing commands. When a project-managed tool isn't on `PATH`, use `mise exec <tool> -- <command>`.
- Use the smallest verification loop that covers the change, and follow the test rules in `docs/development.md#tests`.
- Before committing, stage the intended files and run `task check:staged`. Run `task ci` before marking a backlog task Done.

## Backlog

- `backlog/` is this repository's native `bl` task queue. Read and change tasks only through `task backlog:cli -- <args>` (for example `show BL-12 --json`). Never edit task Markdown directly.
- The wrapper runs from the primary checkout and builds only committed primary `main`, cached by commit in the Git common directory. Worktrees must not use an unmerged build to manage the shared backlog. Primary must remain on `main`.
- Pick and claim work in one step, before changing any status. Use one stable session for your whole loop:

  ```sh
  task backlog:cli -- next --claim --start --session SESSION --json
  ```

  This is `bl next --claim --start`: one ranked snapshot, skip confirmed contention, stop on an uncertain acquire. A held open item resumes without acquiring; a pending acquire replays its exact retained request. Keep the same session on uncertainty; never bypass recovery with a new session. `task backlog:next` and plain `bl next` are read-only. Use `task backlog:cli -- claim BL-12 --session SESSION` only to claim a specific item. Renew with `task backlog:cli -- heartbeat --session SESSION` (`bl heartbeat`) at least every 7 minutes and before half the TTL. Run `task ci` and any other command that may block that long as `task backlog:cli -- heartbeat --session SESSION -- task -d "$PWD" ci`, which renews until the command exits and returns its status; `-d` keeps the checkout, since the wrapper runs from the primary. Finish with `task backlog:cli -- done ID --session SESSION` (`bl done`), which writes then releases before returning. If stopping incomplete, persist progress then use `task backlog:cli -- release ID --session SESSION`. A verified run owns renewal and release; never manage its claim yourself. All Worklease calls resolve from the primary control root through `bl`, including from worktrees.
- Read `task backlog:cli -- instructions` and `task backlog:cli -- instructions workflow` before acting. After claimed selection, use `note ID TEXT`, `ac ID check N --evidence TEXT`, and `done ID --session SESSION`. Pass the same session for status writes; `start` and all active transitions are claim-gated. Keep plan, notes, summary, and acceptance checks in the task; use `edit ID --section plan --text TEXT|-` (or `summary`) for section updates.
- Leave assignees unchanged: the claim records which agent is working (D42).
- Record each criterion's command and observed result with `ac ID check N --evidence TEXT`, not `AC#N:` note lines. Read `show ID --json` first and pass `--if-version VERSION` when acting on criterion indices. Run `task ci` and tick each DoD item with `ac ID check N --dod --evidence TEXT` before `done`; inspect both status and release outcomes before continuing the loop.
- **Local provider override:** inherited `backlog-md` status-as-claim guidance, Backlog.md CLI/MCP writes, Worklease `backlog-md:` keys, and queue/launcher paths that derive them are disabled in this repository. This overrides generic skills referring to `@backlog/`. Only the native wrapper and claimed loop above may launch work; incompatible supervisors must stay stopped. Do not restart a legacy continuation after cutover. Shared-skill migration is separate BL-46 work.
- Commit backlog changes separately in the primary checkout with `docs(backlog): …`. Other sessions change `backlog/` at the same time, so commit only your own task files, by path: `git add FILES` then `git commit -m MESSAGE -- FILES`. A commit limited to paths leaves out whatever another session has staged. If Git reports that `index.lock` exists, another session is using the index: retry after a few seconds, and never delete the lock.
- A task labelled `human` needs the owner: credentials, publishing, pushing, or other repositories. Don't start one without explicit instruction.
- Never skip, delete, or weaken a test to make the gate pass. Stop and escalate instead.

## Git and GitHub

- Agent-created branches MUST be Worktrunk worktrees under `.worktrees/`. Do not create branches in the primary checkout. Create one with `mise exec worktrunk -- wt switch --create <branch> --base main --no-cd --format=json`; `.config/wt.toml` prepares the checkout.
- When a merged worktree branch is no longer needed, remove the checkout and its eligible branch with `mise exec worktrunk -- wt remove <branch> --foreground --format=json`.
- Use Conventional Commits (`docs/development.md#commit-messages`).
- Use `gh` for GitHub operations. Do not construct raw API calls or open GitHub URLs in a browser.
- Do not push or open a pull request without explicit instruction.
