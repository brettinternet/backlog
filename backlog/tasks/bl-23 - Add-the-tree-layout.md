---
id: BL-23
title: Add the tree layout
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - tui
milestone: m-1
dependencies:
  - BL-20
references:
  - docs/tui.md
modified_files:
  - internal/tui
priority: medium
type: feature
ordinal: 23000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

The tui.md tree layout:

- Parent and child hierarchy with `▣ a/b` progress.
- `h` collapses or moves to the parent; `l` expands or moves to the first child.
- `z` folds.
- A filtered tree keeps faint ancestors of matches for context.
- Cycles reported by `bl check` never hang the render.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Model tests cover the `h` and `l` semantics at every depth
- [ ] #2 A filtered tree shows each match under its ancestors
- [ ] #3 Progress counts nested children correctly
- [ ] #4 Render cost stays bounded by visible rows at 50k items with deep hierarchies; the result is recorded
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
