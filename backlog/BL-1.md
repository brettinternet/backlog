---
id: BL-1
title: Establish the CLI skeleton, JSON envelope, exit codes, and test isolation
status: done
type: feature
priority: high
assignees: ["@brett"]
labels: [cli, tooling, reviewed]
milestone: m-0
rank: a0505050505050505
refs: [docs/design.md, docs/development.md]
files: [cmd/bl/main.go, internal/cli, internal/testkit, go.mod, go.sum]
created: 2026-10-22T00:50:00Z
updated: 2026-10-30T05:42:13Z
---

## Why

Every later command, adapter, and test builds on one CLI framework, one JSON envelope, and stable exit codes (design.md D2, D19, §7). Tests must never read the user's real config, state, or cache (development.md#tests).

## What

- Extend the bootstrap `urfave/cli/v3` root already in `internal/cli` (77dc4ee), preserving its injectable Run entry point, version commands and config inspection. Global flags: `--json`/`-j` and `-C DIR`.
- `bl version` and `bl --version` print version and commit; `--json` adds the Go version.
- One envelope type for success and error (§7). A typed error carries a reason, a message, and details. Each reason maps to one exit code 0-7. Usage errors (unknown command or flag, bad arguments) exit 2 and print an error envelope under `--json`.
- Preserve bootstrap behavior: bare `bl` opens the empty TUI on terminals and prints help off-terminal; explicit `bl tui` requires terminals and refuses JSON. BL-16 expands this shell rather than introducing terminal dispatch.
- Extend the existing `internal/testkit.Isolate` helper, which points `HOME` and every `XDG_*` directory at `t.TempDir()` and clears `BL_*` and `WORKLEASE_*`. Add an injectable clock and a reusable capture helper around the existing in-process CLI runner where needed.

Reuse patterns from Worklease v1.9.3 `internal/output`, `internal/reason`, and `internal/testkit`. Do not copy unused helpers.

## Acceptance criteria

- [x] #1 `bin/bl --version` and `bin/bl --json version` print build metadata, and `task smoke` passes
- [x] #2 A table test covers every §7 exit code and reason, and the success and error envelope shapes under `--json`
- [x] #3 An unknown command or flag exits 2, with usage text normally and an error envelope with reason `usage` under `--json`
- [x] #4 A test proves that a CLI run under the testkit isolation helper resolves config, state, and cache directories inside the test's temp dir
- [x] #5 The existing terminal/nonterminal dispatch remains intact while -C resolves the requested invocation directory; a value-taking flag argument containing --json is not mistaken for a JSON switch, and error envelopes include structured details.

## Definition of done

- [x] #1 task ci passes on the final commit
- [x] #2 Every checked acceptance criterion has an AC#N evidence line in Implementation Notes naming the command and its result
- [x] #3 Substantial changes had an independent verifier pass that returned PASS for every acceptance criterion
- [x] #4 The diff stays within the task's modified-file list, or Implementation Notes justify the deviation
- [x] #5 No test was deleted, skipped, or weakened
- [x] #6 User-visible behavior changes are reflected in README.md or docs/ in the same change

## Plan

1. Start from bootstrap commit 77dc4ee: internal/cli.Run, Envelope/Error, version/config show commands, terminal dispatch and internal/testkit.Isolate already exist. Read their tests and extend them rather than introducing a second runner or replacing the working root. Preserve main.buildVersion/main.buildCommit.
2. Complete the remaining contract: -C DIR with invoking-directory handling, structured error details, and all design.md §7 reason/exit mappings (including later reasons session-required, claim-held and run-mismatch). Extend argument-aware JSON error detection when adding value-taking flags; a flag value containing --json must not change output mode.
3. Preserve current terminal behavior: bare bl opens the bootstrap TUI on a terminal and prints help off-terminal; explicit tui requires terminals and rejects JSON. Do not restore the obsolete help-only behavior. Add the missing injectable clock and reusable capture helper only where exercised; reuse Isolate without t.Parallel.
4. Verify existing version/config/TUI dispatch and new CLI contract through focused task cli:test PKG=./internal/cli, then task test, task cli:build and task smoke. Check every exit mapping, JSON details, -C and environment isolation; record bootstrap evidence separately from remaining acceptance.
5. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and deliver the implementation before making dependent work eligible. Do not push or publish without explicit authorization.

6. Resolve -C to an absolute invocation directory without changing process cwd; expose it in config show alongside user paths. Extend README and design §7 (outside the original file list) to document this observable contract and the complete reason table. No production clock is read by the bootstrap, so do not introduce unused clock plumbing; add clock injection only when a timed operation exercises it, as required by the existing plan.

## Notes

Implemented and fast-forward merged c55ee97 (feat(cli): complete output and invocation contract) into main. Extended bootstrap root/envelope rather than replacing them. -C resolves and validates an invocation directory without process chdir; config show reports directory. JSON errors always include object details. All documented exit reasons are mapped, including session-required, claim-held, run-mismatch. Reused Worklease v1.9.3 injected-stream capture pattern. No production operation reads time, so no unused clock helper/plumbing was copied.
AC#1: task smoke and task ci on c55ee97 PASS; bin/bl --version and bin/bl --json version report version, commit c55ee97, and JSON Go metadata.
AC#2: task cli:test PKG=./internal/cli and task ci PASS; TestExitReasons covers all documented reason mappings (1-7 plus unknown fallback), TestMachineOutput covers code 0 and success/error JSON envelopes.
AC#3: task cli:test PKG=./internal/cli PASS; unknown command/flag exit 2 with human usage or reason usage JSON. Independent direct bin/bl --unknown and bin/bl --json --unknown checks PASS.
AC#4: task cli:test PKG=./internal/cli PASS; TestConfigInvocation proves config, state and cache paths stay under testkit.Isolate temp home.
AC#5: task cli:test PKG=./internal/cli PASS; TestInvocationDirectory checks relative/absolute -C, unchanged process cwd, -C values containing --json and structured path/flag details. Independent PTY runs of bare bl and bl tui PASS; existing nonterminal/JSON dispatch checks PASS.
Independent verifier 51b75adb-89e0-4567-9f90-e4c005d38a2d returned PASS for AC1-AC5 with no concrete item-scoped defects (one general pass). LSP diagnostics clean on affected CLI files; task test, task smoke, task check:staged, task ci including race/security PASS. No tests deleted, skipped or weakened. README.md and docs/design.md were changed outside initial modified-file list to satisfy required user-visible documentation and publish exact reason/details/directory contract. No design decisions altered. Delivery complete locally; no push requested. Next step: none.

Review (6fcd2be): jsonRequested now derives value-taking flags from the command tree instead of a hand-kept list; test covers a subcommand value flag. task ci PASS.

## Summary

Completed the CLI invocation and JSON error contract in c55ee97, merged into main. All five acceptance criteria independently PASS; task ci passes on the implementation commit. Version/TUI bootstrap behavior preserved; documented directory and error semantics.
