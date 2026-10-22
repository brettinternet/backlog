---
id: BL-3
title: 'Discover projects, resolve the control root, and load layered config'
status: Done
assignee:
  - '@pi'
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 03:05'
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
- The native milestone catalog and label list from project config (design.md §5): `milestones` entries with `key`, `name`, optional `description`, and `archived`, and a `labels` list of suggestions. Duplicate milestone keys are config errors.
- JSON Schemas in `docs/schemas/` for project and user config. Config errors name the file, line, and key.
- Privileged entries load but stay marked untrusted. BL-29 adds trust.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Tests with real `git worktree add` fixtures resolve the control root from the primary checkout, a linked worktree, a subdirectory, and a bare repository (with a warning), and honor `root: checkout`; root resolution succeeds with `PATH` emptied
- [x] #2 A table test normalizes ssh, https, scp-like, and port-bearing remotes, and falls back to a host-local identity without a remote
- [x] #3 A precedence test sets one key in every layer, resolves the flag value with its provenance, and falls through in D16 order as layers are removed
- [x] #4 Invalid config exits 5 with reason `invalid-config`, naming the file, line, and key
- [x] #5 The design.md §5 example configs validate against the schemas in `docs/schemas/`
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
1. Extend internal/project/config.go and its tests from 77dc4ee. Preserve read-only XDG path resolution, the strict single-document 1 MiB user-file boundary and explicit false overrides for ui.highContrast. Extend the existing docs/schemas/user-config.json instead of adding a competing user schema.
2. Implement direct Git-metadata discovery/control-root resolution, remote normalization and host-local identity; preserve invoking cwd separately from control root. Expand the same config model to D16 layers and per-value provenance, keeping privileged entries inert until BL-29.
3. Extend schemas for project config and current §8 hooks/actions. Validate the design examples and retain file/line/key errors, unknown-field rejection and no config writes during inspection. Exercise real temporary Git/worktree fixtures before clearing PATH to prove discovery does not shell out.
4. Run task cli:test PKG=./internal/project and existing CLI config tests, then task test. Cover old user-file behavior as well as complete precedence, checkout opt-out, bare fallback and remote normalization; update bootstrap-boundary docs to identify what is now implemented.
5. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and deliver the implementation before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented read-only Git discovery, remote identity, D16 YAML overlays with provenance, milestone/label catalogs, strict file/line/key diagnostics, schemas, and inert privileged declarations. task test and task ci pass before delivery. Added README.md/docs/design.md to document shipped behavior; go.mod/go.sum add a test-only JSON Schema validator so the published examples are exercised against real draft-2020-12 schemas (modified-file list deviation). Independent verifier acceptance pass is running. No existing test was removed, skipped, or weakened.

AC#1: task cli:test PKG=./internal/project — PASS; TestDiscoverWorktrees used real git worktree add fixtures for primary, linked, subdirectory, bare and bare-linked, warning, checkout opt-out, with PATH empty during discovery.
AC#2: task cli:test PKG=./internal/project — PASS; TestRemoteIdentity covered ssh, https, scp-like, ports, .git normalization and host-local no-origin fallback.
AC#3: task cli:test PKG=./internal/project — PASS; TestLayerPrecedence checked flag value/provenance and all six D16 layers as higher layers were removed.
AC#4: task cli:test PKG=./internal/cli — PASS; TestConfigInvocation asserted exit 5, invalid-config, filename, line 2 and ui.highContast key. Project tests also cover malformed types, duplicate milestones, unknown keys and 1 MiB boundary.
AC#5: task cli:test PKG=./internal/project — PASS; TestDesignConfigSchemas validated both design.md §5 YAML examples using the draft-2020-12 JSON Schema validator and runtime loader.
Independent verifier b7ad0394-b6c4-48f5-a03b-d1587b3a96a1 returned PASS for AC1–AC5 and found no concrete item-scoped defect; ran project/CLI tests and task ci.
Additional verification: task cli:test PKG=./internal/project -- -fuzz=FuzzConfig -fuzztime=5s passed 15,094 executions. LSP diagnostics clean for config.go, discovery.go, root.go. task check:staged passed. A synthetic credential URL triggered the staged scanner; construction now uses net/url with unchanged normalization coverage, and all checks passed.
Delivery: implementation 6df01e6 merged locally into main as fe95427. task ci passed on both committed implementation and merged main. Concurrent primary-checkout changes were preserved. No push or PR. Cleanup: wt remove feat/bl-3-project-config --foreground --format=json removed owned checkout and branch; Herdr workspace wHQ disappearance verified; no retained task checkout/workspace.
No remaining blocker or resumable implementation step.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Implemented project discovery/control roots, normalized identity, strict layered configuration with provenance, catalogs, schemas and inert privileged entries. Verified all five acceptance criteria independently, passed task ci on committed implementation and merged main, fuzzed YAML loading, and removed the owned worktree/branch/workspace. Commits: 6df01e6, merge fe95427.
<!-- SECTION:FINAL_SUMMARY:END -->
