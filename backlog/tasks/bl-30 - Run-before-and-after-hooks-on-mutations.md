---
id: BL-30
title: Run before and after hooks on mutations
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - hooks
  - security
milestone: m-2
dependencies:
  - BL-6
  - BL-28
  - BL-29
references:
  - docs/design.md
modified_files:
  - internal/hooks
  - internal/engine
  - internal/project
priority: medium
type: feature
ordinal: 30000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

The §8 hooks: `on`, an `if` query (including `changed:`), `phase`, `run` argv, and `timeout`.

- Before hooks run synchronously with the proposed event on stdin. A non-zero exit or a timeout vetoes the mutation, showing the first stderr line.
- After hooks never block. In the CLI they run in a detached `bl` runner process that logs the result, so the command returns at once. In the TUI they run with bounded concurrency and failures show in the status line.
- No shell, ever (D21). Only the documented placeholders and `BL_*` variables. A minimal environment with credentials removed. `BL_HOOK_DEPTH` is capped at 3.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A vetoing before hook makes the command exit 5 with the hook's first stderr line and write nothing; a timeout also vetoes
- [ ] #2 After hooks add no measurable delay to CLI exit beyond spawning, and their results are logged
- [ ] #3 A canary test proves hooks receive only the documented variables and no credential variables
- [ ] #4 A hook that calls `bl` increments the depth, and depth 4 is refused
- [ ] #5 Unknown placeholders are config errors, and argv containing shell metacharacters is passed literally
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
