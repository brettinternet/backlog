---
id: BL-19
title: Manage views as tabs and persist UI state
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:45'
labels:
  - tui
  - config
milestone: m-1
dependencies:
  - BL-18
  - BL-17
references:
  - docs/tui.md
  - docs/design.md
modified_files:
  - internal/tui
  - internal/project
priority: high
type: feature
ordinal: 19000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Backlog.md puts each view behind a separate command. In `bl`, views are tabs in one program, and reopening restores exactly where you were (tui.md#state).

## What

- Built-in views, then project and user saved views, as tabs. Each view has its own query, sort, group, layout, columns, and selection.
- `ctrl-s` saves the current view to user config by default, or to project config on request, as a minimal YAML edit.
- Per-project UI state in XDG state: last view, query, sort, group, layout, selection, detail tab, and split width.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Views from built-ins, project config, and user config appear in the documented order, and switching keeps each view's selection
- [ ] #2 `ctrl-s` writes the view with a minimal diff, and it is present after a restart
- [ ] #3 Quitting and reopening restores view, query, sort, group, layout, selection, detail tab, and split width (temp XDG state)
- [ ] #4 A corrupt state file is ignored with a warning
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
1. Build per-view state on BL-16 tabs and BL-17 detail, merging built-in/project/user views in documented order. Preserve selections by stable ref, not row index.
2. Implement ctrl-s as a minimal config edit (user by default), and atomically persist per-project UI state in XDG state off the update loop. Missing items or unavailable layouts need a safe fallback; board/tree arrive later.
3. Verify isolated restart restoration, view switching, minimal config diffs and corrupt-state warnings through task test and a recorded terminal round trip. Update docs/tui.md with merge/order and fallback rules.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
