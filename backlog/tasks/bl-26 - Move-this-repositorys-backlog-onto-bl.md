---
id: BL-26
title: Move this repository's backlog onto bl
status: In Progress
assignee:
  - '@pi'
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 15:39'
labels:
  - tooling
  - import
milestone: m-0
dependencies:
  - BL-7
  - BL-8
  - BL-9
  - BL-10
  - BL-13
  - BL-12
references:
  - AGENTS.md
  - docs/design.md
  - docs/import.md
modified_files:
  - backlog
  - .config/backlog.yaml
  - .taskfiles/backlog.yaml
  - AGENTS.md
  - mise.toml
  - docs
priority: high
type: task
ordinal: 26000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Dogfooding starts as soon as the agent CLI works. After this task, every later task in this repository is tracked and run with `bl`. Until BL-33 ships the claimed loop, agents claim with the interim `task backlog:claim` protocol in AGENTS.md, which uses the key `bl` will use (D20).

## What

- Rehearse a lossless import in isolation, then coordinate a single authoritative provider cutover in the primary checkout with no concurrent task writers. Prepare code/config changes in a Worktrunk worktree; refresh task state immediately before cutover and retain a recoverable old-format copy until validation and a native task loop pass. See the Implementation Plan for the primary-checkout handoff and loop restart.
- `.config/backlog.yaml`: statuses, `ready.excludeLabels` (`blocked`, `deferred`, `escalated`, `human`), `defaults.dod` from the old `definition_of_done`, and `coordination.worklease`.
- `task backlog:cli -- ARGS` builds `bl` from the primary checkout (the last merged `main`) into the Git common directory and runs it, so agents in worktrees never manage the backlog with an unmerged build. `task backlog:next` becomes `bl next`, and `scripts/backlog-claim-next` reads the ready order from `bl next --json` instead of Backlog.md. Execution claims with `task backlog:claim -- acquire` before `bl start`, never with a status-only claim. BL-33 replaces this with `bl next --claim --start`.
- Rewrite the Backlog section of AGENTS.md for the `bl` loop and `bl instructions`. Evidence moves from `AC#N:` lines in Notes to `bl ac ID check N --evidence TEXT` (D28), in AGENTS.md and `defaults.dod`. Remove Backlog.md from mise.toml and docs.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 `bl check` accepts the migrated backlog, and a comparison against the pre-migration `backlog task list --json` shows matching tasks, statuses, dependencies, and criterion states
- [ ] #2 `task backlog:next` returns a ready task, and `bl next --explain` output is recorded in Implementation Notes
- [ ] #3 AGENTS.md, docs, Taskfiles, and mise.toml no longer depend on Backlog.md, and `task ci` passes
- [ ] #4 One independently ready real task goes through `bl next`, `task backlog:claim -- acquire` with its own stable session distinct from BL-26, `bl start`, note, ac check, `bl done`, and `task backlog:claim -- release`, and the recorded transcript shows the claim before the status change
- [ ] #5 Before cutover resumes any agents, AGENTS.md and the repository's launchers route native work through the interim claim protocol; two distinct-session clients against a migrated fixture claim different items, and inherited skill or launcher paths that set status as a claim or derive `backlog-md:` keys are explicitly disabled
- [ ] #6 After cutover, two distinct sessions running `task backlog:claim-next` claim different native items, and AGENTS.md and `defaults.dod` require `--evidence` instead of `AC#N:` lines
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 task ci passes on the final commit
- [ ] #2 Every checked acceptance criterion has an AC#N evidence line in Implementation Notes naming the command and its result
- [ ] #3 Substantial changes had an independent verifier pass that returned PASS for every acceptance criterion
- [ ] #4 The diff stays within the task's modified-file list, or Implementation Notes justify the deviation
- [ ] #5 No test was deleted, skipped, or weakened
- [ ] #6 User-visible behavior changes are reflected in README.md or docs/ in the same change
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Cut over once the m-0 agent CLI is delivered on primary main. Leave `coordination.worklease` off until BL-32; agents claim with the AGENTS.md interim protocol (`task backlog:claim`, generic key, run from the primary checkout), which produces the key bl derives later. BL-33 replaces that protocol with the claimed loop.
2. Quiesce every other backlog writer/agent loop; retain only the migration task's known claim and record how it is handed off without a second acquisition. Export complete Backlog.md state through the CLI and rehearse import/check/field comparisons in a temporary repository. Do not lose this task's plan, checks or evidence.
3. Prepare config/wrappers/AGENTS.md in the implementation worktree, then coordinate delivery/cutover in primary using a refreshed export. The wrapper builds only merged primary main into a version-keyed Git-common-dir path. Preserve a recoverable old-format copy and reject concurrent task writes before replacement.
4. At the same cutover, make repository-local instructions authoritative for native provider routing: claim with `task backlog:claim -- acquire` (one distinct stable session per loop) before `bl start`, renew with `heartbeat`, release after `bl done`; never treat status as a lock or derive a `backlog-md:` key. Audit the actual launchers and inherited skills; disable incompatible paths locally until fixed. Shared skill edits remain owner-approved BL-46 work, not an implicit prerequisite or permission here. If a launcher cannot obey the local contract, keep it stopped.
5. Reload native instructions before further task selection. Keep BL-26 In Progress and validate one independently ready real task through the native loop with the interim claim, using a separate session/claim/worktree from BL-26 so the one-claim-per-session rule holds. Complete it normally, recording its transcript, claim before start and release after done; do not reset BL-26 to make next select it.
6. Exercise two distinct-session interim claimers against an isolated migrated fixture to prove distinct claims and one resource policy, and verify the wrapper from a linked worktree. Record exact continuation commands and local client exclusions, then finish BL-26 only after lossless comparison, real loop and task ci pass. Stop legacy-provider continuations; do not leave an old supervisor writing stale files.
7. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and deliver the implementation before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Owner confirmed all other backlog writers stopped for cutover. Rehearsal import/check completed in isolated Git repository. Native wrapper builds committed primary main only; regression test proves dirty source exclusion and SHA-keyed refresh. Additional modified paths scripts/backlog-cli, scripts/backlog-claim-next, scripts/test_backlog_cli.py and Taskfile.dist.yaml are required launcher and build-boundary verification surfaces. Retain migration claim/session 01a1643e-8844-729e-88c1-f649c0f12596 without reacquisition. Legacy skills with status-as-claim/backlog-md keys disabled locally before any native client resumes.
<!-- SECTION:NOTES:END -->
