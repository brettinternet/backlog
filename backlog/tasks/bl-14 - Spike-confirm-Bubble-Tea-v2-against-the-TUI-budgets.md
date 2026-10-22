---
id: BL-14
title: 'Spike: confirm Bubble Tea v2 against the TUI budgets'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 02:13'
labels:
  - spike
  - tui
  - perf
milestone: m-1
dependencies:
  - BL-1
  - BL-12
references:
  - docs/design.md
  - docs/tui.md
modified_files:
  - docs/spikes/tui-stack.md
  - docs/design.md
  - internal/tui
  - go.mod
  - go.sum
priority: high
type: spike
ordinal: 14000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

D3 commits to Bubble Tea v2, Lip Gloss v2, and Bubbles v2. The owner's main complaints about Backlog.md are freezes and lag, so measure the stack before building on it.

## What

Build a prototype with 50k synthetic rows:

- A virtualized list, coalesced held-key navigation, and an async detail render. Compare `charm.land/glamour/v2` with a goldmark-plus-Lip-Gloss renderer.
- A PTY harness (`github.com/creack/pty`) that measures key-to-output latency and drives held-key input at 40 Hz.
- A 10-minute idle soak that tracks goroutines, heap, and CPU.

Record the results and decisions in `docs/spikes/tui-stack.md`. Keep the PTY harness in a reusable test package for BL-25. Don't merge prototype code that BL-16 won't reuse.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 docs/spikes/tui-stack.md records reference-host measurements: Update+View per navigation key at 50k, PTY key-to-frame p95, held `j` at 40 Hz for 5 s, Markdown render time for 1, 10, and 100 KB bodies, and a 10-minute idle soak
- [ ] #2 design.md D3 is confirmed or amended with that evidence, and the Markdown renderer choice is recorded
- [ ] #3 The PTY harness lives in a reusable test package with a passing test
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
1. Read the existing Bubble Tea v2 shell in internal/tui/model.go and terminal dispatch in internal/cli/root.go from 77dc4ee. It establishes wiring only, not D3 performance acceptance; reuse it and BL-12 fixtures rather than choosing a fresh stack or treating the shell as completed spike evidence.
2. Build the reusable virtualized list/detail prototype and PTY harness at 10k/50k. Compare renderer choices on 1/10/100 KB bodies and measure Update+View, PTY p95, 40Hz release latency and ten-minute idle CPU/heap/goroutines on the D26 host.
3. Record exact commands, pinned dependency versions and stack/renderer results in docs/spikes/tui-stack.md. Diagnose budget misses before accepting the stack; keep only code BL-16 reuses, preserve the honest bootstrap UI and update D3 only if the evidence changes its decision. Run task test.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and deliver the implementation before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
