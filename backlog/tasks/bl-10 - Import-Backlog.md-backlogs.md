---
id: BL-10
title: Import Backlog.md backlogs
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 02:39'
labels:
  - import
  - backlog-md
milestone: m-0
dependencies:
  - BL-8
  - BL-9
references:
  - docs/design.md
  - docs/format.md
modified_files:
  - internal/source/backlogmd
  - internal/cli
  - docs/import.md
  - docs/cli.md
priority: high
type: feature
ordinal: 10000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

The owner's repositories use Backlog.md 1.52. Moving them, starting with this one (BL-26), must lose nothing (D4).

## What

`bl import backlog-md [PATH] [--to DIR] [--dry-run]` reads Backlog.md 1.52 files directly, never its CLI:

- `tasks/`, `completed/`, `archive/tasks/`, `drafts/`, and `archive/drafts/`, with config from `backlog/config.yml` or `backlog.config.yml`.
- Fields: status names to config keys, `type`, assignee, labels, priority, milestone, `created_date` and `updated_date` to `created` and `updated` in UTC, `dependencies` to `blocked_by`, `parent_task_id` to `parent`, `ordinal` to `rank` (fractional keys that keep order), `references` and `documentation` to `refs`, and `modified_files` to `files`. Other keys are kept.
- Sections: description, criteria and definition of done (HTML markers removed, checked state kept), plan, notes, final summary to Summary, and the comments block.
- Keep IDs verbatim, upper-cased (`task-12` becomes `TASK-12`), rewriting references to match. Completed tasks become `done`, archived tasks go to `archive/`, and drafts get a `draft` status in the `backlog` category.
- Milestones: each file in `milestones/` and `archive/milestones/` becomes a `milestones` entry in config (id to `key`, title to `name`, the Description section to `description`, and `archived` for archived files). A task milestone that names a milestone by title is rewritten to its key. An unresolved milestone is kept as written and reported.
- Write a matching `.config/backlog.yaml`: statuses, milestones, the `labels` list from `config.yml`, ready labels, and `defaults.dod` from `definition_of_done`.
- The target directory must be empty or absent. Report decisions, docs, and anything else skipped.
- Document the mapping in docs/import.md.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Importing the Worklease v1.9.3 `docs/backlog` and the hum `backlog/` into temp directories produces backlogs that `bl check` accepts, with a per-field mapping report that loses nothing
- [ ] #2 Fixtures cover dotted subtask IDs, zero-padded IDs, lowercase IDs, HTML section markers, comments, ordinals, and dependencies on archived tasks
- [ ] #3 `--dry-run` writes nothing and prints the plan; a non-empty target exits 5
- [ ] #4 docs/import.md documents the mapping and the skipped content
- [ ] #5 Importing this repository's `backlog/` yields all five milestones with names and descriptions (m-0 to m-4) and all 33 configured labels in `.config/backlog.yaml`; a fixture covers an archived milestone, a task that names its milestone by title, and an unresolved milestone in the report
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
1. Read Worklease v1.9.3 internal/queue/backlog*.go and the source format evidence. Implement a reusable Backlog.md decoder/mapping for BL-36, distinct from the native codec; never invoke backlog from production bl code.
2. Build and validate an import manifest before writes: preserve IDs/relationships, all body sections and checks, unknown metadata, and stable ordinal ordering; reject case/ID collisions and non-empty destinations. Report skipped content and dry-run decisions.
3. Use copies of Worklease v1.9.3 docs/backlog and the local hum backlog, never mutate those repositories. Record the exact hum commit used and keep synthetic regression fixtures in this repo. Run import and bl check with field-by-field before/after comparison, plus task test; document mapping and any unavailable corpus as a blocker, not a passing check.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
