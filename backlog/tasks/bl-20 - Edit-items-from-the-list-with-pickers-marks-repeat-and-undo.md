---
id: BL-20
title: 'Edit items from the list with pickers, marks, repeat, and undo'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - tui
milestone: m-1
dependencies:
  - BL-17
  - BL-18
references:
  - docs/tui.md
  - docs/design.md
modified_files:
  - internal/tui
priority: high
type: feature
ordinal: 20000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Editing in Backlog.md means rewriting the document. In `bl`, every common edit is at most two keys from the list (design.md §1).

## What

- The tui.md item-edit keys: `s`, `<`, `>`, `p`, `a`, `A`, `l`, `m`, `b`, `P`, `t`, criteria toggles, `S`, `D`, `dd`, `.`, `u`, `ctrl-r`, `y`, and `O`. `n`, `N`, `e`, `E`, `c`, and `i` belong to BL-21; `C` and the claim step of `S` belong to BL-32.
- Marks with `space`, `V`, and `ctrl-a`. Edits apply to every marked item.
- Fuzzy, keyboard-first pickers that preselect current values.
- Pending and failed glyphs, and failure detail.
- Actions the source can't perform are hidden, and the palette shows why (D10).
- Yanking uses OSC 52.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A table test covers each in-scope edit key, producing the expected mutation against a fake source and applying to marked items when marks exist
- [ ] #2 `.` repeats the last edit; `u` and `ctrl-r` restore native file bytes in an integration test
- [ ] #3 Pending and failed glyphs render; a failed edit shows its reason and a conflict shows both versions
- [ ] #4 Keys for unsupported capabilities do nothing except show the disabled reason
- [ ] #5 `yb` emits OSC 52 with a branch name like `bl-12-debounce-detail`
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
