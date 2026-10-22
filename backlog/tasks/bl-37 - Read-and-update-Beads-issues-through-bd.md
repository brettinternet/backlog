---
id: BL-37
title: Read and update Beads issues through bd
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:46'
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

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Port Worklease v1.9.3 beads.go and beads_write.go, retaining bulk dependency reads and version checking while dropping receipts/recovery. Keep executable argv/context bounded and cancellable.
2. Use --readonly --sandbox only for reads; write with the provider's supported update/comment commands after checking expected version. Map only blocks as hard edges and expose truthful capabilities.
3. Verify bulk process count independent of N, mapping, malformed JSON and write conflicts through a fake bd executable. Run task test and an opt-in isolated bd 1.3.0 round trip; record installation/setup requirements and avoid touching any real user database.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
