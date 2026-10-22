---
id: BL-33
title: Claim the next ready item with bl next --claim
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:46'
labels:
  - worklease
  - agents
milestone: m-2
dependencies:
  - BL-32
references:
  - docs/design.md
modified_files:
  - internal/worklease
  - internal/cli
  - internal/mcp
priority: medium
type: feature
ordinal: 33000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

The §9 `next --claim [--start]` walk: one snapshot, the §6 order, a no-wait acquire per candidate, skip contended items, hold at most one claim, stop on an uncertain acquire, never loop (Worklease D28). JSON reports every attempt. MCP `next` gets the same option.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 With a fake Worklease, a contended first candidate is skipped and the second is claimed
- [ ] #2 An uncertain acquire stops the walk with reason `unknown-outcome`, names the item, and makes no further attempts
- [ ] #3 A test asserts the acquire call sequence: never more than one held claim and no second pass
- [ ] #4 `--start` changes status after the claim and reports the two outcomes separately
- [ ] #5 Two concurrent `next --claim` runs get different items (fake Worklease by default; real with `BL_PROVIDER_TESTS=1`)
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
1. Extend the shared next operation with one ranked snapshot and a bounded no-wait acquire walk. Skip only confirmed contention; uncertain acquire stops immediately and preserves the possible held claim for caller recovery.
2. Expose identical --claim/--start/session semantics in CLI and MCP, with all attempts and separate claim/status outcomes. Caller still owns heartbeat/release; never make a second pass.
3. Verify contention-first/success-second, uncertainty stop, single-held-claim and concurrent callers using a stateful fake authority. Run task test and optional isolated real-provider checks; update docs/cli.md, docs/mcp.md and embedded workflow guidance.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
