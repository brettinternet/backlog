---
id: BL-11
title: Cache items in SQLite with stat validation and full-text search
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - cache
  - perf
milestone: m-0
dependencies:
  - BL-4
  - BL-6
references:
  - docs/design.md
modified_files:
  - internal/cache
  - internal/source/native
  - internal/query
  - go.mod
  - go.sum
priority: high
type: feature
ordinal: 11000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Warm `bl ls` at 10k items must take at most 150 ms, and TUI first paint at most 100 ms (§11). Reparsing every file on every run cannot meet that (D23). `text:` needs indexed search.

## What

`internal/cache` per D23: `modernc.org/sqlite` in WAL mode with a busy timeout, in XDG cache keyed by project ID.

- Native rows keyed by path, validated by size, mtime in nanoseconds, inode, and ctime. Only changed files are reparsed.
- An FTS5 index over title and body backs `text:`.
- A schema version. A mismatched or corrupt cache is rebuilt with a warning, never a failure.
- Single-flight refresh per source across processes, with a lock file next to the database.

The cache must never be needed for correctness (D22). Deleting it only costs time.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A warm load of an unchanged directory opens no item files (counter assertion) and returns items equal to a cold load
- [ ] #2 Edits, additions, deletions, renames, and a same-size edit within the same second are all detected
- [ ] #3 Two processes refreshing at once parse each changed file once, and neither fails with a busy error
- [ ] #4 A corrupt or schema-mismatched cache is rebuilt and the command succeeds
- [ ] #5 `text:` queries return the same results through FTS as through the in-memory evaluator
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
