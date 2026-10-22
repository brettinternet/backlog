# CLI benchmarks

## Reference host and reproduction

BL-12 reference run: 2026-10-22, Apple M5 Max, 64 GiB RAM, Darwin 27.0.0
arm64, Go 1.27.3, `GOMAXPROCS=18` (default). The owner approved replacing the
previous M1 Max/32 GiB reference host in D26. Runtime baseline: `ffa8f28`, plus
the BL-12 fixture/harness changes. No absolute timing assertions run in CI.

```sh
task bench                 # build production bin/bl; 20 samples per operation
task test                  # ordinary tests only; no benchmarks or large trees
```

To run just one measurement:

```sh
task bench -- -bench 'BenchmarkCLI/10000/show$'
```

Generate a standalone fixture project (the output directory must not exist):

```sh
mise exec go -- go run ./cmd/bl-fixtures -out /tmp/bl-fixture-example -count 10000 -seed 1
bin/bl -C /tmp/bl-fixture-example ls
```

The generator writes a minimal Git discovery directory, project config and N
native items. Seed 1 is used for both sizes. Statuses span all five categories,
priorities include unset, and labels and assignees include unassigned items.
Blockers include bounded chains and fan-out from BL-1; hierarchy depth reaches
four. Description sizes are 0, 256, 2,048, 8,192 and 102,400 bytes, with weighted
frequencies of 30%, 40%, 20%, 9% and 1%. Titles include CJK, emoji and combining
marks. All clocks and random state are explicit. The ordinary test hashes paths
and contents from independently generated trees, checks a different seed changes
the hash, parses every item and checks references exist without self-edges.
Generation refuses existing output paths. Temporary benchmark projects and all
HOME/XDG config, state and cache directories are isolated from user data.

## Measurement boundaries

- `cold-load`: native `Source.Load` with no SQLite cache; includes parsing all
  items, not configuration, ranking or process launch. It does **not** purge the
  OS page cache, and is warmed once before sampling. “Cold” means no application
  cache, not cold physical storage.
- `warm-load`: native `Source.Load` with a populated SQLite cache and stat
  validation, including cache open/decode. Initial population is excluded.
- `ls`, `next`, `show`, `write`: separate production CLI subprocesses, including
  process launch, discovery, configuration, rendering and output to a discard
  sink. `ls` and `next` use their default limits. `show` selects BL-1. `write`
  runs `bl set BL-1 title=...` with a distinct value each sample, using the
  normal version-checked atomic/fsynced write path, with hooks and Git disabled.
- Each operation receives one unmeasured warm-up. p95 is nearest-rank over the
  20 recorded wall-clock samples (the 19th sorted sample), **not** Go's mean
  `ns/op`. Go may also perform an initial calibration invocation.
- RSS is the maximum per-child high-water RSS across the recorded CLI samples,
  converted to MiB from Darwin bytes or Linux KiB. It is not the Go benchmark
  parent process's accumulated high-water mark. In-process load RSS is not
  reported; the §11 RSS comparison below covers production CLI processes.

Large cold cache populations can take many minutes; the harness allows 45
minutes for the whole run. Benchmark timing remains report-only. Background
work and filesystem cache state can affect results; reproduce on an otherwise
idle reference host before judging a regression. `task ci` includes ordinary
fixture tests but never runs `BenchmarkCLI`.

## Results

`task bench` completed successfully: `PASS`, package elapsed 2,242.742 s.
Each row contains 20 measured samples. Cache population dominates total runtime
and is excluded from the warm measurements. The 10k `ls` p95 includes background
validation activity during that portion of the run; even the preliminary run
(308.6 ms) missed 150 ms. These are observations, not a claim of host isolation.

| Operation | 10k p95 (ms) | 50k p95 (ms) | 10k peak CLI RSS (MiB) | 50k peak CLI RSS (MiB) |
| --- | ---: | ---: | ---: | ---: |
| Cold native load | 244.0 | 1,103 | — | — |
| Warm native load | 288.0 | 1,311 | — | — |
| `bl ls` | 574.6 | 1,529 | 182.5 | 806.3 |
| `bl next` | 309.4 | 1,379 | 166.0 | 752.5 |
| `bl show BL-1` | 14.61 | 48.23 | 16.17 | 32.44 |
| Single-field write | 37.84 | 95.25 | 17.27 | 33.98 |

### Every applicable CLI budget in design §11

| Budget | Reference result | Outcome / follow-up |
| --- | --- | --- |
| `bl show ID` ≤ 30 ms p95 | 10k 14.61 ms; 50k 48.23 ms | 10k pass; 50k miss: [BL-49](../backlog/tasks/bl-49%20-%20Meet-the-show-latency-budget-at-50k-items.md) |
| Warm `bl ls` and `bl next`, 10k ≤ 150 ms p95 | `ls` 574.6 ms; `next` 309.4 ms | Both miss: [BL-47](../backlog/tasks/bl-47%20-%20Meet-the-warm-ls-and-next-CLI-latency-budget.md) |
| Native single-field write ≤ 50 ms p95, excluding hooks and Git | 10k 37.84 ms; 50k 95.25 ms | 10k pass; 50k miss: [BL-50](../backlog/tasks/bl-50%20-%20Meet-the-single-field-write-latency-budget-at-50k-items.md) |
| Peak RSS, 10k ≤ 150 MiB; 50k ≤ 400 MiB | CLI maxima 182.5 MiB and 806.3 MiB (`ls`) | Both miss: [BL-48](../backlog/tasks/bl-48%20-%20Meet-the-CLI-peak-RSS-budgets-at-10k-and-50k.md); `next` also misses at both sizes |

Cold/warm native load and 50k `ls`/`next` timings are baselines without separate
CLI latency thresholds in §11. TUI first paint, navigation, held keys, filter,
file-change visibility and idle-soak budgets are not claimed by this CLI
harness; BL-14 and BL-25 cover those interfaces when implemented. CLI RSS is
measured here; TUI RSS still needs its own measurement. BL-12 establishes the
measurement and records misses rather than declaring the runtime budgets met.
