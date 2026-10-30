---
id: BL-8
title: 'Add the write commands: new, set, start, done, note, comment, ac, edit, and archive'
status: done
type: feature
priority: high
assignees: ["@brett"]
labels: [cli, agents, reviewed]
milestone: m-0
blocked_by: [BL-6, BL-7]
rank: a2828282828282828
refs: [docs/design.md, docs/format.md]
files: [internal/cli, internal/engine, docs/cli.md, docs/design.md]
created: 2026-10-22T00:50:00Z
updated: 2026-10-30T05:44:46Z
---

## Why

Agents must edit the backlog with short commands instead of rewriting documents (D19). This builds the synchronous mutation path that the engine, TUI, hooks, and events reuse.

## What

- An engine mutation pipeline with no TUI concerns: validate against config and capabilities, apply with the expected version, return the new item. BL-15 adds optimism and undo on top of it, and BL-30 adds hooks.
- Commands from §7: `new` (quick-add tokens, `--body -`, `--ac`, `defaults` from config), `set` (`=`, `+=`, `-=` on one or more IDs), `start` and `done` (the workflow config), `note` and `comment` (text or stdin), `ac` (check, uncheck, add, rm, and `check N --evidence TEXT`, which writes the criterion's evidence line per docs/format.md), `edit` (`$EDITOR` on a section or the whole file, version-checked on save), and `archive` and `unarchive`.
- With several IDs, report an outcome per ID and exit non-zero if any failed.
- `--if-version V` on every single-item write (§7): a changed item fails with `conflict` (exit 4) and nothing is written. Every write's JSON returns the new `version`. A write naming several IDs refuses the flag with `usage` (exit 2).
- Document every command in docs/cli.md and mark the names frozen in design.md §7.

## Acceptance criteria

- [x] #1 A CLI test for each command checks the resulting file through a golden diff that shows only the expected lines changed
- [x] #2 `bl set` with an unknown field or value exits 5 and lists the allowed values; a multi-ID set reports each ID's outcome and exits non-zero when any fails
- [x] #3 With a fake `$EDITOR`: a save after a concurrent change exits 4 and keeps both versions; invalid frontmatter exits 5 and leaves the file untouched
- [x] #4 `new --body -`, `note ID -`, and `comment ID -` read stdin
- [x] #5 docs/cli.md documents every write command, and design.md §7 marks the names frozen
- [x] #6 After a criterion is removed, `bl ac ID check N --if-version V` with the version read before the removal exits 4 with reason `conflict` and changes no bytes; with the current version it checks the intended criterion; each write's JSON returns the new `version`, and a multi-ID write with `--if-version` exits 2
- [x] #7 `bl ac ID check N --evidence TEXT` writes one `Evidence:` line under criterion N and replaces an existing one, unchecking removes it, more than one N exits 2, a golden case shows the evidence moving with its criterion when an earlier criterion is removed, and `bl show --json` reports evidence per criterion

## Definition of done

- [x] #1 task ci passes on the final commit
- [x] #2 Every checked acceptance criterion has an AC#N evidence line in Implementation Notes naming the command and its result
- [x] #3 Substantial changes had an independent verifier pass that returned PASS for every acceptance criterion
- [x] #4 The diff stays within the task's modified-file list, or Implementation Notes justify the deviation
- [x] #5 No test was deleted, skipped, or weakened
- [x] #6 User-visible behavior changes are reflected in README.md or docs/ in the same change

## Plan

1. Build the synchronous engine mutation entry point around config/capability validation and expected versions. Reuse it for all write handlers; leave async optimism, hooks and claims to their owning tasks.
2. Implement the listed commands with stdin support and explicit per-ID batch outcomes. Editor workflows capture the original version before launch, use a temporary edit buffer, validate before apply, and retain recoverable edited content on conflict.
3. After implementation, exercise every command through the CLI with golden file diffs and fake-editor conflict/invalid-save cases. Document which batch failure determines the overall exit code, defaults and stdin behavior in docs/cli.md; run task test and freeze write names in design.md.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.

## Notes

Claimed by pi-01a161b8-1fdc-729e-88c1-f4d9601f9c3f. Implementing in a Worktrunk checkout; delivery authorized: commit, local merge, cleanup; no push.

Implemented synchronous engine ApplyWrite, atomic same-item batch mutations, all ten write command names, stdin, version guards, recoverable editor saves, criterion evidence, and command documentation. task test and task ci pass in .worktrees/bl-8-write-commands; independent verifier is running. Scope deviation: internal/model adds Batch and Evidence; internal/format implements atomic codec batches/evidence and raw section buffers; internal/source/native advertises Batch so multi-field set persists once under its existing lock. These are required for per-item atomicity and evidence, not unrelated cleanup. Worktree receipt: .git/worktrees/bl-8-write-commands/agent-creation.json; branch bl-8-write-commands, created from 35c8f3a by session 01a161b8-1fdc-729e-88c1-f4d9601f9c3f, loop bf07d9c4-535b-47b3-a1b5-b1a63ec6eac2.

AC#1: task cli:test PKG=./internal/cli -- -run TestWriteGolden -count=1 (covered by final focused run) PASS: exact file goldens for every write command and AC suboperation; reviewed unified diffs against write-input.md.
AC#2: task cli:test PKG=./internal/cli -- -run TestWriteValidationAndBatch -count=1 (covered by final focused run) PASS: invalid fields/status/priority exit 5 with allowed values; failed multi-field set changes no bytes; mixed-ID batch returns each outcome and exit 3.
AC#3: task cli:test PKG=./internal/cli -- -run TestEditorRejectsAndRetains -count=1 (covered by final focused run) PASS: fake-editor conflict exits 4 preserving disk and edited buffer; invalid frontmatter exits 5 with original untouched.
AC#4: task cli:test PKG=./internal/cli -- -run TestWriteGolden -count=1 (covered by final focused run) PASS: new --body -, note ID -, and comment ID - persist stdin in golden files.
AC#5: rg -n command headings/frozen interface docs/cli.md docs/design.md PASS: all ten write names documented and frozen; independent verifier read full documentation and returned PASS.
AC#6: task cli:test PKG=./internal/cli -- -run TestCriterionVersionAndEvidence\|TestAllSingleWritesGuardVersion\|TestWriteValidationAndBatch -count=1 (covered by final focused run) PASS: stale criterion check exits 4/conflict without byte changes, current guard targets surviving criterion, successful writes return versions, multi-ID guarded writes exit 2.
AC#7: task cli:test PKG=./internal/cli -- -run TestCriterionVersionAndEvidence\|TestWriteGolden -count=1 (covered by final focused run) PASS: evidence added/replaced/removed, multiple evidence targets exit 2, rm golden retains evidence on surviving criterion, show JSON exposes evidence.
Independent verifier 4d739a2a-14b9-4aa6-8859-eb4d57014f20 returned PASS for AC#1-7, no concrete defects; report /tmp/bl-8-verification-01a161b8.md. task ci PASS on committed implementation fe71c5d7203883698ba1c8d3187eff4c1d1e8eb8 (security, checks, all tests, race, smoke); task check:staged PASS before commit. No tests deleted, skipped, or weakened.
Delivery: fe71c5d fast-forward merged into local main; no push. Verified receipt matched session/path/branch, clean checkout, and same-commit integration. wt remove bl-8-write-commands --foreground --format=json removed checkout and branch. Herdr workspace wHZ contained only an idle zsh before removal; post-remove hook closed it; herdr workspace list and wt list confirm no retained task checkout/workspace. No blockers or next resumable work.

Review 5e70dec: new/set expand ID shorthand in parent, blocked_by, related, duplicate_of (previously '<1' was rejected); goldens extended. task ci PASS.

## Summary

Implemented all native write commands with atomic version-checked mutations, stdin, editor recovery, and criterion evidence. Verified all seven acceptance criteria with CLI goldens/regressions and independent PASS; task ci passed on fe71c5d. Merged locally into main and removed the task branch/worktree/workspace. No push.
