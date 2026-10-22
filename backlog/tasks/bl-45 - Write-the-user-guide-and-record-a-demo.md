---
id: BL-45
title: Write the user guide and record a demo
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:46'
labels:
  - docs
milestone: m-4
dependencies:
  - BL-25
  - BL-32
  - BL-10
  - BL-31
  - BL-44
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

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Write the quickstart/guide against merged commands and link the actual docs, covering init/import, TUI, agent loop, config, hooks/actions and Worklease. Distinguish executable examples from owner-only install/auth/publication instructions.
2. Build a deterministic VHS tape using BL-12 fixtures, fixed seed/time/terminal/theme and no real provider data; document required local tools and regeneration command.
3. Run executable shell examples in temp HOME/XDG/project directories with fake external commands where necessary; never run installation/auth or real agent launches as doctests. Render/review the GIF, run task test and verify doc links; keep examples and transcript assertions synchronized.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
