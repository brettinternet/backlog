---
id: BL-8
title: >-
  Add the write commands: new, set, start, done, note, comment, ac, edit, and
  archive
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 02:55'
labels:
  - cli
  - agents
milestone: m-0
dependencies:
  - BL-6
  - BL-7
references:
  - docs/design.md
  - docs/format.md
modified_files:
  - internal/cli
  - internal/engine
  - docs/cli.md
  - docs/design.md
priority: high
type: feature
ordinal: 8000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Agents must edit the backlog with short commands instead of rewriting documents (D19). This builds the synchronous mutation path that the engine, TUI, hooks, and events reuse.

## What

- An engine mutation pipeline with no TUI concerns: validate against config and capabilities, apply with the expected version, return the new item. BL-15 adds optimism and undo on top of it, and BL-30 adds hooks.
- Commands from §7: `new` (quick-add tokens, `--body -`, `--ac`, `defaults` from config), `set` (`=`, `+=`, `-=` on one or more IDs), `start` and `done` (the workflow config), `note` and `comment` (text or stdin), `ac` (check, uncheck, add, rm, and `check N --evidence TEXT`, which writes the criterion's evidence line per docs/format.md), `edit` (`$EDITOR` on a section or the whole file, version-checked on save), and `archive` and `unarchive`.
- With several IDs, report an outcome per ID and exit non-zero if any failed.
- `--if-version V` on every single-item write (§7): a changed item fails with `conflict` (exit 4) and nothing is written. Every write's JSON returns the new `version`. A write naming several IDs refuses the flag with `usage` (exit 2).
- Document every command in docs/cli.md and mark the names frozen in design.md §7.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 A CLI test for each command checks the resulting file through a golden diff that shows only the expected lines changed
- [ ] #2 `bl set` with an unknown field or value exits 5 and lists the allowed values; a multi-ID set reports each ID's outcome and exits non-zero when any fails
- [ ] #3 With a fake `$EDITOR`: a save after a concurrent change exits 4 and keeps both versions; invalid frontmatter exits 5 and leaves the file untouched
- [ ] #4 `new --body -`, `note ID -`, and `comment ID -` read stdin
- [ ] #5 docs/cli.md documents every write command, and design.md §7 marks the names frozen
- [ ] #6 After a criterion is removed, `bl ac ID check N --if-version V` with the version read before the removal exits 4 with reason `conflict` and changes no bytes; with the current version it checks the intended criterion; each write's JSON returns the new `version`, and a multi-ID write with `--if-version` exits 2
- [ ] #7 `bl ac ID check N --evidence TEXT` writes one `Evidence:` line under criterion N and replaces an existing one, unchecking removes it, more than one N exits 2, a golden case shows the evidence moving with its criterion when an earlier criterion is removed, and `bl show --json` reports evidence per criterion
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
1. Build the synchronous engine mutation entry point around config/capability validation and expected versions. Reuse it for all write handlers; leave async optimism, hooks and claims to their owning tasks.
2. Implement the listed commands with stdin support and explicit per-ID batch outcomes. Editor workflows capture the original version before launch, use a temporary edit buffer, validate before apply, and retain recoverable edited content on conflict.
3. After implementation, exercise every command through the CLI with golden file diffs and fake-editor conflict/invalid-save cases. Document which batch failure determines the overall exit code, defaults and stdin behavior in docs/cli.md; run task test and freeze write names in design.md.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
