---
id: BL-28
title: Record events and stream them with bl events
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:45'
labels:
  - events
  - agents
milestone: m-2
dependencies:
  - BL-8
  - BL-15
references:
  - docs/design.md
modified_files:
  - internal/hooks
  - internal/engine
  - internal/cli
  - docs/events.md
priority: medium
type: feature
ordinal: 28000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Other tools coordinate around `bl` through an event stream instead of plugins (D18, §8).

## What

- A per-project JSONL log in XDG state, with ULID IDs and the §8 schema. Appends hold a lock, so lines from concurrent processes never interleave. Rotation by size.
- Every successful `bl`-originated mutation appends exactly one event.
- An external change seen by a sync worker appends a `sync` event, deduplicated by ref and version so several running TUIs emit it once.
- `bl events [--follow] [--json] [--since ID] [QUERY]`.
- docs/events.md documents the schema.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Each mutation command appends one event whose `changes` match the edit
- [ ] #2 Concurrent appends from 8 processes produce valid JSONL
- [ ] #3 `--follow` delivers new events within 200 ms, and `--since` resumes without gaps or duplicates across rotation
- [ ] #4 Two running TUIs that see the same external edit produce one `sync` event
- [ ] #5 docs/events.md documents the schema
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
1. Add locked, bounded JSONL appends and rotation under XDG state, using the shared mutation-success point so CLI/TUI/MCP do not emit duplicate events.
2. Persist external-change deduplication by project/source/ref/version across processes. Implement since/follow across rotations with stable event IDs and an explicit streaming JSON contract distinct from one-shot command envelopes.
3. Verify one event per mutation, eight-process valid appends, resume across rotation and duplicate external observations. Use injected time/observable conditions rather than sleeps; run task test and task race, record follow latency, and freeze the schema/stream format in docs/events.md.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
