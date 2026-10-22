---
id: BL-25
title: Verify the TUI performance and idle-soak budgets
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:45'
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

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Use the merged BL-12 fixtures, BL-14 PTY harness and complete BL-16–24 TUI. Record the commit, host, terminal size, seed, sample count and warm/cold conditions.
2. Measure every design.md §11 TUI/RSS row, held-key release and a full one-hour idle soak with input after the soak. Run long measurements as a managed job, not a sleep/poll loop in bash.
3. Write p95/results and reproduction commands to docs/benchmarks.md. Fix failures in scope; if a miss requires separate work, record its measurement and obtain approval for the follow-up without checking the unmet criterion. Run task test, task race and task ci after fixes; never convert absolute budgets into default CI timing assertions.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
