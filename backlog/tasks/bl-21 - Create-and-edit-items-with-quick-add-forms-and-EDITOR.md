---
id: BL-21
title: 'Create and edit items with quick-add, forms, and $EDITOR'
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
priority: high
type: feature
ordinal: 21000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Fast capture and comfortable long-form editing close the remaining gaps in the editing experience (tui.md#quick-add, tui.md#forms-and-editors).

## What

- `n` and `N` quick-add, with tokens shown as chips. `tab` expands the line into the full form. In the board layout, a new item takes its column's status.
- The full create and edit form for every field the source supports.
- `e` and `E` suspend the TUI for `$EDITOR`. Saving is version-checked. A conflict offers keep yours, keep theirs, or reopen with both. Invalid frontmatter offers reopen or discard.
- `c` adds a comment and `i` appends a note.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Quick-add in the TUI and `bl new` produce identical items from the same input (one shared-parser test through both paths)
- [ ] #2 Form tests create and edit with every field and show validation errors inline
- [ ] #3 With a fake editor, a save applies; a concurrent change offers each conflict choice and each works; invalid frontmatter offers reopen and discard
- [ ] #4 A PTY test shows the terminal restored and the TUI responsive after the editor exits
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
