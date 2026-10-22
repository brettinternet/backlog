---
id: BL-43
title: Browse several projects as a workspace
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - tui
  - config
milestone: m-3
dependencies:
  - BL-19
  - BL-35
references:
  - docs/design.md
  - docs/tui.md
modified_files:
  - internal/project
  - internal/engine
  - internal/tui
  - internal/cli
priority: medium
type: feature
ordinal: 43000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

User config `workspaces` (§5):

- Each project loads with its own config, statuses, sources, and trust.
- `W` toggles between project and workspace scope, keeping the selection when possible.
- Outside a repository, `bl` opens the default workspace.
- A source column appears in workspace scope.
- `source:ID` references resolve across the workspace.
- `bl ls` and `bl next` accept `--workspace NAME`.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A workspace of three projects shows each item with its project and source, using that project's statuses and trust
- [ ] #2 `W` toggles scope and keeps the selection when the item is visible in both
- [ ] #3 A cross-project `blocked_by` is known in workspace scope and `unknown` in project scope
- [ ] #4 First paint for a five-project, 50k-item workspace is measured and recorded
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
