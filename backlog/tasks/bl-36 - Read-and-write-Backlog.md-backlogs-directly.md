---
id: BL-36
title: Read and write Backlog.md backlogs directly
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - backlog-md
  - source
milestone: m-3
dependencies:
  - BL-10
  - BL-15
references:
  - docs/design.md
  - docs/import.md
modified_files:
  - internal/source/backlogmd
  - internal/project
  - docs
priority: medium
type: feature
ordinal: 36000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Some repositories can't move off Backlog.md, such as team repositories. `bl` should still be the better TUI for them (D4).

## What

`internal/source/backlogmd` reads and writes Backlog.md 1.52 files without its CLI, reusing the BL-10 mapping:

- Status, fields, sections, criteria, and comments, with minimal diffs and Backlog.md's own conventions (HTML markers, `updated_date`, quoting).
- Create follows Backlog.md's ID and filename rules (next number, configured zero padding, `<id> - <Title>.md`).
- A repository with only Backlog.md config opens read-only until the source is configured.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 With `BL_PROVIDER_TESTS=1`, every field `bl` reads from a fixture backlog equals Backlog.md 1.52's `task view --plain` output
- [ ] #2 Edits and creates made by `bl` read back identically through Backlog.md 1.52, and golden diffs show minimal changes
- [ ] #3 Capabilities are documented, and unsupported operations are not offered
- [ ] #4 A repository with only Backlog.md config opens read-only with a prompt to configure it
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
