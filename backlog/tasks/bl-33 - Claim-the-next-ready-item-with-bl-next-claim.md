---
id: BL-33
title: Claim the next ready item with bl next --claim
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:55'
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
  - internal/instructions
  - docs/cli.md
  - docs/mcp.md
priority: medium
type: feature
ordinal: 33000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

The §9 `next --claim [--start]` walk: one snapshot, the §6 order, a no-wait acquire per candidate, skip contended items, hold at most one claim, stop on an uncertain acquire, never loop (Worklease D28). JSON reports every attempt.

- **Session first.** Refuse an unscoped session with `session-required` (exit 2) before calling Worklease. Then check the session's handle. A held or pending claim on an open item is returned with `resumed: true`, and a pending acquire is replayed exactly. A held claim on a closed item fails with `claim-held`.
- **MCP parity.** MCP `next` takes `claim`, `start`, and a required `session`, and returns the same attempts and outcomes.
- **Guidance.** The `bl instructions` workflow topic teaches the loop: one stable session for the whole loop, claim and start only with `bl next --claim --start` or MCP `next`, finish with `bl done`. Never treat a status change as a claim, and never derive keys with `worklease key`.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 With a fake Worklease, a contended first candidate is skipped and the second is claimed
- [ ] #2 An uncertain acquire stops the walk with reason `unknown-outcome`, names the item, and makes no further attempts
- [ ] #3 A test asserts the acquire call sequence: never more than one held claim and no second pass
- [ ] #4 `--start` changes status after the claim and reports the two outcomes separately
- [ ] #5 Two concurrent `next --claim --start` calls in one checkout with different sessions, one through the CLI and one through MCP, get different items with no error, and each status changes only after its claim (fake Worklease by default; real with `BL_PROVIDER_TESTS=1`)
- [ ] #6 Without a session, `next --claim` exits 2 with `session-required` and calls no Worklease command; MCP `next` with `claim` and no `session` is refused the same way
- [ ] #7 When the session's handle holds or is acquiring a claim on an open item, `next --claim` returns it with `resumed: true` and acquires nothing new; a held claim on a closed item fails with `claim-held`
- [ ] #8 One session looping `next --claim --start`, then `done`, then `next --claim` claims a second item with no `claim-held`, so the release on done finished before `done` returned
- [ ] #9 The `bl instructions` workflow topic teaches the claimed loop through both CLI and MCP, and the existing instructions test covers every command and flag it names
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
1. Extend the shared next operation against BL-32's typed coordination seam, with one ranked snapshot and a bounded no-wait acquire walk. Never branch on Worklease-specific result types; unavailable and uncertain outcomes must not be treated as contention. Skip only confirmed contention; uncertain acquire stops immediately and preserves the possible held claim for caller recovery.
2. Expose identical --claim/--start/session semantics in CLI and MCP, with all attempts and separate claim/status outcomes. Check the session's handle before walking: resume a held or pending open item, refuse a closed one. Caller owns heartbeat; release happens on the closed transition (BL-32); never make a second pass.
3. Verify contention-first/success-second, uncertainty stop, single-held-claim and concurrent callers using a stateful fake authority. Run task test and optional isolated real-provider checks; update docs/cli.md, docs/mcp.md and embedded workflow guidance.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
