---
id: BL-9
title: 'Add init, check, and renumber'
status: Done
assignee:
  - '@pi'
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 04:16'
labels:
  - cli
  - format
milestone: m-0
dependencies:
  - BL-4
  - BL-7
references:
  - docs/format.md
  - docs/design.md
modified_files:
  - internal/cli
  - internal/format
  - internal/source/native
  - docs/cli.md
priority: high
type: feature
ordinal: 9000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

New projects need one-command setup. CI and agents need a validator. Concurrent clones can still produce duplicate IDs, which need a safe repair that rewrites references (D6).

## What

- `bl init [--prefix P] [--path DIR]` writes `.config/backlog.yaml` and the item directory. It refuses to overwrite an existing config (exit 5).
- `bl check [--json] [--fix]` implements the docs/format.md validation table, plus status values from config. Duplicate IDs across active and archived items are detected.
- The codec rejects a file with conflict markers as `merge-conflict` (D28), checked before YAML parsing, so no command or TUI edit writes to a half-merged item. `--fix` resolves only `updated`-only and comment-only hunks.
- `bl renumber FROM TO` renames the file and rewrites every reference (`blocked_by`, `parent`, `related`, `duplicate_of`, and `source:ID` forms) in active and archived items. It refuses when TO exists.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 `bl init` in a fresh repository creates a config and directory that `bl check` accepts; a second run exits 5 and changes nothing
- [x] #2 Fixture files trigger every docs/format.md finding code with path and line; exit is 5 with errors and 0 otherwise; `--json` lists the findings
- [x] #3 `bl check --fix` applies only the fixes docs/format.md allows
- [x] #4 `bl renumber` rewrites every reference form in active and archived items and refuses an existing target
- [x] #5 Conflict markers in frontmatter or body report `merge-conflict`, not `invalid-frontmatter`, and every write to that file is refused; `--fix` resolves an `updated`-only hunk and a comment-only hunk in both `merge` and `diff3` marker styles and leaves any other hunk, and its error, in place
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
1. Implement init without overwriting existing config. Add check findings directly from the format validation table, including active/archive duplicates, graph cycles and status lookup.
2. Implement only documented --fix repairs under the write lock. Renumber must preflight the destination and all affected reference edits (including archived and qualified refs) before changing files; fail visibly without silently corrupting a partially repaired graph.
3. Exercise every finding code, idempotent init, restricted fixes, target collision and reference rewrite through CLI fixtures. Document duplicate disambiguation and recovery behavior before freezing renumber syntax; run task test and direct init/check in a temporary repository.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implementation prepared in .worktrees/feat-bl-9-maintenance; executor reports focused tests and task ci passed. Independent verifier is checking AC#1–5, concentrating on conflict repair data integrity and renumber safety. No implementation commit yet.

AC#1: task cli:test PKG=./internal/cli -- -run TestInitCreatesValidProjectAndRefusesOverwrite -count=1 passed (covered in focused package run and task ci): fresh init/check succeeds; repeat exits 5 unchanged.
AC#2: task cli:test PKG=./internal/cli -- -run TestCheckReportsEveryFindingCodeWithLocations -count=1 passed (covered in focused package run and task ci): all validation codes have paths/lines, JSON and error/success exits verified.
AC#3: task cli:test PKG="./internal/format ./internal/cli" -- -count=1 passed: restricted fixes, comment byte integrity, and rejection of diff3 comment edits/deletions verified.
AC#4: task cli:test PKG=./internal/cli -- -run TestRenumberRewritesAllReferenceFormsAndRefusesAmbiguityAndCollision -count=1 passed (covered in focused package run and task ci): active/archive, qualified/unqualified references, duplicate selection and target collisions verified.
AC#5: task cli:test PKG="./internal/format ./internal/cli" -- -count=1 passed: merge/diff3 updated/comment repair, unsupported hunk retention, pre-YAML conflict diagnosis and write refusal verified.
Independent verifier returned PASS for AC#1–5 after fixing its two concrete comment-repair findings (header newline and diff3 base deletion/edit refusal); no second general review. task check:staged and task ci passed on source commit 1fd4458 and merged implementation 4937370. Concurrent main changes required a merge resolving command registration alongside dynamic JSON value-flag discovery; merged task ci passed. All changes remain in declared modified-file scope; no tests deleted/skipped/weakened; docs/cli.md updated. Owned Worktrunk checkout and branch removed after integration; no push. No remaining blocker.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added init, native validation and restricted repairs, and reference-safe renumbering. Independent verification PASS for all five criteria; full CI passed on merged implementation 4937370. Source commit 1fd4458 merged to main; worktree and branch cleaned up.
<!-- SECTION:FINAL_SUMMARY:END -->
