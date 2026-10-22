---
id: BL-39
title: Sync Linear issues
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - linear
  - source
milestone: m-3
dependencies:
  - BL-35
references:
  - >-
    https://github.com/brettinternet/worklease/blob/v1.9.3/docs/work-queue-tui-proposal.md
modified_files:
  - internal/source/linear
  - docs
priority: medium
type: feature
ordinal: 39000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

Port Worklease v1.9.3 `internal/queue/linear.go`:

- GraphQL with the 250-item page cap.
- Overlapping cursors that tolerate moving-cursor gaps.
- Periodic relation reconciliation, because relation changes don't bump `updatedAt` (Worklease §3 evidence).
- Directional `blocks` edges.
- Writes: state, assignee, priority, labels, and comments.
- Claim keys use the Worklease `linear` policy (BL-32).
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Fake Linear server tests cover the page cap, a moving-cursor gap, and a relation change caught only by reconciliation
- [ ] #2 Writes check the version, report conflicts, and are gated by capability
- [ ] #3 With `BL_PROVIDER_TESTS=1`, a round trip against the Linear test team passes and cleans up
- [ ] #4 Setup docs cover API key helpers and scopes
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
