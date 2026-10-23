# TUI stack spike (BL-14)

## Decision

- Keep Bubble Tea v2, Lip Gloss v2, and Bubbles v2, with the renderer capped at
  **120 FPS**. At the default 60 FPS, PTY key-to-frame p95 was 18.35 ms, over the
  16 ms budget, while Update+View took only 10.8 µs: frame pacing, not
  computation, was the miss.
- Render Markdown with **Glamour v2**, asynchronously. A Goldmark plus Lip Gloss
  renderer was 4–6× faster but lacked list markers, link targets, tables, and
  syntax highlighting; maintaining a second Markdown renderer isn't worth it.
  Glamour's cost at 100 KiB makes worker rendering, a selection-settle debounce,
  version/width/theme caching, and body limits required (D14).

## Setup

Measured 2026-10-22 on the D26 host (Apple M5 Max, 64 GiB, Darwin 27.0.0 arm64,
Go 1.27.3), 120×40 terminal, 80-cell Markdown. Bubble Tea 2.1.0, Lip Gloss
2.0.5, Bubbles 2.2.1, Glamour 2.0.1, Goldmark 1.8.6, creack/pty 1.1.24, x/vt
0.1.0.

```sh
task cli:test PKG=./internal/tui/... -- -count=1
BL_TUI_MEASURE=1 task cli:test PKG=./internal/tui -- -run '^TestStackMeasurements$' -v -count=1 -timeout=5m
BL_TUI_SOAK=1 task cli:test PKG=./internal/tui -- -run '^TestStackSoak$' -v -count=1 -timeout=12m
```

The in-process run prepares 50,000 rows from the BL-12 generator (seed 42)
before timing. PTY runs use prepared 50k and 10k row sets with small bodies.
This isolates input and rendering; it doesn't measure startup or storage.

## Navigation

| Measurement | Samples | Result | Budget |
| --- | ---: | ---: | ---: |
| 50k Update + View per `j`, p95 | 2,000 | 25.583 µs | ≤ 4 ms |
| 50k PTY key-to-frame, p95 | 100 | 9.684 ms | ≤ 16 ms |
| 10k held `j`, 40 Hz, 200 keys | 5.000 s | all 200 applied | no accumulated lag |
| Release to final frame | 1 | 9.648 ms | ≤ 16 ms |

The 9.648 ms release time shows no multi-frame backlog. It doesn't prove the
nominal 8.33 ms frame interval end to end.

`internal/tui/ptytest` runs a child in a real PTY and rebuilds frames with x/vt,
because Bubble Tea may redraw only a changed digit; searching raw output misses
that. The PTY measurement watches a marker at the bottom of the frame, after
the visible rows. `TestIncrementalFrame` guards partial-frame handling and
`TestListPTY` drives the real loop.

## Markdown

Twenty fresh renders per size, p95, including construction, parsing, and
wrapping:

| Body | Glamour v2 | Goldmark + Lip Gloss |
| --- | ---: | ---: |
| 1 KiB | 0.422 ms | 0.132 ms |
| 10 KiB | 4.956 ms | 1.294 ms |
| 100 KiB | 67.296 ms | 10.487 ms |

Neither runs on navigation. A single 50 ms settle timer coalesces detail
requests, and a generation check drops stale results.

## Ten-minute idle soak

| Metric | Result |
| --- | --- |
| Duration | 600.0 s |
| CPU | 2.225 s: 0.37% of one core |
| Goroutines | 10 → 10 |
| Post-GC heap | 6,262,504 → 6,247,560 bytes |
| `j` to frame afterward | 7.724 ms |

The soak runs the real loop with 50k rows in a PTY and writes counters to a side
file. A first attempt wrote telemetry to stderr, which moved the cursor behind
Bubble Tea's back and failed the final check; it isn't counted. This qualifies
the stack only. The full application's one-hour soak is in
[benchmarks](../benchmarks.md#idle-soak).

## Kept

The virtualized list core, async `RenderMarkdown`, measurement fixtures, and the
PTY harness. No prototype application ships; the child app exists only in tests.
