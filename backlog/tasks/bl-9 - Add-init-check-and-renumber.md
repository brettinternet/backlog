---
id: BL-9
title: 'Add init, check, and renumber'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
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
- `bl renumber FROM TO` renames the file and rewrites every reference (`blocked_by`, `parent`, `related`, `duplicate_of`, and `source:ID` forms) in active and archived items. It refuses when TO exists.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 `bl init` in a fresh repository creates a config and directory that `bl check` accepts; a second run exits 5 and changes nothing
- [ ] #2 Fixture files trigger every docs/format.md finding code with path and line; exit is 5 with errors and 0 otherwise; `--json` lists the findings
- [ ] #3 `bl check --fix` applies only the fixes docs/format.md allows
- [ ] #4 `bl renumber` rewrites every reference form in active and archived items and refuses an existing target
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
