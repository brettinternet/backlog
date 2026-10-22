---
id: BL-4
title: Load and write native items with a shared ID allocator and atomic writes
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - source
  - format
  - git
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
- [ ] #1 A multi-process test creates 400 items concurrently from two linked worktrees and gets 400 distinct IDs
- [ ] #2 Apply with a stale expected version returns reason `conflict` and leaves the file unchanged; a failed write leaves the original intact and no temp files behind
- [ ] #3 Load reports per-file parse errors without aborting, ignores non-item files, and loads `archive/` only on request
- [ ] #4 Archive and unarchive move the file, keep its bytes, and keep the allocator from reusing the ID
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
