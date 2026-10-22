---
id: BL-26
title: Move this repository's backlog onto bl
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 00:51'
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

- In the primary checkout, move the Backlog.md directory aside (`git mv backlog backlog-md`), run `bl import backlog-md backlog-md --to backlog`, then remove `backlog-md` with `trash`, all in one commit.
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
