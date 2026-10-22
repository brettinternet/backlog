---
id: BL-32
title: Add the coordination seam and Worklease claims
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:55'
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
  - BL-28
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

Parallel agents need to see and take claims where they pick work (D20, §9). `bl` knows about claims, not about Worklease. Claims are a typed engine input, because readiness, `next --claim`, and claim-gated status transitions branch on outcomes that exit-code hooks cannot express.

## What

- A coordination seam in `internal/model` and `internal/engine`: per-item claim state, plus acquire, release, and list with the outcomes `acquired`, `contended` (with holder), `uncertain`, and `unavailable`, and the `{resource}` value per item. No public protocol yet (D18).
- `internal/worklease`, the only implementation:
  - In-process key derivation for the `generic`, `github`, and `linear` policies, tested against vectors from `worklease key`.
  - Commands run from the invoking working directory, never the control root. The session comes from `--session`, `BL_SESSION`, or `WORKLEASE_SESSION_ID`, and otherwise is unscoped. MCP takes `session` explicitly and never reads it from the server's environment.
  - One claim per session: `handle-in-use` maps to `claim-held` (exit 4) naming the held item, never to contention.
  - The overlay polls `worklease --json list` while the TUI is visible and after each claim operation, and schedules expiry rechecks locally.
- `bl claim` and `bl release`. Contention exits 7 and names the holder.
- The §9 status-transition rules in the engine, so `bl start`, `bl done`, `bl set status=`, the TUI, and MCP `update` behave identically. Entering an active category acquires first and writes nothing on contention. Entering a closed category releases the session's claim after the write. Both report the claim and status outcomes separately. No release under `WORKLEASE_RUN_ID`. Multi-item transitions into an active category are refused.
- In the TUI: the Claim column, `C`, a claim-gated `S`, and the `is:claimed` and `claimed:` queries.
- MCP `claim` and `release`, and a required `session` on `update` calls that change status while coordination is on.
- `resource` in `bl show --json`, plus `{resource}` and `BL_RESOURCE` for BL-30 and BL-31.
- Agent launch and worktree setup are documented action examples, not built-in behavior. No hook, action, or event field names Worklease.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Key vectors for each policy match `worklease key`; a script regenerates them, and a `BL_PROVIDER_TESTS=1` test compares them against the installed Worklease
- [ ] #2 `bl claim` under contention exits 7 naming the holder, `bl release` succeeds, and both JSON shapes are in docs/cli.md
- [ ] #3 With a fake Worklease, a failed status change after a successful claim keeps the claim and reports both outcomes
- [ ] #4 The overlay shows `mine`, `agent@host`, and expired, flips at expiry under an injected clock without polling, and never calls Worklease per row
- [ ] #5 Engine, readiness, and `next` code depend only on the coordination seam; a fake coordinator drives the same tests with no Worklease present
- [ ] #6 With BL_PROVIDER_TESTS=1, a round trip against Worklease 1.9.3 acquires through `bl start`, lists, and releases through `bl done` on an isolated claim
- [ ] #7 Inside a session that already holds the item claim (a `worklease run --resource` worker), `bl start` skips the acquire and changes status
- [ ] #8 Through each of `bl start`/`bl done`, `bl set status=`, the TUI, and MCP `update`: contention on entering an active category writes nothing and exits 7, entering a closed category releases the session's claim after the write with both outcomes reported, and with `WORKLEASE_RUN_ID` set no release is attempted
- [ ] #9 An acquire while the session holds another active claim fails with `claim-held` (exit 4) naming the held item, and an MCP status change without `session` is refused before anything is written
- [ ] #10 `bl show --json` reports `resource`, equal to the key `bl claim` acquires
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
1. Implement design.md D20's narrow coordination seam in model/engine: per-item claim state, acquire/release/list outcomes acquired/contended/uncertain/unavailable, and optional stable resource identity. Readiness and next depend on this seam, not Worklease types; do not add a public protocol or a second implementation.
2. Implement the sole Worklease schema-v2 client using v1.9.3 evidence, bounded subprocesses, invoking cwd and session precedence. Derive provider keys in-process from stable identities and verify fixture vectors; never invoke Worklease per row.
3. Wire claim/release, the engine-level active/closed transition rules, overlay expiry, TUI controls and MCP. Entering active uses an already-held claim for the same session without reacquiring; a newly acquired claim survives status failure, with separate outcomes. Entering closed releases synchronously after the write, never under WORKLEASE_RUN_ID. Emit claim events via BL-28, but do not trigger data-derived readiness events on overlay changes.
4. Expose resource identity in show --json and for BL-30/31 without knowing about hook workflows. Agent launch and worktree setup remain documented examples owned by those tasks; no built-in launch. Update CLI/MCP/instructions docs.
5. Verify with a fake coordinator and fake CLI: outcome mapping, session/cwd forwarding, claim-held mapping, inherited claim, transition rules on every surface, expiry without polling, and retained claim after status failure. Run task test and task race, then the isolated BL_PROVIDER_TESTS=1 start/list/done round trip.
6. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
