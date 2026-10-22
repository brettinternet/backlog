---
id: BL-7
title: 'Add the read commands: ls, show, and next'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
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
- [ ] #1 CLI tests cover each ID shorthand form, the ambiguous case (exit 2 with matches), and not found (exit 3)
- [ ] #2 Golden tests pin the JSON `data` shapes of ls, show, and next, and docs/cli.md documents the same shapes
- [ ] #3 `bl next --explain --json` lists included and excluded items with the engine's reasons
- [ ] #4 Titles and bodies containing CSI and OSC sequences print without them in text output
- [ ] #5 docs/cli.md documents ls, show, and next, and design.md §7 marks those names frozen
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
