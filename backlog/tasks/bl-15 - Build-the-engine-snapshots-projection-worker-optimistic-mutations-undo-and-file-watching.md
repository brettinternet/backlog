---
id: BL-15
title: >-
  Build the engine: snapshots, projection worker, optimistic mutations, undo,
  and file watching
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - engine
  - perf
milestone: m-1
dependencies:
  - BL-5
  - BL-6
  - BL-8
  - BL-11
references:
  - docs/design.md
modified_files:
  - internal/engine
  - internal/source/native
  - go.mod
  - go.sum
priority: high
type: feature
ordinal: 15000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

The TUI must never wait (D14, D15). That needs immutable snapshots, projections off the UI loop, optimistic edits, and change detection that scales on macOS (§11 hazards 1, 4, and 6).

## What

`internal/engine` store:

- Immutable snapshots with per-item versions and structural sharing.
- A per-source sync worker. Only the native source exists now, but nothing may assume a single source.
- A projection worker that turns query, sort, and group into prepared rows. Latest request wins, and older ones are cancelled.
- An async mutation pipeline on top of BL-8. Edits apply optimistically and stay pending until the source confirms; failures revert, and conflicts show both versions. Session undo and redo replay inverse mutations.
- Native Watch: watch directory entries only, run a low-rate stat sweep for in-place edits, and fall back to polling on `EMFILE`.
- Subscriptions through latest-value mailboxes, so a slow consumer never blocks the engine.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A filter projection over 50k items runs off the caller goroutine, newer requests cancel older ones, and the benchmark result is recorded against the 80 ms budget
- [ ] #2 With a fake source: an edit shows as pending in the next snapshot, a failure reverts with the error recorded, and a conflict exposes both versions with no automatic retry
- [ ] #3 Undo and redo restore the prior file bytes apart from `updated`
- [ ] #4 At 10k items, an external edit, create, delete, or rename appears in a snapshot within 150 ms; open file descriptors stay bounded on macOS; an injected `EMFILE` switches to polling
- [ ] #5 A blocked subscriber does not stall the engine, and `task race` passes
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
