---
id: BL-19
title: Manage views as tabs and persist UI state
status: Done
assignee:
  - '@pi-01a163f2'
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 14:26'
labels:
  - tui
  - config
milestone: m-1
dependencies:
  - BL-18
  - BL-17
references:
  - docs/tui.md
  - docs/design.md
modified_files:
  - internal/tui
  - internal/project
priority: high
type: feature
ordinal: 19000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Backlog.md puts each view behind a separate command. In `bl`, views are tabs in one program, and reopening restores exactly where you were (tui.md#state).

## What

- Built-in views, then project and user saved views, as tabs. Each view has its own query, sort, group, layout, columns, and selection.
- `ctrl-s` saves the current view to user config by default, or to project config on request, as a minimal YAML edit.
- Per-project UI state in XDG state: last view, query, sort, group, layout, selection, detail tab, and split width.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Views from built-ins, project config, and user config appear in the documented order, and switching keeps each view's selection
- [x] #2 `ctrl-s` writes the view with a minimal diff, and it is present after a restart
- [x] #3 Quitting and reopening restores view, query, sort, group, layout, selection, detail tab, and split width (temp XDG state)
- [x] #4 A corrupt state file is ignored with a warning
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
1. Add a separate ordered saved-view catalog without changing D16 replacement semantics for general settings; built-ins, project declarations, user declarations, then per-project user declarations. Keep per-tab query/layout/columns and stable-ref selection.
2. Add ctrl-s name/destination prompt, minimal YAML entry editing with locking/atomic writes on a command, and restore/save project session state outside the update loop at startup/shutdown. Unavailable layouts render as list while retaining their setting; expose keyboard split resizing.
3. Add focused data-integrity and restart/selection/corrupt-state regression tests, update specs/schema, and exercise a real PTY restart.
4. Obtain independent AC verification, run task ci on the committed implementation, merge locally, finalize task evidence and remove the owned worktree.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Claimed by pi-01a163f2. Worktree .worktrees/bl-19-views, branch bl-19-views; creation receipt .git/worktrees/bl-19-views/agent-creation.json. CLI wiring and schema/spec changes beyond the original modified-file list are required for startup/shutdown persistence and the columns configuration vocabulary.

AC#1: task cli:test PKG="./internal/project ./internal/tui ./internal/cli" passed; TestSavedViewCatalogOrder and TestViewsRestoreSession verify built-in/project/user order and stable per-tab selections after reordering. Independent verifier 03c0a9f2 returned PASS.
AC#2: task cli:test PKG="./internal/project ./internal/tui ./internal/cli" passed; TestSaveViewPreservesConfig covers LF/CRLF and block/flow/empty/missing sequences; TestSaveViewRestart verifies ctrl-s user/project saves and restart presence. python3 /tmp/bl19-verify.py passed a real-terminal save preserving unrelated config bytes.
AC#3: TestViewsRestoreSession passed, including the initial empty-projection race caught and fixed by the real PTY check. python3 /tmp/bl19-verify.py passed shutdown/restart restoration of active view, query/sort/group, stable item selection, detail tab and split width; model regression also verifies retained layout/columns. Captures retained at /var/folders/jp/1mwx72h172955139pth4h8800000gn/T/bl19-acceptance-rqgh0hfb.
AC#4: TestViewsRestoreSession passed malformed/version/query/tab state rejection and visible warning; python3 /tmp/bl19-verify.py passed corrupt-state startup in a real terminal.
Implementation commit: 6f839a0ac6a6efee577d2462d58c72f8f7317eed. task check:staged and task ci passed on this commit (security, check, tests, race, smoke); LSP diagnostics clean. Independent verifier returned PASS for all four criteria. No tests deleted/skipped/weakened; navigation golden updated only for advertised keys.
Integration pending: primary checkout has unrelated uncommitted edits in internal/tui/model.go, internal/tui/query.go, internal/tui/testdata/detail-120.golden. git merge --ff-only bl-19-views refused to overwrite model.go; no edits discarded or stashed. User chose to wait for their owner to commit. Unblock condition: these paths have no uncommitted edits; next step merge into main, resolve overlap preserving both implementations, rerun task ci, then finalize and clean owned worktree. Workbench plugin unavailable; Herdr workspace inspection/cleanup still pending.

Integration completed: owner committed the unrelated edits as a65ce91, followed by backlog review commit 5784bd6. Merge 46c0453a9ef8319bd6364f16b94bbc6f091a7b91 preserves both the readable-list-width fix and saved split sizing; the single model.go merge conflict was resolved accordingly. task check:staged, focused tests, and task ci all passed on the final merged implementation. python3 /tmp/bl19-verify.py passed again against the merged primary binary; captures at /var/folders/jp/1mwx72h172955139pth4h8800000gn/T/bl19-acceptance-4yjs47m0.
Cleanup complete: verified session receipt/path/branch and integration ancestry, inspected Herdr workspace wJP (one zsh pane), then wt remove bl-19-views --foreground --format=json --yes removed the checkout and branch. wt list now shows only primary; herdr workspace list confirms wJP disappeared. No retained task worktree/workspace. Temporary acceptance fixtures remain as evidence only. No blocker or next resumable step; all delivery obligations satisfied. No push or PR performed.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added ordered saved-view tabs, stable per-tab selection, ctrl-s user/project minimal config saves, and project-keyed XDG session restoration with corrupt-state warnings. Implemented in 6f839a0 and merged locally as 46c0453; task ci, independent AC verification, and a real-terminal save/restart/corruption round trip passed. Worktree, branch and Herdr workspace removed.
<!-- SECTION:FINAL_SUMMARY:END -->
