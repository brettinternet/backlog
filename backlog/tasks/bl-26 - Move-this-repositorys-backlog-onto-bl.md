---
id: BL-26
title: Move this repository's backlog onto bl
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:45'
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

Dogfooding starts as soon as the agent CLI works. After this task, every later task in this repository is tracked and run with `bl`.

## What

- Rehearse a lossless import in isolation, then coordinate a single authoritative provider cutover in the primary checkout with no concurrent task writers. Prepare code/config changes in a Worktrunk worktree; refresh task state immediately before cutover and retain a recoverable old-format copy until validation and a native task loop pass. See the Implementation Plan for the primary-checkout handoff and loop restart.
- `.config/backlog.yaml`: statuses, `ready.excludeLabels` (`blocked`, `deferred`, `escalated`, `human`), `defaults.dod` from the old `definition_of_done`, and `coordination.worklease`.
- `task backlog:cli -- ARGS` builds `bl` from the primary checkout (the last merged `main`) into the Git common directory and runs it, so agents in worktrees never manage the backlog with an unmerged build. `task backlog:next` becomes `bl next`.
- Rewrite the Backlog section of AGENTS.md for the `bl` loop and `bl instructions`. Remove Backlog.md from mise.toml and docs.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 `bl check` accepts the migrated backlog, and a comparison against the pre-migration `backlog task list --json` shows matching tasks, statuses, dependencies, and criterion states
- [ ] #2 `task backlog:next` returns a ready task, and `bl next --explain` output is recorded in Implementation Notes
- [ ] #3 AGENTS.md, docs, Taskfiles, and mise.toml no longer depend on Backlog.md, and `task ci` passes
- [ ] #4 One real task goes through next, claim (`worklease acquire` until BL-32 ships `bl claim`), start, note, ac check, and done with `bl`, and the transcript is recorded
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
1. Treat this as a coordinated provider cutover, not an ordinary task-file edit. First confirm no other task writers/leases are active; snapshot all tasks through the Backlog.md CLI, including this task's current plan/evidence and archived data. Rehearse import/check/full comparison in a temporary repository using the merged foundation binary.
2. Prepare the native config, wrapper, instructions and dependency removal in the required implementation worktree. The wrapper must build only primary main into a version-keyed Git-common-directory path; a linked worktree must never execute its unmerged binary to mutate shared backlog state.
3. At cutover, refresh the CLI export and repeat comparison before replacing the provider in the primary checkout. Keep a recoverable old-format copy until checks and the native task loop pass. Use approved Worktrunk delivery/owner handoff for primary changes; do not create a branch in primary or discard concurrent task updates.
4. Use BL-26 itself for the real loop: keep the same claim, record next eligibility before start, then native start/note/ac/done only after all implementation and task ci checks pass. Update this task's native evidence, AGENTS.md, Taskfiles and tool pins together; demonstrate task backlog:next from a linked worktree. No dependency on bl claim: direct worklease remains valid until BL-32.
5. Stop after successful cutover and reload repository instructions/provider tools before selecting another task. Record native IDs, authoritative path and exact continuation commands so a supervisor still using Backlog.md does not write stale task files.
6. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
