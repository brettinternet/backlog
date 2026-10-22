---
id: BL-7
title: 'Add the read commands: ls, show, and next'
status: Done
assignee:
  - pi
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 03:46'
labels:
  - cli
  - agents
milestone: m-0
dependencies:
  - BL-4
  - BL-5
  - BL-6
references:
  - docs/design.md
modified_files:
  - internal/cli
  - internal/engine
  - docs/cli.md
  - docs/design.md
priority: high
type: feature
ordinal: 7000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Agents and scripts need fast, stable, short read commands (D19). These freeze the read half of the CLI surface.

## What

- `bl ls [QUERY] [--view V] [--sort S] [--group G] [-n N]`, `bl show ID [--section S]`, and `bl next [QUERY] [-n N] [--explain]`, each with `--json`.
- ID shorthand: `12`, `012`, `BL-12`, and `source:ID` (§7). An ambiguous ID exits 2 and lists the matches. A missing ID exits 3.
- Text output is a compact table sized to the terminal, or 80 columns when not a TTY. Provider text is sanitized: CSI, OSC, and other control sequences are stripped (D21). Put the sanitizer in one shared package.
- Create docs/cli.md with each command, its flags, and its JSON `data` shape, then mark these names frozen in design.md §7.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 CLI tests cover each ID shorthand form, the ambiguous case (exit 2 with matches), and not found (exit 3)
- [x] #2 Golden tests pin the JSON `data` shapes of ls, show, and next, and docs/cli.md documents the same shapes
- [x] #3 `bl next --explain --json` lists included and excluded items with the engine's reasons
- [x] #4 Titles and bodies containing CSI and OSC sequences print without them in text output
- [x] #5 docs/cli.md documents ls, show, and next, and design.md §7 marks those names frozen
- [x] #6 `bl show --json` reports the item's `version`, the value `--if-version` accepts
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
1. Compose project discovery, source reads, query, readiness, and ranking behind engine entry points; do not make CLI handlers access sources directly. Resolve short and source-qualified IDs with explicit ambiguity/not-found errors.
2. Implement ls/show/next, bounded terminal tables, section selection and JSON data shapes. Introduce one shared text sanitizer reused later by the TUI; raw stored content must remain untouched.
3. Pin public JSON and ID behavior with CLI tests; check CSI/OSC stripping and included/excluded explanation output. Run task test and direct commands against a temporary native backlog; create docs/cli.md and freeze read command names in design.md.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Claimed by session 01a161ae-1af8-729e-88c1-f4c1c2cb765a. Delivery authorized: commit, merge main, clean up worktree.

Implemented read-only engine composition, direct detail reads (no whole-backlog parse/ranking for show), ls/show/next, bounded Unicode tables, shared sanitizer, JSON goldens and docs/cli.md. task ci PASS (security, source checks, tests, race, smoke). Independent verifier running. Modified-file scope extension: internal/textutil is the explicitly required shared sanitizer; go.mod promotes its existing x/ansi dependency to direct; internal/model/model.go adds lowercase JSON tags to Ref to match design wire references. No tests removed, skipped or weakened.

AC#1: task cli:test PKG=./internal/cli -- -count=1 passed TestReadIDs (12, 012, full and qualified forms; ambiguity exit 2 with matches; missing exit 3).
AC#2: task cli:test PKG=./internal/cli -- -count=1 passed TestReadJSONGolden for ls/show/next; independent verifier PASS comparing the fixtures to docs/cli.md.
AC#3: task cli:test PKG=./internal/cli -- -count=1 passed TestReadProjectionAndExplain, including candidate, blocker and status reasons, included/excluded items and filtered candidates.
AC#4: task cli:test PKG="./internal/cli ./internal/textutil" -- -count=1 passed TestReadTerminalSafety and TestTerminalControls: CSI/OSC/DCS/control removal, raw JSON preservation and Unicode 80-column rows.
AC#5: independent verifier read docs/cli.md and docs/design.md section 7 and returned PASS; task ci passed documentation/source checks. Flags and JSON shapes documented, read names frozen.
AC#6: task cli:test PKG=./internal/cli -- -count=1 passed TestReadJSONGolden: show item.version equals format.Parse(...).Item().Version, the native expected-version value.
Delivery: 392daad feat(cli): add ls, show, and next fast-forward merged to main; task ci PASS on that committed implementation (security, check, test, race, smoke). Independent verifier edce458b returned PASS for all six ACs, with no concrete defects. No tests deleted, skipped or weakened. Worktrunk receipt matched this session/path/branch; cleanup removed the merged worktree and branch, including its untracked verifier report. No remaining implementation work or blocker; no push performed.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Implemented ls/show/next with native engine reads, ID shorthand and ambiguity errors, versioned JSON goldens, readiness explanations, terminal-safe bounded text, and docs/cli.md. Verified by focused tests, independent verifier PASS for every AC, and task ci on merged commit 392daad. Worktree and branch removed.
<!-- SECTION:FINAL_SUMMARY:END -->
