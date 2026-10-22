# TUI stack spike (BL-14)

## Decision

Keep Bubble Tea v2, Lip Gloss v2, and Bubbles v2. Use **120 FPS** for the
future data-backed TUI: the default 60 FPS cap missed the 16 ms PTY budget.
Keep the current bootstrap shell unchanged until BL-16 wires the list.

Choose **Glamour v2** for asynchronous Markdown rendering. A small
Goldmark-plus-Lip-Gloss renderer is faster, but it is not feature equivalent:
the comparator handles text, headings, emphasis and fenced code, but omits
list markers, link destinations, tables and syntax highlighting. Maintaining
a complete second Markdown renderer is not justified by these measurements.
Glamour's 100 KiB cost makes worker rendering, selection-settle debounce,
version/width/theme caching and body limits necessary, not optional (D14).
The reusable renderer currently uses the explicit `notty` style, avoiding
terminal/environment probing; theme integration belongs to BL-17.

## Reference host and reproduction

Measured 2026-10-22 on D26's Apple M5 Max, 64 GiB RAM, macOS Darwin 27.0.0
arm64, Go 1.27.3. Terminal size: 120 × 40; Markdown width: 80 cells.
Pinned versions: Bubble Tea 2.1.0, Lip Gloss 2.0.5, Bubbles 2.2.1,
Glamour 2.0.1, Goldmark 1.8.6, creack/pty 1.1.24, x/vt 0.1.0.
The dependency update also pins x/text 0.39.0 to resolve GO-2026-5970;
`task ci` reports no reachable vulnerabilities.

Run from the repository root:

```sh
task cli:test PKG=./internal/tui/... -- -count=1
BL_TUI_MEASURE=1 task cli:test PKG=./internal/tui -- -run '^TestStackMeasurements$' -v -count=1 -timeout=5m
BL_TUI_SOAK=1 task cli:test PKG=./internal/tui -- -run '^TestStackSoak$' -v -count=1 -timeout=12m
task cli:test PKG=./internal/tui/... -- -race -count=1
task ci
```

Measurements are opt-in, report-only tests; CI never asserts absolute time.
The in-process run prepares 50,000 rows from BL-12's file fixture generator
(seed 42), including multilingual titles and varied body lengths. Generation,
file reads and parsing occur **before** timing. PTY children use a deterministic
50k/10k prepared row set (multilingual titles, small Markdown bodies), not a
native load. This isolates input/rendering; it does not measure startup or
storage performance. The ten-minute soak also uses the 50k prepared row set.

## Navigation and terminal results

| Measurement | Samples | Result | Budget |
| --- | ---: | ---: | ---: |
| 50k Update + View per `j`, p95 | 2,000 | 25.583 µs | ≤ 4 ms |
| 50k PTY key-to-frame, p95, 120 FPS | 100 | 9.684208 ms | ≤ 16 ms |
| 10k held `j`, 40 Hz, 200 keys | 5.000034667 s | selection 200, no dropped input | 5 s, no accumulated lag |
| Release to final observed frame | 1 held run | 9.648417 ms | within the ≤ 16 ms terminal-frame budget |

The initial 60 FPS experiment measured 18.347625 ms PTY p95 and
14.526208 ms release-to-output. Update+View was only 10.792 µs, identifying
frame pacing rather than O(N) computation as the miss. Raising the renderer
cap to 120 FPS resolved it. The final PTY measurement observes a unique
selection marker at the **bottom** of the frame, after the visible rows.
The earlier 60 FPS run observed a top-of-frame marker; even that lower bound
missed the budget. The decision does not rely on comparing equal marker costs.

`internal/tui/ptytest` starts a child in a real PTY, continuously drains output,
and reconstructs cursor-addressed incremental frames using x/vt. Its terminal
response pipe is forwarded to the child. Searching raw output alone is wrong:
Bubble Tea may output only a changed digit. The harness keeps a bounded 1 MiB
raw transcript and a fixed-size screen, uses deadlines, and kills/reaps owned
children on cleanup. `TestIncrementalFrame` guards the partial-frame regression;
`TestListPTY` drives the real Bubble Tea loop. Timing includes the PTY write,
child input/update, render pacing, terminal output, and parent observation.
The final-frame delay is an upper bound, including parent scheduling latency.
The 120 FPS cap has a nominal 8.33 ms interval; 9.648417 ms is **not** proof
of an 8.33 ms wall-clock ceiling. It shows no multi-frame input backlog and
fits §11's ≤ 16 ms key-to-PTY frame budget. A hard 8.33 ms end-to-end target
would require separate acceptance and tighter scheduling, not rounding this
measurement down.

## Markdown results

Twenty fresh-render samples per size, repeated structured Markdown, p95:

| Body | Glamour v2 | Goldmark + Lip Gloss comparator |
| --- | ---: | ---: |
| 1 KiB | 422.125 µs | 131.500 µs |
| 10 KiB | 4.956334 ms | 1.293917 ms |
| 100 KiB | 67.296042 ms | 10.487042 ms |

These include renderer construction, parsing, wrapping and string generation.
Neither renderer runs on navigation's Update/View path. The 50 ms settle
debounce has one pending timer, coalesces intermediate detail requests and
rejects stale results by generation. Navigation itself applies every key in
constant time, while Bubble Tea coalesces frame output. The latest-wins test
checks that an older result cannot replace the new selection's detail.

## Ten-minute idle soak

The corrected default-duration run **passed**:

| Metric | Before → after / result |
| --- | --- |
| Idle duration | 600.002289250 s |
| Process CPU | 2.225250 s total, 0.371% of one CPU |
| Live goroutines | 10 → 10 |
| Post-GC heap | 6,262,504 → 6,247,560 bytes (−14,944 bytes) |
| Post-soak `j` to observed frame | 7.723750 ms |

No goroutine or retained-heap growth was observed; input remained responsive.
CPU was low but not literally zero. Full-application idle qualification remains
BL-25's responsibility.

The soak runs the actual Bubble Tea loop with 50k rows in a PTY, warms for
one second, then records process CPU (`getrusage`), live goroutines and
post-GC heap before/after ten minutes. Counters are written to a temporary
out-of-band file, then a model message paints a completion banner. It sends
`j` afterward to verify input still reaches an observed final frame. There
are no idle model timers: the measurement goroutine owns just one ten-minute
timer. The default `TestSoakOutputRouting` uses a short interval to guard the
measurement apparatus; only the default-duration opt-in run is soak evidence.
This is the requested short stack qualification, **not** BL-25's one-hour
full-application soak.

An initial ten-minute run had stable counters, but its final assertion failed:
writing telemetry directly to stderr moved the PTY cursor without updating
Bubble Tea's screen tracker. The corrected run uses only model-mediated
terminal output; the failed run is not counted as passing soak evidence.

## What is retained, and what remains

Retain the virtualized `List` core, async `RenderMarkdown`, opt-in measurement
fixtures, and reusable PTY harness. The production bootstrap's behavior is
unchanged. No alternative application entry point or throwaway prototype is
shipped; the child application exists only in tests.

BL-16 should reuse the list core with immutable worker-prepared rows and
120 FPS. BL-17 adds the real detail pane, bounded cancellation/cache and theme
integration. This spike does not qualify filters, source events, first paint,
full-application RSS or the one-hour idle budget; BL-25 measures those as the
features become available. It establishes that the chosen stack is not itself
a blocker to those implementations.
