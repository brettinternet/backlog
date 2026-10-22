---
id: BL-17
title: Render the detail pane asynchronously
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:45'
labels:
  - tui
  - perf
milestone: m-1
dependencies:
  - BL-16
references:
  - docs/tui.md
  - docs/design.md
modified_files:
  - internal/tui
priority: high
type: feature
ordinal: 17000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Synchronous Markdown rendering on navigation is a known cause of held-key lag (§11 hazards 3 and 5).

## What

- The detail pane from tui.md: beside the list at 120 columns or more, replacing it below that.
- Tabs: Overview (with readiness and the reason), Description, Criteria, Plan/Notes, Comments, and Relations (with live status).
- Header fields update synchronously. Bodies render asynchronously once the selection settles, cached by item version, width, and theme.
- Full detail, including comments, loads lazily through Read.
- Large bodies are capped and get a "show more" control.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Holding `j` through 10k rows starts no Markdown render until the selection settles (counter assertion), while header fields update every frame
- [ ] #2 Returning to an item hits the render cache; a version or width change invalidates it
- [ ] #3 The Relations tab shows blocker and child status and updates after an external change
- [ ] #4 Input is handled while a 1 MB body renders, and the capped view offers show more
- [ ] #5 Golden views cover every tab at 120 and 80 columns
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
1. Implement detail focus/open/close and responsive pane geometry with tabs backed by selected ref/version. Update cheap header fields immediately; queue full Read and Markdown rendering only after selection settles.
2. Use cancellation/generation checks and a version/width/theme cache so late results cannot paint the wrong item. Bound huge bodies and load comments lazily; relations read live snapshot state.
3. Verify no render during held navigation, cache reuse/invalidation, live relations and responsiveness during large-body work with counters and model tests. Capture all tabs at 80/120 columns and run task test plus focused PTY checks.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
