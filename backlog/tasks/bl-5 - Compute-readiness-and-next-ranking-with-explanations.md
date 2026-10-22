---
id: BL-5
title: Compute readiness and next ranking with explanations
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:45'
labels:
  - engine
  - readiness
milestone: m-0
dependencies:
  - BL-2
  - BL-3
references:
  - docs/design.md
modified_files:
  - internal/engine
priority: high
type: feature
ordinal: 5000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Backlog.md gives agents no reliable answer to "what's next". `bl` ranks deterministically and explains every inclusion and exclusion (D12, §6).

## What

`internal/engine` readiness over an item set and the ready config:

- Candidate rules, readiness values, and reasons, exactly as §6 states. A known unsatisfied blocker means `blocked` even when other edges are unknown. Cross-source references resolve when their target is present in the supplied snapshot. Missing, unresolved, or stale targets are `unknown`; source qualification alone does not make an edge unknown (format.md#frontmatter-keys).
- Claims come in as an input (`none`, `mine`, `others`, `unknown`) so BL-32 can plug in Worklease without changing the rules.
- Total, deterministic ranking: priority, rank, unblock impact (open items transitively blocked, counted once, safe on cycles), created, natural ID.
- A machine-readable explanation per item and the text form `high · rank a3 · unblocks 4 (BL-9, BL-12, +2)`.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Table tests cover every readiness value and reason, including a known blocker winning over an unknown one, cross-source references, containers, excluded labels, and assignment to others, to me, and to nobody
- [ ] #2 Shuffled input produces an identical ranking, and each tie-break level is exercised
- [ ] #3 Unblock impact counts shared descendants once and terminates on cycles, reporting the cycle members
- [ ] #4 Explanation text matches the design.md §6 format
- [ ] #5 A benchmark over 50k items and 100k edges reports readiness and ranking time; the result is recorded in Implementation Notes
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
1. Implement a pure readiness calculation from items, config, identity, coverage, and claim inputs. Resolve cross-source edges when present in the supplied snapshot; only unresolved/stale edges are unknown. Keep known unsatisfied blockers dominant over unknown edges.
2. Compute cycle-safe distinct transitive unblock impact and total ranking; break natural-ID equivalence deterministically by source/ref when needed. Generate structured and display explanations from the same result.
3. Use table cases for reasons and shuffled-input ranking, plus shared descendants and cycles. Run task test and a 50k-item/100k-edge benchmark; record the command and timing without a wall-clock assertion in default tests.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
