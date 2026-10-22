---
id: BL-4
title: Load and write native items with a shared ID allocator and atomic writes
status: Done
assignee:
  - '@pi'
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 04:36'
labels:
  - source
  - format
  - git
  - reviewed
milestone: m-0
dependencies:
  - BL-2
  - BL-3
references:
  - docs/format.md
  - docs/design.md
modified_files:
  - internal/source
  - internal/model
priority: high
type: feature
ordinal: 4000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Agents in different worktrees create items concurrently, so IDs must be unique without coordination beyond the shared Git directory (D6). Writes must never corrupt or silently overwrite a file (D8, D15).

## What

- `internal/source`: the Source interface from §4.3 and a registry. `internal/source/native` implements Load, Read, and Apply. Watch arrives in BL-15.
- Load parses `.md` files directly under the source path in parallel, bounded by GOMAXPROCS, and skips `archive/` unless asked. A bad file becomes a finding and does not abort the load.
- Apply checks the expected version (content hash) under the repository write lock, then writes atomically: temp file in the same directory, fsync, rename, fsync the directory.
- ID allocator (format.md#ids): flock on `<git-common-dir>/backlog/lock`, counter at `<git-common-dir>/backlog/next/<source-id>`, seeded from the highest active or archived ID. Outside Git, use XDG state keyed by project ID.
- Archive and unarchive move the file and change no bytes.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A multi-process test creates 400 items concurrently from two linked worktrees and gets 400 distinct IDs
- [x] #2 Apply with a stale expected version returns reason `conflict` and leaves the file unchanged; a failed write leaves the original intact and no temp files behind
- [x] #3 Load reports per-file parse errors without aborting, ignores non-item files, and loads `archive/` only on request
- [x] #4 Archive and unarchive move the file, keep its bytes, and keep the allocator from reusing the ID
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
1. Implement the design.md §4.3 Source contract and native backend using BL-2 documents and BL-3 roots. Return per-file findings and explicit coverage; archive loading is opt-in.
2. Under the shared repository lock, seed/reconcile the allocator against active and archived IDs, check expected content hashes, and publish same-directory temp files using fsync/rename. Preserve originals and clean owned temporary files on failure; never reuse an archived ID.
3. Verify the public source API with stale-version/failing-write cases and an actual subprocess concurrency harness creating 400 items from two temporary linked worktrees. Exercise byte-preserving archive/unarchive and bad-file isolation; run task test and task race.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.

5. Deliver in bl-4-native-source: add source contracts/registry, create/archive operations to model, native backend and public-API integrity tests. Extend docs/format.md to document source semantics (justified addition outside original internal/source, internal/model scope). Commit, run final-commit CI, merge main locally, and remove the owned worktree.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implementation complete in .worktrees/bl-4-native-source. Focused integrity suite passed (task cli:test PKG=./internal/source/... -- -count=1); task ci passed including security, static checks, all tests, race detector, and smoke. Independent verifier in progress. docs/format.md is the justified scope addition documenting version guards, loading coverage, allocator gaps/reconciliation, and post-rename durability semantics. No existing test was changed, deleted, skipped, or weakened.

AC#1: task cli:test PKG=./internal/source/... -- -count=1 — PASS; TestMultiProcessWorktreeAllocation launched eight processes from two linked worktrees and verified 400 distinct IDs.
AC#2: task cli:test PKG=./internal/source/... -- -count=1 — PASS; TestApplyIntegrity verified stale/missing-version conflict refusals and a subprocess partial-write failure preserving the original with no temporary files left.
AC#3: task cli:test PKG=./internal/source/... -- -count=1 — PASS; TestLoadAndArchive verified per-file parse/filename findings, non-item/nested-file exclusion, and opt-in archives.
AC#4: task cli:test PKG=./internal/source/... -- -count=1 — PASS; TestLoadAndArchive verified byte-identical archive/unarchive and allocator reconciliation with archived padded/dotted/imported IDs.
Independent verifier e96281fd-c6ee-4b17-b63a-d3fa5be427e3 returned PASS separately for all four criteria; no concrete data-loss defects or excess surface. Its focused suite and git diff --check passed.
Implementation commit a70a9c4 passed task check:staged and task ci on that commit, then fast-forward merged into main. Worktrunk removed the session-owned worktree and branch; its Herdr workspace wHT was inspected (idle shell only), closed by the removal hook, and verified absent. No retained checkout/workspace, no blocker, and no remaining item-scoped step. Nothing pushed.

Review (62249df): Load findings now use the refusal reason or format code with the parse line; unreadable files report code unreadable instead of an error-string prefix. Test asserts finding codes. task ci PASS.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Implemented native source loading, version-guarded atomic mutations, shared cross-worktree ID allocation, and byte-preserving archive moves. Verified all four criteria with the public-API integrity suite, independent verifier PASS, and final implementation-commit task ci. Merged a70a9c4 into main; removed worktree, branch, and Herdr workspace.
<!-- SECTION:FINAL_SUMMARY:END -->
