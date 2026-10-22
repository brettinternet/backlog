---
id: BL-45
title: Write the user guide and record a demo
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - docs
milestone: m-4
dependencies:
  - BL-25
  - BL-32
references:
  - README.md
  - docs/design.md
modified_files:
  - README.md
  - docs/guide.md
  - docs/demo.tape
  - docs/demo.gif
  - internal/doctest
priority: medium
type: docs
ordinal: 45000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

- A README quickstart.
- `docs/guide.md` covering install, init, import, a TUI tour, the agent loop, configuration, hooks and actions, and Worklease.
- A VHS tape and GIF of the TUI built from the BL-12 fixtures, like Worklease's `docs/tui-demo.tape`.
- A doc test that runs every command shown in the docs so they can't drift.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A doc test runs every shell example in README.md and docs/guide.md against a temp project
- [ ] #2 The VHS tape renders deterministically from fixtures
- [ ] #3 README.md links every doc
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
