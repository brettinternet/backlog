---
id: BL-31
title: 'Run actions from the TUI, CLI, and MCP'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
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
  - docs
priority: medium
type: feature
ordinal: 31000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

The §8 actions: `name`, `run`, optional `key`, `when`, `confirm`, and `detach`.

- The `!` menu and each action's own bound key in the TUI.
- `bl action NAME ID`.
- The MCP `action` tool.

A non-detached action suspends the TUI the way `$EDITOR` does. A detached action returns at once and logs its result. Actions share the hook executor and environment rules.

Document examples: start an agent with `worklease run`, open a Herdr workspace, and create a branch named from `yb`.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Actions appear only for items that match `when`, `confirm` prompts, and keys that conflict with the keymap are reported
- [ ] #2 A PTY test shows a non-detached action suspending and resuming the TUI cleanly; a detached action returns at once and logs its result
- [ ] #3 The TUI, `bl action`, and MCP `action` run the same executor with identical environment and stdin
- [ ] #4 The documented example actions are in docs/
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
