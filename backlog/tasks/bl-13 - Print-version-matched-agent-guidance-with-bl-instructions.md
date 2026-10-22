---
id: BL-13
title: Print version-matched agent guidance with bl instructions
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:45'
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
1. Embed concise overview/workflow/query/format/json topics matched to the current CLI. No-topic invocation lists topics and prints the overview; explicit overview prints that topic.
2. Show the foundation loop using next/start/note/ac/done and JSON; explain that direct worklease acquire/release is the interim claim path. Do not teach bl claim or next --claim as available until BL-32/33 updates this guidance.
3. Run the documented examples against an isolated temporary project and assert referenced commands/flags exist. Verify unknown-topic exit 2 and topic listing through task test; keep embedded text and docs/cli.md synchronized.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
