---
id: BL-22
title: Add the board layout
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
ordinal: 22000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

The tui.md board layout:

- One column per status, or per group key, in workflow order.
- `h`/`l` move between columns and `j`/`k` within one. `<`/`>` move a card to the previous or next status, and the selection stays on the card.
- Each column shows its count and scrolls on its own. Rendering is O(visible cards).
- Horizontal scrolling on narrow terminals.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Model tests cover column navigation, empty columns, and card moves that update status optimistically and keep the selection on the card
- [ ] #2 Boards grouped by a non-status key navigate correctly, and `<`/`>` still change status
- [ ] #3 A benchmark at 50k items shows render cost bounded by visible cards; the result is recorded
- [ ] #4 Golden views at 80 and 160 columns
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
