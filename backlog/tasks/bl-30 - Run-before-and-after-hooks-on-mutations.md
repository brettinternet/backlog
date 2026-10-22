---
id: BL-30
title: 'Run hooks on events with results, chaining, and checked placeholders'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:51'
labels:
  - hooks
  - security
milestone: m-2
dependencies:
  - BL-6
  - BL-20
  - BL-28
  - BL-29
  - BL-32
references:
  - docs/design.md
modified_files:
  - internal/hooks
  - internal/engine
  - internal/project
  - internal/query
  - docs/automation.md
priority: medium
type: feature
ordinal: 30000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Hooks are how `bl` connects to worktrees, Git, agents, notifications, and anything else, without `bl` knowing those tools (D18, D27, §8).

## What

The §8 hooks: `name`, `on` (any event type), `if` (an item query plus the event terms from BL-6), `phase`, `run`, `cwd`, and `timeout`.

- `before` hooks only for `item.*` mutation events. They run synchronously with the proposed event on stdin. A non-zero exit or a timeout vetoes with exit 5 and the first stderr line.
- `after` hooks run without blocking, with bounded concurrency per event. The CLI hands them to a detached `bl` runner process, so commands return at once. The process that appended the event runs them (BL-28).
- Every after hook ends in a `hook.finished` event with outcome, exit code, duration, and `result`. The result comes from the `BL_RESULT` file (one JSON object of at most 8 KiB). If that file is empty and stdout is a single JSON object of at most 8 KiB, stdout is the result. Hooks chain with `on: hook.finished` and `if: "hook:NAME outcome:ok"`.
- Placeholders in argv and `cwd`: `{id}`, `{ref}`, `{source}`, `{url}`, `{root}`, `{branch}`, `{resource}`, and `{result.KEY}`. Expanded values must have no control characters, no leading `-`, and at most 1 KiB, otherwise the outcome is `refused`. `{resource}` comes from the coordination seam (BL-32) and is unavailable without it.
- `cwd` defaults to the directory the originating command ran in, carried along the chain, or the control root for sync-originated chains.
- Depth: events from a hook carry `cause` and depth + 1. `bl` calls inside hooks inherit `BL_HOOK_DEPTH`. Hooks don't run for events deeper than 3.
- No shell, ever (D21). A minimal environment with the §8 variables and credentials removed.
- docs/automation.md documents hooks with tested examples: `notify-ready` on `item.readiness`, release on done, and the §8 `agent` hook chained after the `work` action (BL-31 adds the action side).
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A vetoing before hook makes the command exit 5 with the first stderr line and write nothing; a timeout also vetoes
- [ ] #2 After hooks add no measurable delay to CLI exit beyond spawning, and each produces one `hook.finished` event with outcome, exit code, duration, and result from `BL_RESULT` or JSON stdout
- [ ] #3 A two-hook chain runs against a fake `wt` and a fake `worklease`: the second hook runs in `{result.path}` from the first hook's JSON stdout, with the item's `{branch}` and `{resource}`
- [ ] #4 A placeholder value with a leading `-` or a control character, or a missing `{result.KEY}`, produces outcome `refused` and runs nothing; shell metacharacters pass literally
- [ ] #5 A hook chain that loops stops at depth 3, through both `hook.finished` chaining and `bl` calls inside hooks
- [ ] #6 A canary test proves hooks receive only the documented variables and no credential variables
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
1. Insert before hooks before any source write/optimistic publish and after hooks only after confirmed success/events. Reuse query changed-field context and the trust gate.
2. Implement argv-only execution with placeholder validation, a minimal allowlisted environment, stdin context, bounded timeout/concurrency and depth propagation. Keep CLI detached-runner lifecycle/logging explicit so child completion cannot corrupt command stdout or require the parent process.
3. Verify veto/timeout write-nothing behavior, after-hook responsiveness/logging, credential canaries, depth 4 refusal and literal shell metacharacters. Run task test and task race; document runner/log/error behavior and TUI failure reporting.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.

Note (2026-10-22): this plan predates the D20/D21/D27 redesign of coordination, derived events, results, and chaining. Re-plan against the current description and acceptance criteria before starting.
<!-- SECTION:PLAN:END -->
