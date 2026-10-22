---
id: BL-16
title: 'Build the TUI shell: list layout, navigation, header, and footer'
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:45'
labels:
  - tui
milestone: m-1
dependencies:
  - BL-14
  - BL-15
references:
  - docs/tui.md
  - docs/design.md
modified_files:
  - internal/tui
  - internal/cli
  - scripts/tui-snapshot
  - go.mod
  - go.sum
priority: high
type: feature
ordinal: 16000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

The TUI is the owner's main reason for `bl`. This task builds the frame that every later TUI task extends (tui.md).

## What

- Bare `bl` opens the TUI when stdin and stdout are terminals.
- The tui.md layout: header with view tabs and one badge per source, the list, and a footer with the query, pending and failed counts, and hints.
- List rows with columns, glyphs, and the claim column hidden until BL-32. Rendering is O(viewport).
- The navigation keys from tui.md: counts, `gg`/`G`/`NG`, half and full pages, `[`/`]`, `za` folds, `1`-`9`, `gt`/`gT` over the built-in views, `R`, `q`.
- Resize handling, and hiding secondary columns below 80 columns. Width measured with `x/ansi`.
- `NO_COLOR`, `--high-contrast`, and sanitized text.
- `scripts/tui-snapshot`: start `bl` in a tmux pane of a given size and capture the pane as text, so agents can inspect renders.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Model tests at a fixed size and theme cover every tui.md navigation key, including counts and group jumps, against golden views
- [ ] #2 A benchmark shows navigation Update+View at 50k items within 4 ms, and the result is recorded
- [ ] #3 `scripts/tui-snapshot` captures renders at 80x24 and 160x48, used as evidence in Implementation Notes
- [ ] #4 Under `NO_COLOR` the output has no styling; `--high-contrast` uses no faint text; glyphs still carry meaning
- [ ] #5 At 79 columns secondary columns hide, and CJK and emoji titles align in golden views
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
1. Use the accepted BL-14 stack and BL-15 engine subscriptions. Add terminal-only root dispatch, immediate loading frame, viewport-only rows, source badges and footer; keep non-TTY bare invocation useful rather than entering raw mode.
2. Implement the in-scope navigation and provisional built-in list tabs, resize/cell widths, sanitization and accessibility. Later tasks own detail contents, saved views, board/tree and editing; do not advertise unfinished actions as operational.
3. Add a reproducible scripts/tui-snapshot using an owned tmux pane and temporary fixture root. Verify 80x24/160x48, 79-column collapse, Unicode and NO_COLOR with model/PTY output; run task test and opt-in 50k navigation measurements. Reconcile implemented bindings in docs/tui.md.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
