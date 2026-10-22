---
id: BL-29
title: 'Trust privileged config, show effective config, and diagnose with doctor'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:54'
labels:
  - security
  - config
milestone: m-2
dependencies:
  - BL-3
  - BL-16
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

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Implement canonical privileged-subset hashing and trust storage bound to the control root, preserving provenance so user config is trusted while committed executable/network entries are inert. Include executable behavior in cwd, subscriptions, conditions, timeouts and result/argv expansion rules in the trusted subset; changing any such config must require re-trust.
2. Implement config show with provenance and doctor with offline checks/fix hints, optional online probes, and safe unavailable results for integrations not installed. Add the TUI trust banner through the existing shell.
3. Use canary commands and network counters to prove no pre-trust execution, invalidation on semantic change, and key-order stability. Run task test, direct trust/config/doctor in an isolated repository, and update docs/cli.md; later extension tasks must reuse this gate.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
