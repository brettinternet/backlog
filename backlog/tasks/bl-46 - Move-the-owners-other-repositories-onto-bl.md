---
id: BL-46
title: Move the owner's other repositories onto bl
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 02:28'
labels:
  - import
  - human
milestone: m-4
dependencies:
  - BL-26
  - BL-32
  - BL-33
  - BL-44
references:
  - docs/import.md
modified_files:
  - docs
priority: low
type: task
ordinal: 46000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

The goal is to replace Backlog.md across the owner's projects (design.md §1).

## Needs the owner

This edits other repositories. The owner chooses the order and approves each migration.

## What

For each chosen Backlog.md repository (hum, worklease, tell, noon, and others):

- Run `bl import backlog-md`.
- Port the repository's backlog Taskfile targets and AGENTS.md workflow to `bl`.
- Commit separately in that repository.

Also update the owner's shared agent skills in `~/.agents/skills` (`backlog-source-workflow`, `backlog-md`, `worklease`). In a `bl` project they must select, claim, and start through `bl next --claim --start` or MCP `next`, renew with `bl heartbeat`, and finish with `bl done`. Today's skills treat setting In Progress as the claim and derive `backlog-md:` keys, which never contend with `bl`'s keys (D20). Mixed clients could then take the same task.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Each migrated repository passes `bl check`, and a pre- and post-migration comparison shows nothing lost
- [ ] #2 Each repository's agent workflow runs one full task loop with `bl`
- [ ] #3 With the updated skills, two agent loops started together in one migrated repository claim different items with no contention or `claim-held` error, and no skill sets status as a claim or calls `worklease key` for a `bl` item
- [ ] #4 Updated shared skills preserve Backlog.md routing for unmigrated projects while native projects use the bl-owned resource/claim protocol; each migrated repository records which incompatible launchers remain disabled until updated.
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
1. Obtain owner approval for target repositories, their order, delivery policy and shared skill paths. Inspect each repository's instructions and actual launchers; approval to migrate a repository is not blanket authority over unrelated repositories or global skills.
2. Before enabling native loops in each target, update the approved shared skills with provider-aware dispatch: native projects use bl next --claim --start or MCP next with stable distinct sessions and request-scoped ownership, while unmigrated Backlog.md projects retain their existing provider workflow. Do not globally replace all providers with bl.
3. Use BL-26's already-proven local cutover contract: quiesce writers, export/rehearse/compare, retain a recoverable backup, deliver primary-built wrappers and local instructions, then reload clients. Until a shared skill or launcher is updated and verified, keep it excluded locally rather than permit alternate-key or status-only claims.
4. Verify two distinct-session loops and one real complete native task loop per migrated repository, including done/release and recovery guidance; verify the same skills still dispatch correctly for an unmigrated Backlog.md fixture. Record evidence and separate commits in each authorized repository and the skills' owning repository, with any deviation from this task's local file scope explained. Do not push without explicit permission.
5. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and deliver the implementation before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
