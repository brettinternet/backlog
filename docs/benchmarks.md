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

The original full `task bench` completed successfully: `PASS`, package elapsed
2,242.742 s. Each row contains 20 measured samples. Cache population dominates
total runtime and is excluded from the warm measurements. The 10k `ls` p95
includes background validation activity during that portion of the run; even
the preliminary run (308.6 ms) missed 150 ms. These are observations, not a
claim of host isolation.

BL-49 focused rerun on 2026-10-22 (Apple M5 Max, 64 GiB, Darwin 27.0.0 arm64,
Go 1.27.3, `GOMAXPROCS=18`):

```sh
task bench -- -bench 'BenchmarkCLI/(10000|50000)/show$'
```

The focused run passed with 20 samples at each size: 10k p95 12.30 ms and peak
CLI RSS 22.41 MiB; 50k p95 12.48 ms and peak CLI RSS 22.47 MiB. The harness's
unmeasured warm-up builds or validates the persistent filename index; a first
lookup after index creation or a directory change enumerates item filenames.
Every `show` still reads and parses its selected item directly from the source.
These measurements establish the pre-BL-48 `show` baseline.

BL-47 focused rerun on 2026-10-22 on the same reference host and toolchain:

```sh
task bench -- -bench 'BenchmarkCLI/10000/(ls|next)$'
```

The run passed in 48.218 s with 20 measured subprocess samples per operation:
10k `ls` p95 135.4 ms and peak RSS 204.1 MiB; `next` p95 115.6 ms and peak RSS
251.1 MiB. Both warm latency budgets pass. The cache now encodes parsed items
in one gob snapshot stream, amortizing type metadata across items. Each warm
load still enumerates files and validates every file's stat identity; changed
items are reparsed and FTS updates remain incremental. When any item changes,
the snapshot payload is re-encoded. Build identity invalidates older cache
encodings automatically. Query and ranking semantics are unchanged.

BL-48 final rerun on 2026-10-22 on the same reference host and toolchain,
with `GOGC` and `GOMEMLIMIT` unset (runtime baseline `8d41dbb` plus BL-48):

```sh
task bench -- -bench 'BenchmarkCLI/(10000|50000)/(ls|next|show|write)$'
```

The run passed in 1,236.889 s with 20 measured subprocess samples per operation.
The `ls`, `next` and `show` rows below use this final rerun; the in-process load
rows retain the original full-run measurements. CLI peak RSS meets both §11
budgets. The 10k warm `ls`/`next` latency budgets remain met. This run predates
integration of BL-50: writes measured 95.06/380.1 ms and 31.05/42.30 MiB at
10k/50k. The write row below uses the integrated rerun recorded below.

The cache now encodes bounded batches of 128 items rather than one whole-source
gob payload, avoiding simultaneous full-size encoded and decoded snapshots.
Cache result records point to parsed items rather than copying them; source
loads preallocate their result, read composition reuses the first source slice,
and CLI filtering/selection avoids redundant full-item copies. The search
closure retains only the cache generation, not its entire item snapshot.
Full item data, query behavior, stat validation and incremental FTS semantics
are unchanged. Existing cache encodings are invalidated by build identity.

`bl` also defaults Go's **soft** memory target to 300 MiB, leaving headroom for
SQLite and process overhead under the 400 MiB RSS budget. This bounds GC
headroom, not supported backlog size: the runtime may exceed the target when
live data requires it. An explicit standard `GOMEMLIMIT` setting takes
precedence, including `GOMEMLIMIT=off`; the measurements above use the shipped
default, not benchmark-only environment tuning. Timing remains report-only.

BL-50 focused rerun on 2026-10-22 on the same D26 reference host and toolchain:

```sh
task bench -- -bench 'BenchmarkCLI/(10000|50000)/write$'
```

The run passed in 13.361 s, with 20 measured production subprocess samples per
size: 10k write p95 29.05 ms and peak RSS 23.52 MiB; 50k p95 42.02 ms and peak
RSS 26.75 MiB. Both single-field write budgets pass. A same-host pre-change
rerun measured 84.86 ms at 10k and 319.8 ms at 50k: atomic replacement had
invalidated the BL-49 filename index on every write, rebuilding all SQL rows.

Directory stat changes now trigger a filename digest comparison. Unchanged
names retain the SQL index; membership or enumeration-order changes rebuild it.
Matching paths are always checked for regular-file type, and item contents are
still read directly and version-checked under the repository write lock before
the atomic/fsynced replacement. Directory scans do not sort filenames or stat
every item. Disposable SQLite WAL transactions use NORMAL synchronization;
source-file fsync and conflict checks are unchanged. Losing a recent cache
transaction after power loss only causes validation or rebuilding on the next
read. Hooks and Git remain disabled.

After integrating BL-50 and the rest of `main` into BL-48 (`ffe5b81`), the same
focused write command passed in 13.747 s: 10k p95 26.10 ms / 23.56 MiB and
50k p95 38.66 ms / 26.84 MiB. The write row below uses this integrated rerun;
all correctness, race, security and smoke checks also pass on the merged code.

| Operation | 10k p95 (ms) | 50k p95 (ms) | 10k peak CLI RSS (MiB) | 50k peak CLI RSS (MiB) |
| --- | ---: | ---: | ---: | ---: |
| Cold native load | 244.0 | 1,103 | — | — |
| Warm native load | 288.0 | 1,311 | — | — |
| `bl ls` | 132.6 | 815.0 | 116.6 | 383.8 |
| `bl next` | 107.7 | 736.4 | 113.1 | 340.8 |
| `bl show BL-1` | 12.29 | 15.15 | 22.48 | 22.84 |
| Single-field write | 26.10 | 38.66 | 23.56 | 26.84 |

### Every applicable CLI budget in design §11

| Budget | Reference result | Outcome / follow-up |
| --- | --- | --- |
| `bl show ID` ≤ 30 ms p95 | 10k 12.29 ms; 50k 15.15 ms | Both pass on the D26 reference host |
| Warm `bl ls` and `bl next`, 10k ≤ 150 ms p95 | `ls` 132.6 ms; `next` 107.7 ms | Both pass ([BL-47](../backlog/BL-47.md)) |
| Native single-field write ≤ 50 ms p95, excluding hooks and Git | 10k 26.10 ms; 50k 38.66 ms | Both pass ([BL-50](../backlog/BL-50.md)) |
| Peak RSS, 10k ≤ 150 MiB; 50k ≤ 400 MiB | CLI maxima 116.6 MiB (10k `ls`) and 383.8 MiB (50k `ls`) | Both pass ([BL-48](../backlog/BL-48.md)) |

Cold/warm native load and 50k `ls`/`next` timings are baselines without separate
CLI latency thresholds in §11. TUI first paint, navigation, held keys, filter,
file-change visibility and idle-soak budgets are not claimed by this CLI
harness; BL-14 and BL-25 cover those interfaces when implemented. CLI RSS is
measured here; TUI RSS still needs its own measurement. BL-12 establishes the
measurement and records misses rather than declaring the runtime budgets met.
