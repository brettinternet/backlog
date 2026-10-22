---
id: BL-41
title: Sync Jira Cloud issues
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - jira
  - source
milestone: m-3
dependencies:
  - BL-35
  - BL-40
references:
  - docs/providers/jira.md
  - docs/design.md
modified_files:
  - internal/source/jira
  - docs
priority: low
type: feature
ordinal: 41000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

A Jira Cloud source built on the BL-40 evidence:

- Site and JQL scope (a project or saved filter), with API-token credentials through the account helper.
- Coverage reported as estimated or unknown.
- Configured link types as hard edges.
- Raw status kept beside `statusCategory` and resolution. A done status with an unsuccessful resolution maps to `canceled`.
- Writes: transitions discovered per issue, comments, and single-assignee replacement with confirmation.
- Claim keys use `generic` with the site and the numeric issue ID, so a project move never rekeys a claim.

Jira Data Center is out of scope.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Fake server tests cover `nextPageToken` pagination, rate limiting with retry, permission loss, a project move, and an unsuccessful resolution
- [ ] #2 Transitions are offered only when discovered for that issue; writes check the version and report conflicts
- [ ] #3 Claim identity survives a project move (vector test)
- [ ] #4 Setup docs cover token scopes and the Data Center exclusion
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
