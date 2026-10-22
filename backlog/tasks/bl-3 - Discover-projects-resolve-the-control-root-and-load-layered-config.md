---
id: BL-3
title: 'Discover projects, resolve the control root, and load layered config'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:54'
labels:
  - config
  - git
milestone: m-0
dependencies:
  - BL-1
references:
  - docs/design.md
modified_files:
  - internal/project
  - docs/schemas
  - internal/cli
priority: high
type: feature
ordinal: 3000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Every worktree and agent must see one live backlog (D5), each repository needs its own sources and config (§5), and project identity keys caches, trust, events, and claims.

## What

- `internal/project`: walk up to the Git root, then resolve the primary worktree by reading `.git`, `gitdir`, and `commondir` files without spawning `git`. A bare repository falls back to the invoking checkout with a warning. `root: checkout` opts out.
- Project identity: normalize the `origin` URL read from the Git config (ssh, https, scp-like, ports, `.git` suffix) to `host/owner/repo`. Without a remote, fall back to the control-root path marked host-local.
- Layered config in D16 order: built-ins, user `$XDG_CONFIG_HOME/backlog/config.yaml`, project `.config/backlog.yaml`, user `projects.<id>` overrides, `BL_*` environment, flags. Record which layer set each value so BL-29 can print it.
- Without a project config, detect Backlog.md (`backlog/config.yml`, `backlog.config.yml`) and Beads (`.beads/`) and report them.
- JSON Schemas in `docs/schemas/` for project and user config. Config errors name the file, line, and key.
- Privileged entries load but stay marked untrusted. BL-29 adds trust.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Tests with real `git worktree add` fixtures resolve the control root from the primary checkout, a linked worktree, a subdirectory, and a bare repository (with a warning), and honor `root: checkout`; root resolution succeeds with `PATH` emptied
- [ ] #2 A table test normalizes ssh, https, scp-like, and port-bearing remotes, and falls back to a host-local identity without a remote
- [ ] #3 A precedence test sets one key in every layer, resolves the flag value with its provenance, and falls through in D16 order as layers are removed
- [ ] #4 Invalid config exits 5 with reason `invalid-config`, naming the file, line, and key
- [ ] #5 The design.md §5 example configs validate against the schemas in `docs/schemas/`
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
1. Read design.md D5, D16, D22 and §5. Implement discovery/control-root resolution directly from Git metadata, then remote normalization and host-local identity; preserve the invoking cwd separately from the control root.
2. Implement layered config and per-value provenance with actionable file/line/key errors. Load privileged entries as inert data; do not execute helpers, adapters, hooks, or network requests before BL-29 trust enforcement.
3. Create project/user schemas covering current design.md §8 actions/hooks (cwd, subscriptions, result placeholders, timeout and confirmation), and validate the design examples. Use temporary real Git/worktree fixtures created before clearing PATH to verify no runtime git subprocess; exercise bare fallback, checkout opt-out, precedence, and normalization through task test.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
