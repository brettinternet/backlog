---
id: BL-32
title: Add the coordination seam and Worklease claims
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:51'
labels:
  - worklease
  - agents
  - tui
milestone: m-2
dependencies:
  - BL-8
  - BL-15
  - BL-16
  - BL-27
references:
  - docs/design.md
  - 'https://github.com/brettinternet/worklease/blob/v1.9.3/docs/claim-model.md'
modified_files:
  - internal/model
  - internal/engine
  - internal/worklease
  - internal/cli
  - internal/tui
  - internal/mcp
  - internal/query
  - docs/cli.md
  - docs/automation.md
priority: medium
type: feature
ordinal: 32000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Parallel agents need to see and take claims where they pick work (D20, §9). `bl` knows about claims, not about Worklease. Claims are a typed engine input, because readiness, `next --claim`, and claim-gated Start branch on outcomes that exit-code hooks cannot express.

## What

- A coordination seam in `internal/model` and `internal/engine`: per-item claim state, plus acquire, release, and list with the outcomes `acquired`, `contended` (with holder), `uncertain`, and `unavailable`, and the `{resource}` value per item. No public protocol yet (D18).
- `internal/worklease`, the only implementation:
  - In-process key derivation for the `generic`, `github`, and `linear` policies, tested against vectors from `worklease key`.
  - Commands run from the invoking working directory, never the control root. The session comes from `--session`, `BL_SESSION`, or `WORKLEASE_SESSION_ID`.
  - The overlay polls `worklease --json list` while the TUI is visible and after each claim operation, and schedules expiry rechecks locally.
- `bl claim` and `bl release`. Contention exits 7 and names the holder.
- With coordination on, `bl start` claims first, then changes status, reporting the two outcomes separately.
- In the TUI: the Claim column, `C`, a claim-gated `S`, and the `is:claimed` and `claimed:` queries.
- MCP `claim` and `release`.
- `{resource}` and `BL_RESOURCE` for BL-30 and BL-31.
- Release on done and agent launch are documented hook and action examples, not built-in behavior. No hook, action, or event field names Worklease.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Key vectors for each policy match `worklease key`; a script regenerates them, and a `BL_PROVIDER_TESTS=1` test compares them against the installed Worklease
- [ ] #2 `bl claim` under contention exits 7 naming the holder, `bl release` succeeds, and both JSON shapes are in docs/cli.md
- [ ] #3 With a fake Worklease, a failed status change after a successful claim keeps the claim and reports both outcomes
- [ ] #4 The overlay shows `mine`, `agent@host`, and expired, flips at expiry under an injected clock without polling, and never calls Worklease per row
- [ ] #5 Engine, readiness, and `next` code depend only on the coordination seam; a fake coordinator drives the same tests with no Worklease present
- [ ] #6 With `BL_PROVIDER_TESTS=1`, a round trip against Worklease 1.9.3 acquires, lists, and releases, and the docs/automation.md release-on-done hook releases a real claim
- [ ] #7 Inside a session that already holds the item claim (a `worklease run --resource` worker), `bl start` skips the acquire and changes status
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
1. Read Worklease v1.9.3 CLI/key evidence and build a bounded schema-v2 client. Preserve invoking cwd, session precedence and provider-stable identities; use fixture vectors and a regeneration script instead of per-row key subprocesses.
2. Implement claim/release and claim-first start as two separately reported outcomes, preserving a successful claim when status fails. Wire engine overlay, expiry scheduling, TUI controls, MCP tools and capability-aware instructions.
3. Verify contention, unknown outcomes, cwd/session forwarding, polling cancellation and local expiry with fake CLI/clock fixtures. Run task test and task race, then the opt-in real Worklease round trip in an isolated resource namespace with guaranteed release. Update CLI/MCP/instructions docs.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.

Note (2026-10-22): this plan predates the D20/D21/D27 redesign of coordination, derived events, results, and chaining. Re-plan against the current description and acceptance criteria before starting.
<!-- SECTION:PLAN:END -->
