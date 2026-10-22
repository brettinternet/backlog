---
id: BL-12
title: Generate deterministic fixtures and benchmark the CLI budgets
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:45'
labels:
  - perf
  - tooling
milestone: m-0
dependencies:
  - BL-7
  - BL-11
  - BL-8
references:
  - docs/design.md
  - docs/development.md
modified_files:
  - internal/fixtures
  - cmd/bl-fixtures
  - .taskfiles/cli.yaml
  - docs/benchmarks.md
priority: high
type: feature
ordinal: 12000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

The §11 budgets are acceptance criteria (D26), so every later task needs a reproducible way to measure them.

## What

- A seeded fixture generator (`internal/fixtures` plus `cmd/bl-fixtures`) that writes N native items with realistic spreads: statuses, priorities, labels, assignees, blocker chains and fan-out, hierarchy depth, body sizes from empty to 100 KB, and CJK, emoji, and combining-mark titles.
- Benchmarks at 10k and 50k for cold load, warm load, `ls`, `next`, `show`, and a single-field write.
- `task bench` runs them. They never run under `task test`.
- `docs/benchmarks.md` records reference-host results against each CLI budget and explains how to reproduce them.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 The same seed produces a byte-identical fixture tree (a test hashes the output)
- [ ] #2 `task bench` reports results at 10k and 50k for each listed operation, and `task test` runs none of them
- [ ] #3 docs/benchmarks.md records reference-host results for every CLI budget in §11; each miss links a new follow-up task
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
1. Implement seeded deterministic fixtures with a fixed clock, realistic graphs/bodies and Unicode titles; expose cmd/bl-fixtures with documented seed/count/output arguments.
2. Wire task bench to opt-in cold/warm load, ls/next/show and single-field-write measurements at 10k/50k, with warm-up and multiple samples for p95. Use BL-8 writes rather than a benchmark-only mutation implementation.
3. Run deterministic tree-hash verification under task test, then task bench on the design.md D26 reference host. Record host/toolchain/seed, exact commands and each CLI budget in docs/benchmarks.md. A miss is not a pass: diagnose/fix within scope or record measured follow-up and keep unmet acceptance visible.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
