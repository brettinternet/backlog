---
id: BL-49
title: Meet the show latency budget at 50k items
status: To Do
assignee: []
created_date: '2026-10-22 06:04'
labels:
  - perf
dependencies:
  - BL-12
priority: high
ordinal: 49000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
BL-12 task bench on the approved Apple M5 Max/64 GiB D26 host measured bl show BL-1 p95 48.23 ms at 50k, exceeding the design.md §11 30 ms budget (10k passes at 14.61 ms). Production CLI process launch, discovery and rendering are included. See docs/benchmarks.md. Preserve ID shorthand and duplicate detection.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 task bench reports bl show p95 at most 30 ms at both 10k and 50k on the D26 reference host
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
