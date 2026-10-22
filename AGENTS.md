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

- `backlog/` (Backlog.md) is this repository's task queue until BL-26 migrates it to `bl`'s native format. Read and change tasks only through the `backlog` CLI. Never edit task Markdown directly.
- Backlog state lives in the primary checkout. From a linked worktree, run `task backlog:cli -- <args>`, for example `task backlog:cli -- task view BL-12 --plain`, instead of calling `backlog` directly.
- Pick and claim work in one step, before changing any status. Use one stable session for your whole loop:

  ```sh
  task backlog:claim-next -- SESSION
  ```

  It walks the ready To Do tasks in order, skips tasks labelled `blocked`, `deferred`, `escalated`, or `human`, skips tasks another session holds, and claims the first free one. If your session already holds a claim, it stops and tells you so. `task backlog:next` only shows the first ready task, without claiming. To claim a specific task instead, run `task backlog:claim -- acquire --provider generic --source github.com/brettinternet/backlog --item BL-12 -s SESSION`. Renew long work with `task backlog:claim -- heartbeat -s SESSION` at least every 7 minutes, and release with `task backlog:claim -- release -s SESSION` when you finish. The target runs Worklease from the primary checkout. A plain `worklease` call from a linked worktree can't see the claim, because Worklease keys claims by checkout. `bl` uses the same key (D20). This interim protocol lasts until BL-33 ships `bl next --claim --start`.
- Read the guides before acting: `backlog instructions task-execution`, then `backlog instructions task-finalization`. Keep status, assignee, plan, notes, and acceptance checks in the task.
- When you finish, record one `AC#N:` evidence line per criterion in Implementation Notes. Each line names the command you ran and its result.
- Commit backlog changes separately in the primary checkout with `docs(backlog): …`. Other sessions change `backlog/` at the same time, so commit only your own task files, by path: `git add FILES` then `git commit -m MESSAGE -- FILES`. A commit limited to paths leaves out whatever another session has staged.
- A task labelled `human` needs the owner: credentials, publishing, pushing, or other repositories. Don't start one without explicit instruction.
- Never skip, delete, or weaken a test to make the gate pass. Stop and escalate instead.

## Git and GitHub

- Agent-created branches MUST be Worktrunk worktrees under `.worktrees/`. Do not create branches in the primary checkout. Create one with `mise exec worktrunk -- wt switch --create <branch> --base main --no-cd --format=json`; `.config/wt.toml` prepares the checkout.
- When a merged worktree branch is no longer needed, remove the checkout and its eligible branch with `mise exec worktrunk -- wt remove <branch> --foreground --format=json`.
- Use Conventional Commits (`docs/development.md#commit-messages`).
- Use `gh` for GitHub operations. Do not construct raw API calls or open GitHub URLs in a browser.
- Do not push or open a pull request without explicit instruction.
