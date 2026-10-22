---
id: BL-46
title: Move the owner's other repositories onto bl
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
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
