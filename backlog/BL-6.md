---
id: BL-6
title: Implement the query language and the quick-add parser
status: done
type: feature
priority: high
assignees: ["@brett"]
labels: [query, reviewed]
milestone: m-0
blocked_by: [BL-2, BL-5]
rank: a1e1e1e1e1e1e1e1e
refs: [docs/query.md, docs/tui.md]
files: [internal/query, docs/query.md]
created: 2026-10-22T00:50:00Z
updated: 2026-10-30T05:44:32Z
---

## Why

One query language serves the TUI, the CLI, views, hooks, actions, and MCP (D13). Quick-add tokens make creation one line in both the TUI and `bl new` (tui.md#quick-add).

## What

- `internal/query`: parse to an AST with positions; evaluate against items, readiness, and an injected clock; sort and group as docs/query.md states.
- Errors are positioned and suggest close field names.
- The quick-add tokenizer from tui.md, shared by the TUI and `bl new`.
- Completion candidates for fields and values from a snapshot, for BL-18.
- Freeze docs/query.md: set its status to Frozen and reconcile any gaps.

Source pushdown waits for the first remote source (BL-35). Do not add the interface now.

## Acceptance criteria

- [x] #1 Table tests cover every field and `is:` and `has:` value, negation, comma OR lists, ordered operators, relative dates under an injected clock, and quoted phrases with escapes
- [x] #2 Malformed queries return positioned errors with suggestions, including the docs/query.md example
- [x] #3 Sort and group tests cover every key, items in several label groups, workflow order, and the documented tie-breaks
- [x] #4 Quick-add parses every tui.md token, escape, and quoted string; the query and quick-add fuzz targets each run 60 s without failures
- [x] #5 docs/query.md says Frozen and matches the implementation
- [x] #6 Hook-only event terms (`changed:` including `changed:readiness`, `hook:`, `action:`, `outcome:`) parse, evaluate against an optional event context, and are rejected with a positioned error outside hook `if:`
- [x] #7 Milestone groups follow catalog order, with undeclared milestones after them in value order; completion offers declared, unarchived labels and milestones (with names) before values in use

## Definition of done

- [x] #1 task ci passes on the final commit
- [x] #2 Every checked acceptance criterion has an AC#N evidence line in Implementation Notes naming the command and its result
- [x] #3 Substantial changes had an independent verifier pass that returned PASS for every acceptance criterion
- [x] #4 The diff stays within the task's modified-file list, or Implementation Notes justify the deviation
- [x] #5 No test was deleted, skipped, or weakened
- [x] #6 User-visible behavior changes are reflected in README.md or docs/ in the same change

## Plan

1. Read docs/query.md and the quick-add section of docs/tui.md. Implement positioned AST parsing and evaluation using injected time and readiness; keep query-context defaults (such as ls adding is:open) out of the pure parser.
2. Implement sort/group and snapshot completion, then the shared quick-add tokenizer including escaped literal tokens. Represent an optional event evaluation context for changed: (including readiness), hook:, action: and outcome:. These terms are accepted only in hook if: and rejected with positioned errors elsewhere; use pure context fixtures without depending on the later event log. Claim/pending inputs must not require Worklease or the TUI to exist.
3. Verify parsing, evaluation, sorting and quick-add with table tests, then run separate 60s fuzz sessions for both parsers. Freeze docs/query.md with exact text-match semantics so BL-11 can preserve equivalence through FTS; defer provider pushdown to BL-35.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.

## Notes

Implementation scope: internal/query and docs/query.md as planned. The pure query snapshot will accept derived readiness, claim/pending/archive overlays and next order as inputs, avoiding a query-to-engine dependency cycle. Parser validation is separate from snapshot value validation so CLI errors and TUI warnings can share diagnostics. No source pushdown interface.

Implemented positioned AST parsing, separate snapshot validation, pure match/projection and completion, event-only hook mode, and shared quick-add parsing in internal/query. docs/query.md is Frozen and specifies literal substring text equivalence for later FTS. Focused query suite and task ci PASS. Both 60s fuzz runs PASS: FuzzQuery 4,973,791 executions; FuzzQuickAdd 7,426,589 executions. Independent acceptance verifier is running before commit/integration. Delivery remains commit, merge main, and Worktrunk cleanup; no push requested.

AC#1: task cli:test PKG=./internal/query -- -count=1 — PASS; TestFields covers all fields, is/has predicates, AND/OR/negation, ordered priorities, relative dates under injected time, escaped quotes, and missing overlays.
AC#2: task cli:test PKG=./internal/query -- -count=1 — PASS; TestErrors verifies positioned syntax/value errors and exact unknown field "lable" at 7; did you mean "label"? diagnostic.
AC#3: task cli:test PKG=./internal/query -- -count=1 — PASS; TestSort and TestGroupsAndCompletion cover all sort/group keys, workflow ordering, label memberships, reversal, and real engine next/ID tie-breaks.
AC#4: task cli:test PKG=./internal/query -- -count=1 — PASS TestQuickAdd; task cli:test PKG=./internal/query -- -run="^$" -fuzz="^FuzzQuery$" -fuzztime=60s -parallel=2 — PASS, 4,973,791 executions; same command with FuzzQuickAdd — PASS, 7,426,589 executions. Independent verifier reran both for 60s: PASS, 3,934,821 / 8,880,247 executions.
AC#5: task cli:test PKG=./internal/query -- -count=1 and independent verifier read of docs/query.md against all implementation files — PASS; Frozen spec defines text equivalence, dates, sort/group, validation, completion, and quick-add behavior.
AC#6: task cli:test PKG=./internal/query -- -count=1 — PASS TestEvents for changed/readiness, hook/action/outcome, missing and wrong event contexts, and positioned rejection outside hook mode.
AC#7: task cli:test PKG=./internal/query -- -count=1 — PASS TestGroupsAndCompletion for milestone catalog order then undeclared values, declared labels first, unarchived named milestone completion, and display-name matching.
Independent verifier 6d681e47-4c38-44fe-bbc9-a52b8c06adab returned PASS for AC1–AC7, no acceptance-blocking defects. Single verification pass; only concrete correction was reserved NONE validation, covered by the extended existing test. All affected LSP diagnostics clean. task check:staged and task ci PASS on implementation commit fe2316ecc65f038e7a892c6b925e46c9903e37c6. No existing tests were deleted, skipped, or weakened. Source diff is limited to internal/query and docs/query.md.
Delivery: committed feat(query): add query language and quick-add parser (fe2316e), fast-forward merged into main. Verified session receipt and clean linked worktree; wt remove bl-6-query --foreground --format=json deleted the worktree and branch. Herdr workspace wHX disappeared; git worktree list reports primary only. No retained task checkout or workspace, no push. Remaining blocker: none. Next resumable step: none for BL-6; consumers can now use the pure query package.

Review (62249df): quick-add treats colons as reference syntax only for ^ and <; field.NAME matches raw keys case-insensitively; field-name completion offers raw keys in use; priority groups follow priority order; docs/query.md updated. FuzzQuickAdd 20 s PASS; task ci PASS.

## Summary

Implemented and froze the shared query language, projections/completion, hook-only event predicates, and quick-add parser. Focused tests, both 60s fuzz runs, independent AC1–AC7 verification, staged hooks and task ci pass. Implementation fe2316e merged to main; worktree, branch and workspace removed.
