---
id: BL-47
title: Meet the warm ls and next CLI latency budget
status: To Do
assignee: []
created_date: '2026-10-22 05:26'
labels:
  - perf
dependencies:
  - BL-12
priority: high
ordinal: 47000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
BL-12 measured 10k-item warm production CLI latency above design.md §11 on the approved Apple M5 Max/64 GiB reference host: initial p95 ls 308.6 ms and next 279.6 ms versus 150 ms. Reproduce with task bench; preserve query and ranking semantics. See docs/benchmarks.md for the final reference run.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 task bench records p95 at most 150 ms for both warm bl ls and bl next at 10k on the D26 reference host
- [ ] #2 Relevant correctness tests and task ci pass without weakened or skipped tests
- [ ] #3 docs/benchmarks.md records updated measurements and reproduction details
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
