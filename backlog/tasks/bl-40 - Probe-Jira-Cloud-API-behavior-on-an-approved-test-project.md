---
id: BL-40
title: Probe Jira Cloud API behavior on an approved test project
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - jira
  - spike
  - human
milestone: m-3
dependencies: []
references:
  - docs/design.md
  - >-
    https://github.com/brettinternet/worklease/blob/v1.9.3/docs/work-queue-tui-proposal.md
modified_files:
  - docs/providers/jira.md
  - docs/design.md
priority: low
type: spike
ordinal: 40000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Jira identity and write semantics must come from observation, not documentation. This carries over Worklease TASK-139.1, which was blocked because no test project existed.

## Needs the owner

An approved Jira Cloud site URL, disposable project keys that allow issue create, link, move, transition, and delete, a test accountId, and a private credential helper for an API token. Never ask for the token in chat.

## What

Use synthetic issues only, and remove them afterward. Probe:

- search pagination and ordering on `POST /rest/api/3/search/jql`
- whether adding or removing a link changes `updated` on either issue
- identity across a project move (the numeric ID versus the key)
- deletion and permission-loss visibility
- transition field requirements
- rate-limit responses and headers
- ADF comment round-trips

Record the results in `docs/providers/jira.md`, separating observations from unknowns, and update design.md §10.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 docs/providers/jira.md records each listed behavior as observed or unknown, from a probe on an owner-approved project; every created issue was removed
- [ ] #2 The doc names a stable source identity and issue key for claims and versions, and design.md §10 cites it
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
