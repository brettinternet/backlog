---
id: BL-12
title: Generate deterministic fixtures and benchmark the CLI budgets
status: Done
assignee:
  - '@01a16205-e986-729e-88c1-f544e7657f9e'
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 06:07'
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
- [x] #1 The same seed produces a byte-identical fixture tree (a test hashes the output)
- [x] #2 `task bench` reports results at 10k and 50k for each listed operation, and `task test` runs none of them
- [x] #3 docs/benchmarks.md records reference-host results for every CLI budget in §11; each miss links a new follow-up task
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
1. Implement seeded deterministic fixtures with a fixed clock, realistic graphs/bodies and Unicode titles; expose cmd/bl-fixtures with documented seed/count/output arguments.
2. Wire task bench to opt-in cold/warm load, ls/next/show and single-field-write measurements at 10k/50k, with warm-up and multiple samples for p95. Use BL-8 writes rather than a benchmark-only mutation implementation.
3. Run deterministic tree-hash verification under task test, then task bench on the design.md D26 reference host. Record host/toolchain/seed, exact commands and each CLI budget in docs/benchmarks.md. A miss is not a pass: diagnose/fix within scope or record measured follow-up and keep unmet acceptance visible.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Owner approved this Apple M5 Max/64 GiB as the D26 reference host and creation of measured budget-miss follow-up tasks. Modified-file scope additionally includes docs/design.md for this approved decision update. Benchmarks use production CLI subprocesses; cold load means uncached native parsing, not a privileged OS page-cache purge.

Implementation and task ci passed in the isolated checkout. Direct cmd/bl-fixtures generation (64 items, seed 42) followed by bin/bl check --json returned no findings. Independent verifier 4425db9a returned PASS AC1/AC2, AC3 pending final benchmark report (no concrete defects). Full 20-sample run is still warming the 50k cache; log /tmp/bl-12-bench-full.log. Initial 600s run timed out; full harness timeout is now 45m. Newly created BL-47 covers warm ls/next latency, BL-48 covers CLI RSS misses. Finalize results and request criteria-only verification of AC3, then commit, integrate, run final CI and cleanup.

AC#1: task cli:test PKG=./internal/fixtures -- -count=1 passed; TestDeterministicTree hashed paths/content from independent same-seed trees, confirmed a different seed changes output, and parsed/validated references. Direct cmd/bl-fixtures (64 items, seed 42) plus bin/bl check --json returned findings: [].
AC#2: task bench passed with all 12 operation/size rows, 20 samples each, package elapsed 2242.742s. task ci on integrated main 1a8f55d passed, including task test and race; ordinary tests ran no benchmarks or large fixture generation.
AC#3: task bench results were compared against docs/benchmarks.md by independent verifier ac1af477: PASS. Every CLI latency/RSS budget is reported and each miss links the newly created BL-47, BL-48, BL-49 or BL-50. Approved M5 Max/64 GiB host is recorded in D26. Runtime misses remain explicit follow-up work, not falsely reported passes.
Independent verifier 4425db9a plus pending-evidence completion ac1af477 returned PASS for all three criteria; no concrete scoped defects found. Source unchanged after verification. No existing test deleted, skipped or weakened. The newly added BenchmarkCLI opt-in gate is benchmark-only, not a skipped correctness test.
Delivery: implementation d194aa7 merged into main as 1a8f55d; task ci passed on integrated source (/tmp/bl-12-ci-final.log). docs/design.md is the only justified extension beyond declared file scope (owner-approved D26 update). No pushes or PRs.
Cleanup verified: session-owned checkout /Users/brett/dev/me/backlog/.worktrees/bl-12-fixture-benchmarks, branch bl-12-fixture-benchmarks, receipt creation ffa8f2846a09be53f8ea090d90f7649ee204dec9, session 01a16205-e986-729e-88c1-f544e7657f9e. wt list and receipt matched; Herdr wJ5 had only an idle zsh. wt remove --foreground --format=json deleted checkout and branch; subsequent wt list shows primary only and herdr workspace list confirms wJ5 disappeared. Workbench plugin unavailable, so equivalent Herdr CLI inspection was used. No retained task worktrees/workspaces. No blocker or resumable BL-12 work remains.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Added seeded native fixtures and opt-in 10k/50k CLI benchmarks with p95/RSS reporting. Full task bench and integrated-main task ci passed; independent verifier PASS all criteria. Reference results and budget misses recorded in docs/benchmarks.md with BL-47–BL-50 follow-ups. Merged d194aa7 via 1a8f55d; owned checkout, branch and Herdr workspace removed.
<!-- SECTION:FINAL_SUMMARY:END -->
