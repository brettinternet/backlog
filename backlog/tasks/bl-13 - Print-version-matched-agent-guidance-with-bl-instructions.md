---
id: BL-13
title: Print version-matched agent guidance with bl instructions
status: Done
assignee:
  - pi-bl-20260906-01
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 06:25'
labels:
  - agents
  - docs
  - reviewed
milestone: m-0
dependencies:
  - BL-8
  - BL-9
references:
  - docs/design.md
modified_files:
  - internal/instructions
  - internal/cli
priority: high
type: feature
ordinal: 13000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Agents should learn the workflow from the binary they're running, not from stale docs (D19). BL-26 points this repository's AGENTS.md at it.

## What

`bl instructions [TOPIC]` prints embedded Markdown:

- `overview` (shown with the topic list when no topic is supplied)
- `workflow`: pick, claim, start, record evidence, finish. Pass `--if-version` with the version from the last read or write whenever a write depends on what was read, such as `bl ac ID check N`. Record evidence with `bl ac ID check N --evidence TEXT`, not in Notes by number.
- `query`
- `format`
- `json`: envelope, reasons, and exit codes

Running it with no topic lists the topics. Keep the text short and imperative.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 `bl instructions` lists the topics, each topic prints, and an unknown topic exits 2 with the list
- [x] #2 A test fails when the instructions mention a `bl` command or flag that does not exist
- [x] #3 The workflow topic shows the agent loop end to end (`next`, `start`, `note`, `ac check`, `done`) with JSON examples
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 task ci passes on the final commit
- [x] #2 Every checked acceptance criterion has an AC#N evidence line in Implementation Notes naming the command and its result
- [x] #3 Substantial changes had an independent verifier pass that returned PASS for every acceptance criterion
- [x] #4 The diff stays within the task's modified-file list, or Implementation Notes justify the deviation
- [x] #5 No test was deleted, skipped, or weakened
- [x] #6 User-visible behavior changes are reflected in README.md or docs/ in the same change
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Embed overview/workflow/query/format/json topics for the actual shipped CLI. No topic lists topics and shows overview. Inspect help/command definitions rather than copying a future design command list.
2. Before BL-32/33, document the unclaimed foundation next/start/note/ac/done operations as such: status changes are not claims, and this is not a safe concurrent-agent loop. Do not recommend independently deriving resource keys or direct Worklease acquisition as a temporary bl protocol. This repository's interim claim protocol lives in AGENTS.md (BL-26), not in bl instructions.
3. BL-32 updates claim/resource/transition guidance and BL-33 replaces the provisional workflow with the stable-session CLI/MCP claimed loop. Make these phase boundaries explicit so early instructions do not imply unavailable commands or coordination guarantees.
4. Run documented available examples against an isolated project, check topic/unknown-topic behavior and the command/flag drift test through task test. Keep embedded guidance and docs/cli.md matched to installed capabilities.
5. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and deliver the implementation before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implementation in .worktrees/feat-bl-13-instructions. Modified-file scope includes docs/cli.md to satisfy the user-visible documentation DoD. Embedded five topics and CLI JSON/usage handling; focused TestInstructions tests pass, including live command/flag drift validation and an isolated version-guarded agent loop. No decision-register changes: D19 implemented with explicit BL-32/33 coordination boundaries.

AC#1: task cli:test PKG=./internal/cli -- -run TestInstructionsTopics -count=1 (covered by TestInstructions run) passed: default topic list/overview, all five Markdown/JSON topics, unknown/extra/path-like topics exit 2 and list valid topics; broken config does not block guidance.
AC#2: task cli:test PKG=./internal/cli -- -run TestInstructionsCommandDrift -count=1 (covered by TestInstructions run) passed: embedded command/flag references match live help; negative probes reject nonexistent commands/flags, nested config command, and next --if-version.
AC#3: task cli:test PKG=./internal/cli -- -run TestInstructionsWorkflow -count=1 (covered by TestInstructions run) passed: isolated next/show/start/note/ac-check/done loop consumes returned JSON versions and persists criterion evidence; embedded workflow includes JSON response example and correct version paths.
Independent verifier 9de6a518-affc-4bef-9628-56f82fb86aaf returned PASS for AC1, AC2, AC3 after task cli:test PKG=./internal/cli -- -run TestInstructions -count=1. No concrete defects found; no tests deleted, skipped, or weakened. LSP diagnostics clean for command implementation.
Delivery: implementation commit 68b0c4a passed task check:staged and task ci, then fast-forward merged into main. Worktrunk removed session-owned feat/bl-13-instructions checkout and branch; Herdr workspace wJA disappeared after the post-remove hook. No retained task worktree/workspace. No push performed. No remaining blocker or next implementation step.

Review 6df19c6: removed repository task IDs (BL-32/BL-33) from shipped workflow guidance and replaced the query negation example, which failed with unknown-label usage error in projects without a wontfix label, with -priority:low (verified live). TestInstructions and internal/fixtures tests pass.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added embedded overview/workflow/query/format/json agent guidance with JSON output, usage-topic errors, and explicit pre-coordination workflow boundaries. Verified topic handling, live command/flag drift detection, version-guarded workflow, independent PASS on every AC, and task ci on implementation commit 68b0c4a. Merged to main; worktree, branch, and workspace cleaned up.
<!-- SECTION:FINAL_SUMMARY:END -->
