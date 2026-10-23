# Benchmarks

Measurements against the [design §11 budgets](design.md#11-performance). They
are report-only: CI never asserts absolute time.

Reference host (D26): Apple M5 Max, 64 GiB, Darwin 27.0.0 arm64, Go 1.27.3,
`GOMAXPROCS=18`, `GOGC` and `GOMEMLIMIT` unset. Runs date from 2026-10-22. Other
work sometimes ran on the same host, so treat these as observations, not
isolated-host guarantees.

## Summary

| Budget (p95) | Result | Run |
| --- | --- | --- |
| `bl show ID` ≤ 30 ms | 10k 12.29 ms; 50k 15.15 ms | BL-48 |
| Warm `bl ls` / `bl next`, 10k ≤ 150 ms | 132.6 / 107.7 ms | BL-48 |
| Native single-field write ≤ 50 ms | 10k 26.10 ms; 50k 38.66 ms | BL-50 |
| CLI peak RSS, 10k ≤ 150 MiB; 50k ≤ 400 MiB | 116.6 / 383.8 MiB (`ls`) | BL-48 |
| TUI warm first content, 10k ≤ 100 ms | 37.60 ms | BL-58 |
| TUI cold first content, 10k ≤ 400 ms | 54.50 ms, before the load completes | BL-58 |
| Navigation Update+View, 50k ≤ 4 ms | 0.081 ms | BL-58 |
| Navigation key-to-PTY, 50k ≤ 16 ms | 10.13 ms | BL-58 |
| Held `j`, 40 Hz × 5 s, 10k: stop within one frame (8.33 ms) | 6.61 ms after release, all 200 keys | BL-58 |
| Filter keystroke, 10k ≤ 30 ms; 50k ≤ 80 ms | 9.38 / 17.13 ms | BL-58 |
| External file change to visible, 10k ≤ 150 ms | 91.45 ms | BL-58 |
| One hour idle, 10k: ≤ 2% of a core, no growth | 1.94%; goroutines 21 → 21; heap +1,080 B | BL-25 |
| TUI peak RSS, 10k ≤ 150 MiB; 50k ≤ 400 MiB | 133.11 / 382.83 MiB | BL-58 |

Every budget passes.

## Fixture

```sh
mise exec go -- go run ./cmd/bl-fixtures -out /tmp/bl-fixture-example -count 10000 -seed 1
bin/bl -C /tmp/bl-fixture-example ls
```

The generator (BL-12) writes Git discovery files, project config, and N native
items. The output directory must not exist. Seed 1 is used throughout.

| Property | Distribution |
| --- | --- |
| Statuses | All five categories |
| Priorities, labels, assignees | Include unset and unassigned |
| Blockers | Bounded chains plus fan-out from BL-1 |
| Hierarchy | Up to four levels |
| Description size | 0 / 256 / 2,048 / 8,192 / 102,400 bytes at 30 / 40 / 20 / 9 / 1% |
| Titles | Include CJK, emoji, and combining marks |

Clocks and randomness are explicit. A test checks that the same seed reproduces
the same tree, a different seed changes it, every item parses, and references
resolve without self-edges. HOME and XDG directories are isolated.

## CLI

```sh
task bench                                           # everything, 20 samples each
task bench -- -bench 'BenchmarkCLI/10000/show$'      # one measurement
```

| Operation | 10k p95 | 50k p95 | 10k peak RSS | 50k peak RSS |
| --- | ---: | ---: | ---: | ---: |
| Cold native load | 244.0 ms | 1,103 ms | — | — |
| Warm native load | 288.0 ms | 1,311 ms | — | — |
| `bl ls` | 132.6 ms | 815.0 ms | 116.6 MiB | 383.8 MiB |
| `bl next` | 107.7 ms | 736.4 ms | 113.1 MiB | 340.8 MiB |
| `bl show BL-1` | 12.29 ms | 15.15 ms | 22.48 MiB | 22.84 MiB |
| Single-field write | 26.10 ms | 38.66 ms | 23.56 MiB | 26.84 MiB |

What each row measures:

- **Cold load:** native `Source.Load` without the SQLite cache, parsing every
  item. The OS page cache is warm; "cold" means no application cache.
- **Warm load:** `Source.Load` with a populated cache and stat validation.
- **`ls`, `next`, `show`, write:** a production `bl` subprocess, including
  launch, discovery, config, and output. `show` reads BL-1. Write runs
  `bl set BL-1 title=...` with hooks and Git off.
- **p95:** nearest rank over 20 samples (the 19th), after one unmeasured
  warm-up. Not Go's mean `ns/op`.
- **RSS:** the highest child-process high-water mark across samples.

The 50k `ls`/`next` and load times have no §11 budget. A full run can take
40 minutes, mostly cold cache population. `task ci` never runs `BenchmarkCLI`.

## TUI

```sh
BL_TUI_MEASURE=1 task cli:test PKG=./internal/tui -- -run '^TestProductionMeasurements$|^TestShellMeasurement$' -v -count=1 -timeout=10m
BL_TUI_PERF_SOAK=1h task cli:test PKG=./internal/tui -- -run '^TestProductionSoak$' -v -count=1 -timeout=75m
```

Narrow the first command with `/10000/startup$` or
`/(10000|50000)/interaction$` after `TestProductionMeasurements$`.

The harness drives the production store, directory watcher, projector, and
`Model` in a real 120×40 PTY at 120 FPS, using the same runtime settings as
`bl`. A measurement header replaces one screen row to expose projection and
cursor state without changing input or rendering.

| Boundary | 10k | 50k |
| --- | ---: | ---: |
| Peak child RSS | 133.11 MiB | 382.83 MiB |
| Cold startup: shell / first content / complete | 21.80 / 54.50 / 2,180.02 ms | — |
| Warm startup: shell / first content / complete | 21.61 / 37.60 / 205.12 ms | — |
| Navigation key-to-PTY (n=100) | 9.55 ms | 10.13 ms |
| Filter key-to-completed-PTY (n=20) | 9.38 ms | 17.13 ms |
| External rename-to-visible (n=20) | 91.45 ms | — |

Update+View over 50,001 prepared rows: 0.081 ms (n=2,000). Held `j`: 200 keys in
5.002 s, final cursor 6.61 ms after release. An independent rerun measured peak
RSS of 131.48 / 379.34 MiB.

What each boundary means:

- **Startup:** launch to the shell, to a nonempty prepared list, and to complete
  source coverage. A loading screen never counts as content. 20 samples per
  condition. Cold means a fresh application cache.
- **Navigation:** key written to the PTY until the frame shows the new cursor.
- **Held `j`:** 200 keys on a 25 ms ticker over all 10,000 items (not the short
  Next view), then release to the final cursor.
- **Filter:** the final query keystroke to a completed projection on screen.
- **External change:** from just before an atomic rename until the new title
  appears, so it includes filesystem-event delivery.
- **RSS:** the child's high-water mark across cache population, startup,
  interaction, and filtering.

### Idle soak

One hour at 10k after loading and a 10-second warm-up (BL-25):

| Metric | Result |
| --- | --- |
| CPU | 69.76 s over 3,600 s: 1.94% of one core |
| Goroutines | 21 → 21 |
| Post-GC heap | 74,544,840 → 74,545,920 B |
| Key to PTY afterward | 7.87 ms |

The remaining CPU is the two-second native stat sweep. The owner set the idle
limit at ≤ 2% of one core, not host-normalized (D26).

### Workspaces

Five independent 10k projects with colliding source and item IDs, in a 120×40
PTY, waiting for all five sources to complete:

```sh
BL_TUI_WORKSPACE_MEASURE=1 task cli:test PKG=./internal/tui -- -run '^TestWorkspaceMeasurements$' -count=1 -v -timeout=6m
```

| Cache | Shell | First content | Complete |
| --- | ---: | ---: | ---: |
| Cold, one run | 26.50 ms | 91.57 ms | 4.234 s |
| Warm, five runs, p95 | 27.70 ms | 91.43 ms | 1.034 s |

### Go-to

Update → worker → View over a synthetic 50k ID/title catalog, until `BL-50000`
is found:

```sh
task cli:test PKG=./internal/tui -- -run '^$' -bench '^BenchmarkGoTo50k$' -benchtime=20x -count=1
```

| Query | `B` | `BL` | `BL-` | `BL-5` | `BL-50` | `BL-500` | `BL-5000` | `BL-50000` |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| p95 ms | 5.632 | 5.851 | 6.036 | 6.469 | 6.808 | 6.713 | 6.690 | 7.757 |

### Readiness

`task bench` includes a 50k-item, 100k-edge shared-prerequisite graph for
`engine.Evaluate`, reporting time and allocations only.

## History

How the budgets were reached. Details are in each backlog task.

| Task | Change | Effect |
| --- | --- | --- |
| BL-25 | Skip FTS deletes for new cache rows, which made cold population quadratic | 10k cold list 62.9 s → 2.24 s |
| BL-47 | Encode cached items as one gob stream | 10k warm `ls` 308.6 → 135.4 ms |
| BL-48 | Cache in 128-item batches; share parsed items; 300 MiB soft memory target for the CLI | 50k CLI RSS under 400 MiB |
| BL-49 | Persistent filename index for `show` | `show` ~12 ms at 10k and 50k |
| BL-50 | Keep the filename index across atomic writes; WAL `NORMAL` sync for the disposable cache | 10k write 84.9 → 26.1 ms; 50k 319.8 → 38.7 ms |
| BL-54 | Publish the first 256 items before the full load | 10k first content: warm 193 → 31 ms; cold 2,239 → 47 ms |
| BL-55 | Standard GC heap growth in the TUI instead of the fixed CLI target | 50k navigation 31.69 → 9.36 ms |
| BL-56 | Filter completion values before sorting; collect matches before allocating | 50k filter 132.1 → 16.6 ms |
| BL-57 | Reuse the watched native source and stat-validate in memory | 10k external change 258.3 → 100.1 ms |
| BL-58 | Share immutable items between source and store; dedupe repeated body strings per refresh; 20% GC heap growth | TUI RSS 300 / 1,073 → 133 / 383 MiB |

Notes that still matter:

- BL-55 traded memory for latency; BL-58 then cut memory. Reimposing a fixed
  memory target without that reduction brings back GC thrashing.
- BL-58's body-string sharing depends on content. The fixture repeats bodies,
  so unique documents save less.
- A Darwin bulk-metadata prototype cut idle CPU to about 1% but needed unsafe
  platform ABI parsing, so it was discarded.

See also the [TUI stack spike](spikes/tui-stack.md) for the original Bubble Tea
and Markdown measurements.
