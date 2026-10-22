---
id: BL-29
title: 'Trust privileged config, show effective config, and diagnose with doctor'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - security
  - config
milestone: m-2
dependencies:
  - BL-3
references:
  - docs/design.md
modified_files:
  - internal/project
  - internal/cli
  - docs/cli.md
priority: medium
type: feature
ordinal: 29000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Cloning a repository must never run its hooks, actions, or adapters, or contact its network sources, until the user agrees (D16, §5).

## What

- `bl trust [--show]` records the SHA-256 of the canonical privileged subset for the control root in XDG state. Any change to that subset needs re-trust. Privileged entries in user config are always trusted.
- Untrusted entries show as a TUI banner and in `bl doctor`.
- `bl config show [--json]` prints each effective value with its layer.
- `bl doctor [--json]` checks:
  - config validity
  - trust
  - control-root branch (D5)
  - cache health
  - Worklease presence and version
  - source configuration

  It makes network checks only with `--online`, and exits 5 when any check fails.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 An integration test clones a repository whose committed config has a canary hook, action, and adapter, and no `bl` command runs them before `bl trust`
- [ ] #2 Changing a privileged entry invalidates trust, while key-order-only YAML changes do not
- [ ] #3 `bl config show --json` reports each value's layer, and docs/cli.md documents the shape
- [ ] #4 `bl doctor` reports every check with a status and a fix hint, and exits 5 on failure
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
