---
id: BL-25
title: Verify the TUI performance and idle-soak budgets
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - perf
  - tui
milestone: m-1
dependencies:
  - BL-17
  - BL-18
  - BL-19
  - BL-20
  - BL-21
  - BL-22
  - BL-23
  - BL-24
references:
  - docs/design.md
  - docs/benchmarks.md
modified_files:
  - docs/benchmarks.md
  - internal/tui
priority: high
type: task
ordinal: 25000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

The budgets in §11 are acceptance criteria (D26). This task proves the finished TUI meets them, including the Backlog.md failure modes: idle freezes and held-key lag.

## What

Use the BL-14 PTY harness and BL-12 fixtures to measure every TUI row in §11 on the reference host, including a one-hour idle soak. Record the results in docs/benchmarks.md. Fix misses, or file a follow-up task for each one with its measurement.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 docs/benchmarks.md records a reference-host p95 and the reproduce command for every TUI budget row in §11
- [ ] #2 After a one-hour idle soak, CPU is about 0 %, goroutines and heap are flat, and input still responds (PTY harness)
- [ ] #3 After holding `j` at 40 Hz for 5 s at 10k items, the selection stops within one frame of release
- [ ] #4 Every miss is fixed or has a linked follow-up task with its measurement
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
