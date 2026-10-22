---
id: BL-18
title: 'Filter, sort, and group from the list'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:45'
labels:
  - tui
  - query
milestone: m-1
dependencies:
  - BL-16
references:
  - docs/tui.md
  - docs/query.md
modified_files:
  - internal/tui
priority: high
type: feature
ordinal: 18000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Clunky filtering, sorting, and search are a core Backlog.md complaint. Every one should take at most two keys (tui.md principles).

## What

- `/` edits the query with completion of fields and values from the snapshot. Errors show inline, and the last valid result stays on screen. `enter` applies and `esc` restores.
- The `f`, `o`, and `z` menus with which-key popups, and `F` to clear.
- Free-text matches are highlighted.
- All projection work runs through the engine worker.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Model tests cover typing, field and value completion, apply, restore, and an invalid query that shows a positioned error while keeping the last valid result
- [ ] #2 A table test covers every `f`, `o`, and `z` second key in tui.md
- [ ] #3 At 50k items, keys keep being processed while a projection is pending, the latest query wins, and keystroke-to-list latency is recorded against budget
- [ ] #4 Free-text matches are highlighted in golden views
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
1. Connect query editing/completion and filter/sort/group menus to BL-15's latest-wins worker. Keep last valid rows on parse errors, enter commits, and esc restores the prior query.
2. Resolve the draft keymap's duplicate za before coding: reserve za for fold and use zA for assignee grouping, updating docs/tui.md in the same implementation change. Keep one binding source for menus and later help.
3. Implement matching highlights without unsafe provider escape sequences. Verify menu routing, apply/cancel/error transitions and stale projection rejection with model tests; run task test and opt-in 50k keystroke-to-list measurements.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
