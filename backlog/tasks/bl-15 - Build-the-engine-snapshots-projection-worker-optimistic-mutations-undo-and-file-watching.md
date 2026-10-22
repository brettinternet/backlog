---
id: BL-15
title: >-
  Build the engine: snapshots, projection worker, optimistic mutations, undo,
  and file watching
status: Done
assignee:
  - '@pi'
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 07:35'
labels:
  - engine
  - perf
milestone: m-1
dependencies:
  - BL-5
  - BL-6
  - BL-8
  - BL-11
  - BL-12
references:
  - docs/design.md
modified_files:
  - internal/engine
  - internal/source/native
  - go.mod
  - go.sum
priority: high
type: feature
ordinal: 15000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

The TUI must never wait (D14, D15). That needs immutable snapshots, projections off the UI loop, optimistic edits, and change detection that scales on macOS (§11 hazards 1, 4, and 6).

## What

`internal/engine` store:

- Immutable snapshots with per-item versions and structural sharing.
- A per-source sync worker. Only the native source exists now, but nothing may assume a single source.
- A projection worker that turns query, sort, and group into prepared rows. Latest request wins, and older ones are cancelled.
- An async mutation pipeline on top of BL-8. Edits apply optimistically and stay pending until the source confirms; failures revert, and conflicts show both versions. Session undo and redo replay inverse mutations.
- Native Watch: watch directory entries only, run a low-rate stat sweep for in-place edits, and fall back to polling on `EMFILE`.
- Subscriptions through latest-value mailboxes, so a slow consumer never blocks the engine.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 A filter projection over 50k items runs off the caller goroutine, newer requests cancel older ones, and the benchmark result is recorded against the 80 ms budget
- [x] #2 With a fake source: an edit shows as pending in the next snapshot, a failure reverts with the error recorded, and a conflict exposes both versions with no automatic retry
- [x] #3 Undo and redo restore the prior file bytes apart from `updated`
- [x] #4 At 10k items, an external edit, create, delete, or rename appears in a snapshot within 150 ms; open file descriptors stay bounded on macOS; an injected `EMFILE` switches to polling
- [x] #5 A blocked subscriber does not stall the engine, and `task race` passes
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
1. Add an immutable snapshot store with structurally shared item records, independent serialized source workers, latest-value subscriptions, and a cancellable latest-wins projection worker. Keep public UI-facing requests constant-time and nonblocking.
2. Reuse BL-8 validation and the native codec through an optional source mutation preparer, preserving exact inverse bytes. Publish pending before writes, retain rollback/conflict diagnostics, reject overlapping same-item edits, and run undo/redo through version-checked mutations.
3. Add directory-only platform watches (kqueue/inotify), stat reconciliation for in-place edits, and EMFILE polling fallback. Reuse unchanged parsed native items during loads.
4. Verify subtle state transitions, cancellation, source isolation, native byte restoration and watcher fallback; benchmark deterministic 50k projections and 10k external updates. Obtain one independent verifier pass, run task ci on the committed implementation, merge to main, finalize task evidence, and remove the owned worktree.
Modified files: internal/engine, internal/source/native, internal/source/source.go (optional preparation contract), internal/query/projection.go (context cancellation), go.mod/go.sum if needed, docs/tui.md (engine contract and watcher guarantees).
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented immutable structurally shared records, independent source workers, optimistic pending snapshots before queued source I/O, conflict diagnostics and byte-preserving inverse history with external-version boundaries. Added directory-only kqueue/inotify watches and EMFILE polling fallback; source loads reuse stat-validated parsed items. Scope additions from original modified-file list: internal/source/source.go adds the optional lossless Preparer contract; internal/query/projection.go adds cooperative cancellation; docs/tui.md documents engine APIs and watcher guarantees; go.mod promotes already-present x/sys to direct for native watches. No existing tests removed or weakened.
Measurement: task cli:test PKG=./internal/engine -- -run ^$ -bench ^BenchmarkEngine$ -benchtime=20x -count=1 -timeout=3m on Apple M5 Max passed: 50k warm filter p95 24.45 ms (80 ms budget), initial snapshot readiness+projection 153.1 ms separately; 10k atomic edit/create/delete/rename p95 55.47/99.77/45.06/54.46 ms (150 ms budget). Native directory-event guarantees exclude macOS in-place edits, which are detected by the specified low-rate 2s stat sweep. An exploratory cold SQLite/FTS fixture load timed out before projection after 20m; this existing cache-population path is not the in-memory engine filter benchmark. Claim expired during that exploratory run and was reacquired in the same session, with no competing owner reported. Independent verifier 3e34f2c9 is running; next: finish validation, fix concrete findings, commit and run final-commit CI, merge/finalize/cleanup.

