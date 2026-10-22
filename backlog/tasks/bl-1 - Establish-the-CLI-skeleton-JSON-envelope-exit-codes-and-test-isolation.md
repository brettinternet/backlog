---
id: BL-1
title: 'Establish the CLI skeleton, JSON envelope, exit codes, and test isolation'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
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

- Replace the placeholder in `cmd/bl` with a `urfave/cli/v3` root in `internal/cli`. Global flags: `--json`/`-j` and `-C DIR`.
- `bl version` and `bl --version` print version and commit; `--json` adds the Go version.
- One envelope type for success and error (§7). A typed error carries a reason, a message, and details. Each reason maps to one exit code 0-7. Usage errors (unknown command or flag, bad arguments) exit 2 and print an error envelope under `--json`.
- Bare `bl` prints help until BL-16 opens the TUI.
- `internal/testkit`: a helper that points `HOME` and every `XDG_*` directory at `t.TempDir()` and clears `BL_*` and `WORKLEASE_*`; an injectable clock; and an in-process CLI runner that captures stdout, stderr, and the exit code.

Reuse patterns from Worklease v1.9.3 `internal/output`, `internal/reason`, and `internal/testkit`. Do not copy unused helpers.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 `bin/bl --version` and `bin/bl --json version` print build metadata, and `task smoke` passes
- [ ] #2 A table test covers every §7 exit code and reason, and the success and error envelope shapes under `--json`
- [ ] #3 An unknown command or flag exits 2, with usage text normally and an error envelope with reason `usage` under `--json`
- [ ] #4 A test proves that a CLI run under the testkit isolation helper resolves config, state, and cache directories inside the test's temp dir
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
