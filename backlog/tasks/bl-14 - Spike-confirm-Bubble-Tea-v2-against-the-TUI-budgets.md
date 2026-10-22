---
id: BL-14
title: 'Spike: confirm Bubble Tea v2 against the TUI budgets'
status: Done
assignee:
  - '@pi-01a1623f'
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 06:45'
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
- [x] #1 docs/spikes/tui-stack.md records reference-host measurements: Update+View per navigation key at 50k, PTY key-to-frame p95, held `j` at 40 Hz for 5 s, Markdown render time for 1, 10, and 100 KB bodies, and a 10-minute idle soak
- [x] #2 design.md D3 is confirmed or amended with that evidence, and the Markdown renderer choice is recorded
- [x] #3 The PTY harness lives in a reusable test package with a passing test
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
1. Reuse the bootstrap shell and deterministic fixtures; add reusable virtualized navigation/detail primitives and a PTY test harness without changing the bootstrap UI.
2. Measure 50k Update+View, PTY latency and held input, renderer alternatives at 1/10/100 KB, and a real ten-minute idle soak.
3. Document reference-host results and D3/renderer decision; run focused checks, task ci, and independent acceptance verification.
4. Commit implementation, merge into main, record acceptance evidence and completion in the primary backlog, release claim, and remove the owned worktree.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Implemented reusable viewport-only List core, debounced async detail rendering, explicit-style Glamour renderer, and unix PTY harness with terminal diff reconstruction and public-interface regression test. Production bootstrap remains unchanged. Measurements: 50k Update+View p95 25.583 us; PTY p95 9.684208 ms at 120 FPS (default 60 FPS missed at 18.347625 ms); 200 held keys at 40 Hz applied with final-frame observation 9.648417 ms after release. This is below the 16 ms terminal-output budget, not a claim of a strict 8.33 ms wall-clock ceiling. Final ten-minute soak still running. Focused race tests and task ci passed; final commit gate will be rerun. Independent verifier AC3 PASS; AC1/AC2 pending final soak/D3 documentation. Modified-file scope preserved: harness is under internal/tui. Upgraded x/text to 0.39.0 because newly reachable normalization code triggered GO-2026-5970 in task ci; security now reports zero reachable vulnerabilities.

First ten-minute run recorded CPU 2.457166 s over 600.001840 s, goroutines 10->10, post-GC heap 6256536->6241880 bytes. Its final frame assertion failed because the measurement goroutine wrote stats to stderr (the same PTY), desynchronizing Bubble Tea cursor tracking; output showed the key was processed, but this run is not accepted as a passing soak. Corrected telemetry now writes an out-of-band file and sends a model message for an in-band completion banner. Added TestSoakOutputRouting to catch that regression through a real PTY. Short apparatus smoke (2 s, race detector) and default focused/race tests pass; task ci passes after the fix. Full corrected ten-minute soak is running with default duration; completion remains gated on its result and final independent acceptance verification.

AC#1: BL_TUI_MEASURE=1 task cli:test PKG=./internal/tui -- -run "^TestStackMeasurements$" -v -count=1 -timeout=5m PASS: 50k Update+View p95 25.583 us; PTY p95 9.684208 ms; 40 Hz/200 keys/5.000034667 s, final frame 9.648417 ms after release; Glamour p95 0.422125/4.956334/67.296042 ms at 1/10/100 KiB. BL_TUI_SOAK=1 task cli:test PKG=./internal/tui -- -run "^TestStackSoak$" -v -count=1 -timeout=12m PASS: 600.002289250 s, CPU 2.225250 s, goroutines 10->10, heap 6262504->6247560 bytes, post-idle response 7.723750 ms. Exact reference-host results and reproduction are in docs/spikes/tui-stack.md.
AC#2: git diff 598cd4f 4ade600 -- docs/design.md docs/spikes/tui-stack.md reviewed; D3 confirms v2 with 120 FPS, asynchronous Glamour v2 choice and measured rationale. Independent verifier run 753c62bc-b5c5-4a58-8664-a77910150581 returned AC2 PASS.
AC#3: task cli:test PKG=./internal/tui/... -- -race -count=1 PASS, including reusable ptytest external-package incremental-frame regression and model-mediated telemetry-routing regression. Independent verifier returned AC1 PASS, AC2 PASS, AC3 PASS after focused tests/race and completed-soak evidence review.
Delivery: implementation commit 4ade600, rebased onto current main without modifying concurrent work, then git merge --ff-only spike/bl-14-tui-stack succeeded. task check:staged and task ci PASS on 4ade600 (security, check, full tests, race, build, smoke); no tests deleted, skipped or weakened. git diff --name-status 598cd4f 4ade600 confirms only the declared modified-file scope. Documentation and D3 included in the implementation commit; production bootstrap behavior is unchanged.
Cleanup: verified original session creation receipt and wt list, inspected exact owned Herdr workspace wJB, then wt remove spike/bl-14-tui-stack --foreground --format=json removed both checkout and merged branch. git worktree list shows only the primary checkout; herdr workspace list confirms wJB disappeared. No retained task checkout/workspace. No remaining blocker or resumable item-scoped step; BL-16 can reuse the list core and harness.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Qualified the v2 TUI stack on the reference host, selected asynchronous Glamour, retained a virtualized list core and reusable PTY harness, and documented exact measurements including a passing ten-minute idle soak. Independent verifier PASS for all ACs; task ci PASS. Implementation 4ade600 merged locally into main; owned worktree, branch and Herdr workspace removed. Nothing pushed.
<!-- SECTION:FINAL_SUMMARY:END -->
