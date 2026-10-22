---
id: BL-35
title: >-
  Sync remote sources with coverage, cursors, credentials, and per-account
  scheduling
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:46'
labels:
  - source
  - cache
  - security
milestone: m-3
dependencies:
  - BL-11
  - BL-15
  - BL-29
references:
  - docs/design.md
  - 'https://github.com/brettinternet/worklease/tree/v1.9.3/internal/queue'
modified_files:
  - internal/engine
  - internal/source
  - internal/cache
  - internal/project
priority: medium
type: feature
ordinal: 35000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Remote backends need warm starts, honest coverage, quota-aware scheduling, and safe credentials before any one adapter ships (§4.3, §10, §11 hazard 7, D17).

## What

- Generalize the engine for remote sources: `Load(since)` with complete, partial, or unknown coverage; cursors and rows in the cache; per-source workers and badges; and remote optimistic writes with provider versions.
- An HTTP scheduler per host and account, serialized, honoring `Retry-After` and reset headers, with ETags.
- Account credential helpers: an argv from user config, results held in memory only, and redacted everywhere.
- Source pushdown of query predicates where the provider supports them. Results must equal in-memory evaluation (D13).

Port `scheduler.go` and `credential_helper.go` from Worklease v1.9.3 `internal/queue`, dropping the receipt and recovery journal (D24).
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Partial or stale coverage never deletes an item or makes an unknown blocker satisfied
- [ ] #2 Fake-HTTP tests show `Retry-After` and reset headers honored, requests serialized per account, and two accounts running in parallel
- [ ] #3 A canary credential never appears in logs, errors, events, hook environments, or the cache
- [ ] #4 With fake sources, a slow source does not delay another source's updates in the TUI
- [ ] #5 A warm start paints from the cache before any network response arrives
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
1. Port only needed scheduler/credential-helper behavior from Worklease v1.9.3; use its provider probes and tests as evidence, not current main. Extend existing engine/cache contracts for explicit coverage and durable cursors.
2. Implement serialized host/account scheduling, cancellation, quota headers/ETags and memory-only account credentials behind trust. Partial/stale snapshots must retain unseen rows and unresolved dependencies.
3. Use fake HTTP/sources to verify account isolation, rate limits, honest coverage, warm paint before network and credential canaries in every sink. Add pushdown only for exercised predicates with local parity checks; run task test and task race and document source failure/retry state.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
