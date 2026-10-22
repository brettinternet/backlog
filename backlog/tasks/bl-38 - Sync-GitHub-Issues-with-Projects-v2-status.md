---
id: BL-38
title: Sync GitHub Issues with Projects v2 status
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:46'
labels:
  - github
  - source
  - human
milestone: m-3
dependencies:
  - BL-35
  - BL-32
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

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Before starting, satisfy the owner-access gate in Implementation Notes. Port Worklease v1.9.3 github.go, github_projects.go and github_write.go onto BL-35 scheduling/credentials and BL-32 key policies, not its recovery journal.
2. Implement paged reads, typed edges, Projects status mapping and reconciliation independent of issue updatedAt. Add capability/version-checked writes and stable repository/issue claim identity.
3. Use fake-server tests for pagination, 304, secondary limits, project-only changes and write conflicts; run task test. Then run the required opt-in round trip only in the approved disposable repository/project, recording created IDs and cleanup. Never use or log a token directly; document scopes and credential-helper argv.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Owner-access gate: AC#3 requires a real GitHub round trip. Before selecting this task, the owner must authorize a disposable repository and (for Projects v2 writes) project, permitted create/update/close/delete cleanup operations, and an account credential-helper reference with required scopes. Never request a token in chat. Remove the human label only after these non-secret details and cleanup authority are recorded here. Fake-server implementation remains required; a skipped live test does not satisfy AC#3.
<!-- SECTION:NOTES:END -->
