---
id: BL-42
title: Define the external adapter protocol and a reference adapter
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - external
  - source
milestone: m-3
dependencies:
  - BL-35
references:
  - docs/design.md
  - >-
    https://github.com/brettinternet/worklease/blob/v1.9.3/docs/external-adapter-protocol.md
modified_files:
  - internal/source/external
  - cmd/bl-reference-adapter
  - internal/cli
  - docs/external-adapter-protocol.md
priority: low
type: feature
ordinal: 42000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

- External source adapter protocol v1 (§8): JSON-RPC 2.0 over stdio. It covers initialize and manifest, capabilities, paged list, read, apply with expected version, and cancellation, with per-frame and per-response bounds. Derive it from Worklease's external adapter protocol v1, dropping receipts and recovery.
- docs/external-adapter-protocol.md with JSON Schemas, frozen as v1.
- The host side, with bounded timeouts and restart backoff.
- `bl adapter check EXEC` for conformance.
- A reference adapter under `cmd/bl-reference-adapter`.
- Committed adapters stay inert until trusted (BL-29).
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 docs/external-adapter-protocol.md is frozen as v1, with schemas
- [ ] #2 `bl adapter check` passes the reference adapter and reports oversized frames, bad versions, and missing capabilities in failing test adapters
- [ ] #3 A crashing or hanging adapter is bounded by timeouts, restarted with backoff, and shown with an error badge
- [ ] #4 An untrusted committed adapter never starts
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
