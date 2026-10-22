---
id: BL-24
title: >-
  Add go-to, the command palette, help, which-key popups, keymap overrides, and
  mouse
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
  - docs/tui.md
priority: medium
type: feature
ordinal: 24000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

- `ctrl-p` go-to: fuzzy ID and title search over every item, including done and archived ones, through the cache.
- `:` opens a palette of every action with its binding and any disabled reason.
- `?` opens searchable help generated from the keymap.
- Which-key popups for prefix keys.
- User config `keys:` overrides, with conflicts reported at startup.
- Mouse support as tui.md describes.

The default keymap must equal the tables in docs/tui.md, the documented source of truth.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Go-to finds items across 50k items, including archived ones, and per-keystroke latency is recorded
- [ ] #2 The palette lists every keymap action, and running one from the palette equals pressing its key
- [ ] #3 A test fails when the default keymap and the docs/tui.md tables disagree
- [ ] #4 `keys:` overrides apply, and conflicting bindings are reported
- [ ] #5 Model tests cover click select, double-click open, wheel scroll, tab and header clicks, and divider drag
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
