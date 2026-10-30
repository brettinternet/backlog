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
| `bl show ID` with 100k events, 10k ≤ 30 ms | 20.07 ms (15.20 ms without events, same run) | BL-136 |
| `bl show ID` with a held session's contact ≤ 30 ms | 25.0 ms with Herdr and Claude peers vs 25.9 ms before contact: median of 25 on the live repository, `worklease` on PATH directly rather than through the mise shim, which adds ~470 ms to both | BL-142 |
| Warm `bl history ID` with 100k events and 20 commits, 10k ≤ 30 ms | 21.78 ms (`show` with events 24.16 ms, same run) | BL-139 |
| Warm `bl ls` / `bl next`, 10k ≤ 150 ms | 132.6 / 107.7 ms | BL-48 |
| Native single-field write ≤ 50 ms | 10k 26.10 ms; 50k 38.66 ms | BL-50 |
| CLI peak RSS, 10k ≤ 150 MiB; 50k ≤ 400 MiB | 116.6 / 383.8 MiB (`ls`) | BL-48 |
| TUI warm first content, 10k ≤ 100 ms | 37.60 ms | BL-58 |
| TUI cold first content, 10k ≤ 400 ms | 54.50 ms, before the load completes | BL-58 |
| Navigation Update+View, 50k ≤ 4 ms | 0.081 ms | BL-58 |
| Warm History open Update+View, 1,120 entries ≤ 4 ms | 0.138 ms | BL-139 |
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
| `bl show BL-1`, 100k events | 20.07 ms | — | 37.36 MiB | — |
| `bl history BL-1`, 100k events, 20 commits | 21.78 ms | — | 39.86 MiB | — |
| Single-field write | 26.10 ms | 38.66 ms | 23.56 MiB | 26.84 MiB |

What each row measures:

- **Cold load:** native `Source.Load` without the SQLite cache, parsing every
  item. The OS page cache is warm; "cold" means no application cache.
- **Warm load:** `Source.Load` with a populated cache and stat validation.
- **`ls`, `next`, `show`, write:** a production `bl` subprocess, including
  launch, discovery, config, and output. `show` reads BL-1. Write runs
  `bl set BL-1 title=...` with hooks and Git off.
- **100k events:** `show` after seeding 100k events across rotated logs, 1,000
  of them for BL-1 from 100 sessions, so it reads the item's sessions through
  the event index's `ref` column (BL-136, 2026-10-29). The warm-up builds the
  index. Measured at 10k only.
- **History:** `bl history BL-1` on the same seeded fixture, after committing
  the fixture to Git and then committing 20 edits to BL-1, so it reads events,
  the session fold, comments, and Git history (BL-139, 2026-10-29). The warm-up
  fills the Git history cache. `show` with events measured 24.16 ms in the same
  run.
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

Update+View over 50,001 prepared rows: 0.081 ms (n=2,000).

A warm History open (BL-139) does no loading on the loop: the worker composes
history off it and caches it by item version and event-log position. On the
loop, the tab switch with its View, then applying the worker's page with its
View, measured 0.138 ms p95 (n=200) for BL-1's history in the 100k-event fixture
(1,000 events across 100 sessions, plus 20 commits: 120 rows collapsed).
Run it with `BL_TUI_MEASURE=1 task cli:test PKG=./internal/tui -- -run
'^TestHistoryMeasurement$' -v -count=1`. Held `j`: 200 keys in
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

### Native watcher

The production store over the native watcher at 10k, without a PTY, so it runs
on Windows too (BL-106). Latency runs from just before the file operation until
the store publishes the snapshot the TUI renders, n=20 each. The soak follows a
10-second settle.

```sh
BL_WATCH_MEASURE=1 BL_WATCH_SOAK=1h task cli:test PKG=./internal/source/native -- -run '^TestWatchMeasurement$' -v -count=1 -timeout=75m
```

| p95, 10k | Reference host (30 s soak) | Windows |
| --- | ---: | ---: |
| Create | 66.2 ms | — |
| In-place edit | 2,006 ms (sweep) | — |
| Atomic rename | 40.8 ms | — |
| Delete | 51.1 ms | — |
| Idle CPU | 1.77% of one core | — |
| Goroutines / post-GC heap | 6 → 6 / no growth | — |

macOS sees in-place edits only through the two-second sweep. Windows has no
sweep: `ReadDirectoryChangesW` reports the edit itself.

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

## Remote sync

A GitHub source refreshes with a change window between hourly reconciliations
([github.md](github.md#sync)). Adapter `Load` against `charmbracelet/vhs` (262
issues, no project), from the reference host over home broadband, BL-120:

| Load | Wall time | Requests |
| --- | ---: | ---: |
| Reconciliation (cold, or hourly) | 4.30 s | 6 |
| Change window, no changes (three runs) | 0.75–0.85 s | 3 |

The change window's request count doesn't grow with the repository, which the
fake-server test `TestChangeWindowSync` asserts. Most of its time is the
credential helper and three round trips.

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
| BL-120 | GitHub change window between hourly reconciliations | vhs warm refresh 4.30 → 0.80 s, 6 → 3 requests |
| BL-105 | Windows support: shared `filelock`, `atomicfile`, and per-platform file identity | No regression: 10k warm `ls` p95 142.2 / 147.5 ms against `main` 141.1 / 144.4 ms, alternating runs on the reference host (2026-10-29, a slower day than BL-48). Windows not yet measured. |

Notes that still matter:

- BL-55 traded memory for latency; BL-58 then cut memory. Reimposing a fixed
  memory target without that reduction brings back GC thrashing.
- BL-58's body-string sharing depends on content. The fixture repeats bodies,
  so unique documents save less.
- A Darwin bulk-metadata prototype cut idle CPU to about 1% but needed unsafe
  platform ABI parsing, so it was discarded.

See also the [TUI stack spike](spikes/tui-stack.md) for the original Bubble Tea
and Markdown measurements.
