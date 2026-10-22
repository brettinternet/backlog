---
id: BL-32
title: 'Show Worklease claims and claim, release, and start through Worklease'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - worklease
  - agents
  - tui
milestone: m-2
dependencies:
  - BL-8
  - BL-15
  - BL-16
references:
  - docs/design.md
  - 'https://github.com/brettinternet/worklease/blob/v1.9.3/docs/claim-model.md'
modified_files:
  - internal/worklease
  - internal/cli
  - internal/tui
  - internal/mcp
  - internal/query
  - docs/cli.md
priority: medium
type: feature
ordinal: 32000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Parallel agents need to see and take claims where they pick work (D20, §9). Worklease stays the claim authority, and `bl` only calls its stable `--json` CLI.

## What

`internal/worklease`:

- In-process key derivation for the `generic`, `github`, and `linear` policies, tested against vectors from `worklease key`.
- Commands run from the invoking working directory, never the control root. The session comes from `--session`, `BL_SESSION`, or `WORKLEASE_SESSION_ID`.
- The overlay polls `worklease --json list` while the TUI is visible and after each claim operation, and schedules expiry rechecks locally.
- `bl claim` and `bl release`. Contention exits 7 and names the holder.
- With coordination on, `bl start` claims first, then changes status, reporting the two outcomes separately.
- In the TUI: the Claim column, `C`, a claim-gated `S`, and the `is:claimed` and `claimed:` queries.
- MCP `claim` and `release`.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Key vectors for each policy match `worklease key`; a script regenerates them, and a `BL_PROVIDER_TESTS=1` test compares them against the installed Worklease
- [ ] #2 `bl claim` under contention exits 7 naming the holder, `bl release` succeeds, and both JSON shapes are in docs/cli.md
- [ ] #3 With a fake Worklease, a failed status change after a successful claim keeps the claim and reports both outcomes
- [ ] #4 The overlay shows `mine`, `agent@host`, and expired, flips at expiry under an injected clock without polling, and never calls Worklease per row
- [ ] #5 With `BL_PROVIDER_TESTS=1`, a round trip against Worklease 1.9.3 acquires, lists, and releases
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
