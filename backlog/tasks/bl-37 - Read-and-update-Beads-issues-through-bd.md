---
id: BL-37
title: Read and update Beads issues through bd
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - beads
  - source
milestone: m-3
dependencies:
  - BL-35
references:
  - >-
    https://github.com/brettinternet/worklease/blob/v1.9.3/docs/work-queue-tui-proposal.md
modified_files:
  - internal/source/beads
  - docs
priority: medium
type: feature
ordinal: 37000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

Port Worklease v1.9.3 `internal/queue/beads.go`:

- `bd` 1.3.0 with `--json --readonly --sandbox`, one bulk call per refresh that includes dependencies (§11 hazard 2).
- A version check.
- `bd update` and comment writes with an expected-version check.
- `blocks` is the only hard edge.

Document the field and edge mapping.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A fake `bd` that counts invocations shows one process per refresh regardless of item count
- [ ] #2 The mapping table is documented, and `blocks` is the only hard edge
- [ ] #3 Writes check the expected version and report conflicts
- [ ] #4 With `BL_PROVIDER_TESTS=1`, a round trip against real `bd` 1.3.0 passes
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
