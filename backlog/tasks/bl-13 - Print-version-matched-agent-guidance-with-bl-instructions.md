---
id: BL-13
title: Print version-matched agent guidance with bl instructions
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 02:13'
labels:
  - agents
  - docs
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
- `workflow`: pick, claim, start, record evidence, finish
- `query`
- `format`
- `json`: envelope, reasons, and exit codes

Running it with no topic lists the topics. Keep the text short and imperative.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 `bl instructions` lists the topics, each topic prints, and an unknown topic exits 2 with the list
- [ ] #2 A test fails when the instructions mention a `bl` command or flag that does not exist
- [ ] #3 The workflow topic shows the agent loop end to end (`next`, `start`, `note`, `ac check`, `done`) with JSON examples
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
1. Embed overview/workflow/query/format/json topics for the actual shipped CLI. No topic lists topics and shows overview. Inspect help/command definitions rather than copying a future design command list.
2. Before BL-32/33, document the unclaimed foundation next/start/note/ac/done operations as such: status changes are not claims, and this is not a safe concurrent-agent loop. Do not recommend independently deriving resource keys or direct Worklease acquisition as a temporary bl protocol. The repository remains on Backlog.md until BL-26, which now waits for BL-33.
3. BL-32 updates claim/resource/transition guidance and BL-33 replaces the provisional workflow with the stable-session CLI/MCP claimed loop. Make these phase boundaries explicit so early instructions do not imply unavailable commands or coordination guarantees.
4. Run documented available examples against an isolated project, check topic/unknown-topic behavior and the command/flag drift test through task test. Keep embedded guidance and docs/cli.md matched to installed capabilities.
5. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and deliver the implementation before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
