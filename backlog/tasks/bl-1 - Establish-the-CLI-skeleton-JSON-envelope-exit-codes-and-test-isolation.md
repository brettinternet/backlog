---
id: BL-1
title: 'Establish the CLI skeleton, JSON envelope, exit codes, and test isolation'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 02:13'
labels:
  - cli
  - tooling
milestone: m-0
dependencies: []
references:
  - docs/design.md
  - docs/development.md
modified_files:
  - cmd/bl/main.go
  - internal/cli
  - internal/testkit
  - go.mod
  - go.sum
priority: high
type: feature
ordinal: 1000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Every later command, adapter, and test builds on one CLI framework, one JSON envelope, and stable exit codes (design.md D2, D19, §7). Tests must never read the user's real config, state, or cache (development.md#tests).

## What

- Extend the bootstrap `urfave/cli/v3` root already in `internal/cli` (77dc4ee), preserving its injectable Run entry point, version commands and config inspection. Global flags: `--json`/`-j` and `-C DIR`.
- `bl version` and `bl --version` print version and commit; `--json` adds the Go version.
- One envelope type for success and error (§7). A typed error carries a reason, a message, and details. Each reason maps to one exit code 0-7. Usage errors (unknown command or flag, bad arguments) exit 2 and print an error envelope under `--json`.
- Preserve bootstrap behavior: bare `bl` opens the empty TUI on terminals and prints help off-terminal; explicit `bl tui` requires terminals and refuses JSON. BL-16 expands this shell rather than introducing terminal dispatch.
- Extend the existing `internal/testkit.Isolate` helper, which points `HOME` and every `XDG_*` directory at `t.TempDir()` and clears `BL_*` and `WORKLEASE_*`. Add an injectable clock and a reusable capture helper around the existing in-process CLI runner where needed.

Reuse patterns from Worklease v1.9.3 `internal/output`, `internal/reason`, and `internal/testkit`. Do not copy unused helpers.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 `bin/bl --version` and `bin/bl --json version` print build metadata, and `task smoke` passes
- [ ] #2 A table test covers every §7 exit code and reason, and the success and error envelope shapes under `--json`
- [ ] #3 An unknown command or flag exits 2, with usage text normally and an error envelope with reason `usage` under `--json`
- [ ] #4 A test proves that a CLI run under the testkit isolation helper resolves config, state, and cache directories inside the test's temp dir
- [ ] #5 The existing terminal/nonterminal dispatch remains intact while -C resolves the requested invocation directory; a value-taking flag argument containing --json is not mistaken for a JSON switch, and error envelopes include structured details.
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
1. Start from bootstrap commit 77dc4ee: internal/cli.Run, Envelope/Error, version/config show commands, terminal dispatch and internal/testkit.Isolate already exist. Read their tests and extend them rather than introducing a second runner or replacing the working root. Preserve main.buildVersion/main.buildCommit.
2. Complete the remaining contract: -C DIR with invoking-directory handling, structured error details, and all design.md §7 reason/exit mappings (including later reasons session-required and claim-held). Extend argument-aware JSON error detection when adding value-taking flags; a flag value containing --json must not change output mode.
3. Preserve current terminal behavior: bare bl opens the bootstrap TUI on a terminal and prints help off-terminal; explicit tui requires terminals and rejects JSON. Do not restore the obsolete help-only behavior. Add the missing injectable clock and reusable capture helper only where exercised; reuse Isolate without t.Parallel.
4. Verify existing version/config/TUI dispatch and new CLI contract through focused task cli:test PKG=./internal/cli, then task test, task cli:build and task smoke. Check every exit mapping, JSON details, -C and environment isolation; record bootstrap evidence separately from remaining acceptance.
5. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and deliver the implementation before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
