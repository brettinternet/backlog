---
id: BL-10
title: Import Backlog.md backlogs
status: Done
assignee:
  - '@pi'
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 05:01'
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
- [x] #1 Importing the Worklease v1.9.3 docs/backlog and this repository backlog/ into temp directories produces backlogs that bl check accepts, with a per-field mapping report that loses nothing
- [x] #2 Fixtures cover dotted subtask IDs, zero-padded IDs, lowercase IDs, HTML section markers, comments, ordinals, and dependencies on archived tasks
- [x] #3 --dry-run writes nothing and prints the plan; a non-empty target exits 5
- [x] #4 docs/import.md documents the mapping and the skipped content
- [x] #5 Importing this repository backlog/ yields all five milestones with names and descriptions (m-0 to m-4) and all 33 configured labels in .config/backlog.yaml; a fixture covers an archived milestone, a task that names its milestone by title, and an unresolved milestone in the report
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
1. Implement a reusable direct-file Backlog.md importer, using native format/project APIs and Worklease v1.9.3 format evidence. Preserve metadata, body sections, relationships, ID spelling, ordering, and config; reject collisions and occupied targets. 2. Integrate bl import backlog-md with JSON/human mapping reports and dry-run. Document mappings and skipped content. 3. Verify synthetic data-integrity fixtures plus Worklease v1.9.3 docs/backlog and this repository backlog snapshots. Hum is not a dependency or migration target (owner clarification). 4. Obtain independent acceptance verification, run task ci on implementation commit, merge to main, record evidence, and clean up the owned worktree.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Claimed by Pi session 01a161d3-2aed-729e-88c1-f51cb720f259. Worktree .worktrees/bl-10-import; creation receipt .git/worktrees/bl-10-import/agent-creation.json. Core importer delegated to executor; CLI integration and real-corpus validation in parent. hum corpus commit 6b639b8a519aafbf61f796529a739c48aaf297f7. No decision-register changes intended.

Owner clarification in this session: Hum was only an example Go project, not an import requirement or dependency. Corrected AC1 to use this repository instead of Hum; no source data was changed. Preliminary Hum collision evidence is irrelevant to completion. Retained strict ID collision rejection as required by data integrity.

AC#1: task cli:build; bin/bl import backlog-md /tmp/bl-10-corpora.ONJeZZ/docs/backlog --to /tmp/bl-10-corpora.ONJeZZ/worklease-final --json and corresponding self-backlog/self-final import; git init each target; bin/bl -C each target check --json: both OK, 278 Worklease items (14 drafts), 46 local items, per-file original/output mappings emitted. Sources are snapshots, unchanged. Reports and check results saved under /tmp/bl-10-corpora.ONJeZZ/.
AC#2: task cli:test PKG=./internal/source/backlogmd and task cli:test PKG=./internal/cli (also task ci): PASS. TestImportPreservesBacklogDataAndReportsMappings covers dotted/padded/lowercase IDs, HTML markers, comments, ordinals and archived prerequisites.
AC#3: task cli:test PKG=./internal/cli: TestImportDryRunAndDestinationRefusal PASS, absent destination remains absent during dry-run, import succeeds, occupied destination exits 5 without changing source/output. Package collision/nonempty dry-run test also passes.
AC#4: Read docs/import.md and docs/cli.md; independent verifier PASS confirms documented field/section/config mappings and skipped content match report behavior.
AC#5: self-final import/check above: PASS with five milestones m-0 through m-4 and all 33 labels; package fixture verifies names/descriptions, archived milestone, title rewrite and unresolved milestone report. Independent verifier PASS.
Delivery: implementation 673407e merged to main as f2b4a96. task check:staged PASS; task ci PASS on implementation commit and merged main (ci-final.log, ci-merged.log in temp evidence directory). Independent verifier e436ff55 then corrected-scope continuation d899d36c returned PASS for every AC. Scope correction was owner-directed, not a weakened test. No tests deleted/skipped/weakened; source diff stays in declared scope. Removed verified session-owned worktree and branch with wt remove --foreground; post-remove hook completed. No push/PR. BL-46 migration assumption corrected separately per owner clarification.

Owner follow-up: no real external project is a required import/migration target. The Worklease snapshot used above was historical read-only validation evidence, not a production dependency or a future CI prerequisite. Maintained regression coverage uses repository-local synthetic fixtures; no sibling checkout or external project data is required by the importer or test suite.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added direct-file Backlog.md import with loss-preserving metadata/section/config mapping, dry-run reports, strict collision/destination rejection, milestones and labels. Worklease and repository snapshot imports pass bl check; all focused tests, CI and independent AC verification pass. Merged to main; owned worktree/branch removed. Hum is reference-only per owner.
<!-- SECTION:FINAL_SUMMARY:END -->