AC#1: task cli:test PKG=./internal/engine -- -run ^$ -bench ^BenchmarkEngine$ -benchtime=20x -count=1 -timeout=3m PASS: 50k warmed filter p95 24.45 ms <=80 ms; TestLatestProjectionAndCancellation PASS in focused suite. Initial snapshot readiness+projection was 153.1 ms and is reported separately, not represented as warmed latency.
AC#2: task cli:test PKG="./internal/engine ./internal/source/native ./internal/query" -- -count=1 PASS: TestOptimisticFailureAndConflict covers pending, rollback/error, both conflict versions and exactly one Apply; TestQueuedEditsArePendingBeforeSourceIO proves pending publication before preparation and queued source I/O.
AC#3: task cli:test PKG="./internal/engine ./internal/source/native ./internal/query" -- -count=1 PASS: TestNativeUndoRedoAndExternalHistoryGap restores CRLF/comments/unknown fields byte-for-byte except updated and rejects crossing external-version history gaps; create/archive undo/redo and shutdown also pass.
AC#4: task cli:test PKG=./internal/engine -- -run ^$ -bench ^BenchmarkEngine$ -benchtime=20x -count=1 -timeout=3m PASS: 10k atomic edit/create/delete/rename p95 55.47/99.77/45.06/54.46 ms <=150 ms after directory signals. task cli:test PKG=./internal/source/native -- -run ^TestDirectoryWatchDescriptorBound$ -v -count=1 PASS: descriptor counts 5 before, 6 during, 5 after at 10k. TestDirectoryWatchAndPollingFallback PASS with injected EMFILE and in-place sweep. As design section11 specifies, macOS in-place edits use a low-rate sweep rather than a nonexistent directory event.
AC#5: task race PASS in independent verification, and task ci PASS on implementation commit 97f0ece and merged main commit 69e6470, including full race suite; TestSourceIsolationCoverageAndBlockedSubscriber proves an undrained mailbox and blocked source cannot stall other work.
Independent verifier 3e34f2c9-a998-408c-b63f-1a99e0a274fe returned PASS separately for AC#1 through AC#5, with no acceptance blockers. It independently measured warm filter p95 26.77 ms and 10k edit/create/delete/rename p95 31.95/48.88/28.24/43.48 ms. Residual measurement: first 50k projection 227.1 ms including initial readiness; warmed budget passes.
Delivery complete: implementation 97f0ece merged to main as 69e6470, preserving concurrent BL-14 changes. task check:staged PASS before implementation commit; task ci PASS on final merged source commit. No tests deleted, skipped or weakened. docs/tui.md reflects the delivered engine contract. Required modified-file deviations are justified above. Worktrunk removed the verified session-owned .worktrees/feat-bl-15-engine checkout and feat/bl-15-engine branch; its exact Herdr workspace wJD had only an idle zsh pane and disappeared after post-remove. wt list now contains only primary main; no task-owned checkout/workspace remains. No push or PR performed. No remaining blocker or resumable implementation step.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Implemented the async engine with immutable snapshots, latest-wins projections, optimistic edits/conflicts, lossless undo/redo, and directory-only native watching with polling fallback. All five acceptance criteria independently PASS; task ci passes on merged main 69e6470. Warm 50k filter p95 24.45 ms; all 10k file-operation p95 values <100 ms. Implementation 97f0ece merged; owned branch/worktree/Herdr workspace removed. No push.
<!-- SECTION:FINAL_SUMMARY:END -->
