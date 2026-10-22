---
id: BL-31
title: 'Run actions from the TUI, CLI, and MCP'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:50'
labels:
  - hooks
  - tui
  - agents
milestone: m-2
dependencies:
  - BL-24
  - BL-27
  - BL-30
references:
  - docs/design.md
  - docs/tui.md
modified_files:
  - internal/hooks
  - internal/tui
  - internal/cli
  - internal/mcp
  - docs/automation.md
priority: medium
type: feature
ordinal: 31000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

The §8 actions: `name`, `run`, optional `cwd` and `key`, `when`, `confirm`, and `detach`. They run from:

- the `!` menu and each action's own key in the TUI
- `bl action NAME ID`
- the MCP `action` tool

Actions share the BL-30 executor: placeholders, value checks, result capture, environment, and depth. Every run ends in an `action.finished` event with outcome and result, so hooks can chain after an action exactly as after a hook. A non-detached action suspends the TUI the way `$EDITOR` does. A detached action returns at once. An action whose placeholders cannot expand for the item (for example `{resource}` without coordination) is shown as disabled with the reason.

Document examples: create a worktree with `wt switch --create {branch}`, start an agent with `worklease run --resource {resource}`, open a Herdr workspace.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Actions appear only for items that match `when`, `confirm` prompts, and keys that conflict with the keymap are reported
- [ ] #2 A PTY test shows a non-detached action suspending and resuming the TUI cleanly; a detached action returns at once
- [ ] #3 The TUI, `bl action`, and MCP `action` run the same executor with identical environment and stdin, and each run appends one `action.finished` event that a hook can chain on
- [ ] #4 An action whose placeholders cannot expand is shown as disabled with the reason and never runs
- [ ] #5 The example actions in docs/automation.md run against fakes in a test
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
1. Extend BL-30's executor rather than add another subprocess path. Add when/capability filtering, confirmation and binding-conflict validation, with the same item JSON/environment for CLI, TUI and MCP.
2. Connect the TUI action menu, command palette and custom keys; reuse BL-21 suspend/resume for attached actions and the detached runner for background actions. Define noninteractive confirmation behavior explicitly and fail closed rather than hang.
3. Verify identical invocation contexts through all three entry points, plus a PTY suspend/resume and detached log check. Run task test and document safe argv-only Worklease/Herdr/branch examples without running real external actions.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.

Note (2026-10-22): this plan predates the D20/D21/D27 redesign of coordination, derived events, results, and chaining. Re-plan against the current description and acceptance criteria before starting.
<!-- SECTION:PLAN:END -->
