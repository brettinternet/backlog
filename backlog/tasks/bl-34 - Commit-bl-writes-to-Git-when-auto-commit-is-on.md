---
id: BL-34
title: Commit bl writes to Git when auto-commit is on
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:46'
labels:
  - git
milestone: m-2
dependencies:
  - BL-8
  - BL-16
references:
  - docs/design.md
modified_files:
  - internal/engine
  - internal/project
  - docs
priority: low
type: feature
ordinal: 34000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

`git.autoCommit` (D9), off by default:

- Commit only the paths `bl` wrote, with pathspec-limited commits, so other staged and unstaged changes are left alone.
- Skip with a warning during a merge, rebase, or cherry-pick, or on a detached HEAD.
- One commit per CLI mutation command. The TUI combines consecutive edits into one commit after 2 s idle and on quit.
- Never pass `--no-verify`. A failing Git hook is reported, and the file write stands.
- Message format: `docs(backlog): <verb> <ID> <title>`.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A test with other staged and unstaged changes shows only `bl`-written paths committed and everything else left exactly as it was
- [ ] #2 Merge, rebase, cherry-pick, and detached HEAD each skip with a warning
- [ ] #3 A failing Git hook is reported, and the file write remains
- [ ] #4 The default is off, and docs describe the message format
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
1. Add opt-in post-write Git commit handling using the exact written-path set from engine results. Preserve unrelated index/worktree changes, including user changes already present on a path bl edits; refuse unsafe cases rather than stage blindly.
2. Detect merge/rebase/cherry-pick/detached states and report skips. Serialize commit work with repository mutations; implement TUI idle batching with an injected timer off Update and flush on quit without bypassing Git hooks.
3. Use temporary repositories to verify staged/unstaged preservation, same-path edits, hook failure and skip states. Run task test and task race; document that successful file writes survive commit failure and autoCommit defaults off.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
