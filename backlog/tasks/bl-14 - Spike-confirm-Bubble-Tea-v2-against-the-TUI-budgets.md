---
id: BL-14
title: 'Spike: confirm Bubble Tea v2 against the TUI budgets'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - spike
  - tui
  - perf
milestone: m-1
dependencies:
  - BL-1
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
