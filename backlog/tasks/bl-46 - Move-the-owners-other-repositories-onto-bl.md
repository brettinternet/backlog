---
id: BL-46
title: Move the owner's other repositories onto bl
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:46'
labels:
  - import
  - human
milestone: m-4
dependencies:
  - BL-26
  - BL-32
  - BL-44
references:
  - docs/import.md
modified_files:
  - docs
priority: low
type: task
ordinal: 46000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

The goal is to replace Backlog.md across the owner's projects (design.md §1).

## Needs the owner

This edits other repositories. The owner chooses the order and approves each migration.

## What

For each chosen Backlog.md repository (hum, worklease, tell, noon, and others):

- Run `bl import backlog-md`.
- Port the repository's backlog Taskfile targets and AGENTS.md workflow to `bl`.
- Commit separately in that repository.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Each migrated repository passes `bl check`, and a pre- and post-migration comparison shows nothing lost
- [ ] #2 Each repository's agent workflow runs one full task loop with `bl`
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
1. Wait for the owner to name repositories, order, per-repository approval and the required Git delivery policy. This task does not authorize touching arbitrary sibling repositories.
2. For each approved repository, inspect its own AGENTS.md and current task writers/claims, export the full provider state and rehearse import/check/comparison in a temporary location before cutover.
3. Reuse BL-26's safe provider handoff and wrappers, retain a recoverable old-format copy, verify all fields/checks/dependencies and one real task loop, then commit separately in that repository. Record commit IDs, commands and retained backups here; do not push without explicit permission.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
