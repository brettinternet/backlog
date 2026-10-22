---
id: BL-6
title: Implement the query language and the quick-add parser
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:49'
labels:
  - query
milestone: m-0
dependencies:
  - BL-2
  - BL-5
references:
  - docs/query.md
  - docs/tui.md
modified_files:
  - internal/query
  - docs/query.md
priority: high
type: feature
ordinal: 6000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

One query language serves the TUI, the CLI, views, hooks, actions, and MCP (D13). Quick-add tokens make creation one line in both the TUI and `bl new` (tui.md#quick-add).

## What

- `internal/query`: parse to an AST with positions; evaluate against items, readiness, and an injected clock; sort and group as docs/query.md states.
- Errors are positioned and suggest close field names.
- The quick-add tokenizer from tui.md, shared by the TUI and `bl new`.
- Completion candidates for fields and values from a snapshot, for BL-18.
- Freeze docs/query.md: set its status to Frozen and reconcile any gaps.

Source pushdown waits for the first remote source (BL-35). Do not add the interface now.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Table tests cover every field and `is:` and `has:` value, negation, comma OR lists, ordered operators, relative dates under an injected clock, and quoted phrases with escapes
- [ ] #2 Malformed queries return positioned errors with suggestions, including the docs/query.md example
- [ ] #3 Sort and group tests cover every key, items in several label groups, workflow order, and the documented tie-breaks
- [ ] #4 Quick-add parses every tui.md token, escape, and quoted string; the query and quick-add fuzz targets each run 60 s without failures
- [ ] #5 docs/query.md says Frozen and matches the implementation
- [ ] #6 Hook-only event terms (`changed:` including `changed:readiness`, `hook:`, `action:`, `outcome:`) parse, evaluate against an optional event context, and are rejected with a positioned error outside hook `if:`
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
1. Read docs/query.md and the quick-add section of docs/tui.md. Implement positioned AST parsing and evaluation using injected time and readiness; keep query-context defaults (such as ls adding is:open) out of the pure parser.
2. Implement sort/group and snapshot completion, then the shared quick-add tokenizer including escaped literal tokens. changed: is available only with mutation context; claim/pending inputs must not require Worklease or the later TUI to exist.
3. Verify parsing, evaluation, sorting and quick-add with table tests, then run separate 60s fuzz sessions for both parsers. Freeze docs/query.md with exact text-match semantics so BL-11 can preserve equivalence through FTS; defer provider pushdown to BL-35.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
