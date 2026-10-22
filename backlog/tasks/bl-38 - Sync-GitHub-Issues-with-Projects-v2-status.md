---
id: BL-38
title: Sync GitHub Issues with Projects v2 status
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - github
  - source
milestone: m-3
dependencies:
  - BL-35
references:
  - >-
    https://github.com/brettinternet/worklease/blob/v1.9.3/docs/work-queue-tui-proposal.md
modified_files:
  - internal/source/github
  - docs
priority: medium
type: feature
ordinal: 38000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

Port Worklease v1.9.3 `internal/queue/github.go`:

- GraphQL and REST with ETags and secondary-limit backoff.
- `blockedBy` and sub-issue edges.
- Configurable Projects v2 status mapping, plus reconciliation scans for Projects v2 changes that don't bump the issue's `updatedAt` (Worklease §3 evidence).
- Writes: state, labels, assignees, comments, and project status.
- Credentials come from a `gh auth token` helper.
- Claim keys use the Worklease `github` policy (BL-32).
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Fake GitHub server tests cover pagination, ETag 304s, secondary-limit backoff, and a Projects v2 status change caught by reconciliation
- [ ] #2 Writes check the version, report conflicts, and are gated by capability
- [ ] #3 With `BL_PROVIDER_TESTS=1`, a round trip against a test repository passes and cleans up
- [ ] #4 Setup docs name the token scopes needed
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
