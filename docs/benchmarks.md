# Benchmarks

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

## Production TUI qualification (BL-25)

The opt-in `TestProductionMeasurements` and `TestProductionSoak` harnesses use
BL-12 seed-1 native files, the production project store, directory watcher,
projector and TUI `Model`, and BL-14's real PTY/emulator at 120×40 and 120 FPS.
They do not measure the spike's synthetic `List`. A measurement-only header
replaces one screen row to expose completed projections and cursor positions;
it does not add a row, change the input path, or render details synchronously.
The child applies the production 300 MiB soft Go memory target unless the caller
sets `GOMEMLIMIT`. HOME and XDG paths are isolated from user data.

```sh
BL_TUI_MEASURE=1 task cli:test PKG=./internal/tui -- -run '^TestProductionMeasurements$' -v -count=1 -timeout=90m
BL_TUI_MEASURE=1 task cli:test PKG=./internal/tui -- -run '^TestShellMeasurement$' -v -count=1
BL_TUI_PERF_SOAK=1h task cli:test PKG=./internal/tui -- -run '^TestProductionSoak$' -v -count=1 -timeout=75m
```

First-paint samples include test-child process launch, configuration and store
construction. Cold samples use a fresh application cache, not a purged OS page
cache; warm samples reuse a populated cache. Both initial shell and completed
list latencies are reported, so a fast loading screen cannot conceal slow data
availability. Startup has 20 samples per condition at 10k (the budgeted size).
Navigation has 100
PTY samples; in-process Update+View uses the existing shell fixture with
2,000 samples over 50,001 prepared rows (three groups), without file I/O.
Filter samples measure one final query keystroke through a completed projection
and PTY frame, not editor echo; there are 20 samples at each size. The external
change samples measure atomic rename through visible title replacement (20 at
10k), conservatively including notification delivery. Held navigation sends
200 `j` inputs on a 25 ms ticker and measures release to the final cursor frame.
All timings are report-only, never default CI thresholds.

The one-hour idle interval starts after native data and a projection have
arrived, followed by a 10-second warmup. CPU is process user+system time;
goroutines and live heap are sampled after GC at both endpoints. Diagnostics
go to an out-of-band file, not the terminal. A real key and observed PTY cursor
change after the interval prove continued input/output responsiveness. RSS is
per-child process high-water RSS, including load, projection and interaction,
not the parent fixture generator's memory. Darwin bytes and Linux KiB are
converted to MiB. Cold cache population and other host activity can affect
results; record these conditions rather than treating a synthetic core result
as full-application acceptance.

### Reference results

2026-10-22, D26 host: Apple M5 Max, 64 GiB RAM, Darwin 27.0.0 arm64,
Go 1.27.3, default `GOMAXPROCS=18`, runtime baseline `18b2ca5` plus this
harness and the cold-cache fix below. Other repository work and the idle-soak
child ran on the same host; these are observed reference-host results, not
isolated-host claims. The final full measurement passed in 93.149 s. The
startup and interaction portions can also be reproduced separately:

```sh
BL_TUI_MEASURE=1 task cli:test PKG=./internal/tui -- -run '^TestProductionMeasurements$/10000/startup$' -v -count=1 -timeout=45m
BL_TUI_MEASURE=1 task cli:test PKG=./internal/tui -- -run '^TestProductionMeasurements$/(10000|50000)/interaction$' -v -count=1 -timeout=30m
```

| TUI operation | Reference result | Budget / observation |
| --- | --- | --- |
| Warm first paint, 10k | Initial shell p95 26.84 ms; completed list p95 193.17 ms | ≤ 100 ms: meaningful list misses; [BL-54](../backlog/BL-54.md) |
| Cold first paint, 10k | Initial shell p95 25.47 ms; completed list p95 2,239.06 ms | ≤ 400 ms: shell paints before load, meaningful list misses; [BL-54](../backlog/BL-54.md) |
| Navigation Update+View, 50k | p95 0.109 ms, n=2,000 | ≤ 4 ms: passes |
| Navigation key-to-PTY, 50k | p95 34.92 ms, n=100 | ≤ 16 ms: misses; [BL-55](../backlog/BL-55.md) |
| Navigation key-to-PTY, 10k | p95 8.99 ms, n=100 | Baseline; §11's PTY budget applies at 50k |
| Held `j`, 10k, 40 Hz for 5 s | 200 keys in 5.001 s; final selection observed 7.77 ms after release | One 120 FPS frame = 8.33 ms: passes |
| Filter single keystroke-to-PTY, 10k / 50k | p95 9.28 / 158.09 ms, n=20 each | ≤ 30 / 80 ms: 10k passes, 50k misses; [BL-56](../backlog/BL-56.md) |
| External atomic rename-to-visible, 10k | p95 258.27 ms, n=20 | ≤ 150 ms: conservative end-to-end miss; [BL-57](../backlog/BL-57.md) |
| Peak TUI RSS, 10k / 50k | 295.56 / 688.19 MiB | ≤ 150 / 400 MiB: both miss; [BL-58](../backlog/BL-58.md) |
| One-hour idle, 10k | 69.760774 CPU s over 3,600.006 wall s; goroutines 21 → 21; post-GC heap 74,544,840 → 74,545,920 B; post-soak PTY input 7.87 ms | Heap (+1,080 B), goroutines and input are stable; CPU 1.94% of one core passes the owner-approved ≤ 2% limit (D26); no host normalization |

The owner approved the five linked measurement follow-ups above. BL-25 qualifies
the measurements and fixes the quadratic cold-cache work; it does not claim
that the remaining startup, PTY, filter, update or RSS budgets pass.

The held-key run explicitly changes from the short default Next view to all
10,000 items before sending keys: hitting the end of a 188-row ready view would
not measure input backlog. Every key is received through Bubble Tea's real PTY
input parser, and the final observed cursor is 200, not a timer-only assertion.
Cold cache population is included in completed-list time. A loading screen is
not reported as proof of usable backlog availability. The complete-list warm
miss and directory-change miss also cannot be inferred from the much faster
in-process viewport-only benchmark.

### First-content startup (BL-54)

2026-10-22, same D26 reference host: Apple M5 Max, 64 GiB, Darwin 27.0.0
arm64, Go 1.27.3, default GOMAXPROCS=18, seed 1, 120×40, 120 FPS.
Runtime baseline `76aed64` plus BL-54. No absolute timing assertion was added.

```sh
BL_TUI_MEASURE=1 task cli:test PKG=./internal/tui -- -run '^TestProductionMeasurements$/10000/startup$' -v -count=1 -timeout=45m
```

The production PTY run passed in 47.557 s with 20 subprocess samples per
condition (unmeasured cache population excluded from warm samples):

| 10k startup boundary (p95) | Cold | Warm |
| --- | ---: | ---: |
| Initial shell | 21.57 ms | 21.91 ms |
| First usable list | 46.66 ms | 30.93 ms |
| Complete source and list | 2,146.05 ms | 164.11 ms |

First usable content now explicitly requires a nonempty, completed projection
and prepared list, not a loading screen. Full completion additionally requires
complete source coverage; interaction and soak measurements still wait for that
boundary. Both first-content budgets pass, and the cold list appears before
full loading completes. **Complete-load latency has not met the first-content
thresholds** and is not relabeled as such.

The source worker first publishes at most 256 complete items in native filename
order. It reads at most two cached gob batches, validates each reused item's
current file identity, and parses missing or changed items directly. The partial
source badge remains visible; readiness, ranking and counts reflect only loaded
items, missing dependency targets are unknown, and later full results preserve
the selected item where possible. This is a usable partial list, not a promise
that the full default Next ranking is already known. Details and writes retain
full item data and authoritative version checks. A full load always follows,
including all stat validation, findings, cache refresh and FTS indexing. The
preview neither updates cache metadata nor changes the search generation.

### Filter latency (BL-56)

2026-10-22, D26 host: Apple M5 Max, 64 GiB, Darwin 27.0.0 arm64,
Go 1.27.3, default GOMAXPROCS=18, seed 1, 120×40, 120 FPS. Runtime
baseline `2d242ad` plus BL-56; the production child retains its default
300 MiB Go memory limit. No absolute timing assertion was added.

```sh
BL_TUI_MEASURE=1 task cli:test PKG=./internal/tui -- -run '^TestProductionMeasurements$/(10000|50000)/interaction$' -v -count=1 -timeout=30m
```

| Final filter keystroke to completed PTY list, 20 samples, p95 | Before | After | Budget |
| --- | ---: | ---: | ---: |
| 10k | 10.22 ms | 8.88 ms | ≤ 30 ms |
| 50k | 132.12 ms | 65.64 ms | ≤ 80 ms |

Completion filters used values before sorting, retaining declared-value order,
alias matching and deduplication. Completion scans and row preparation check
cancellation; the single latest-wins worker and request fences are unchanged.
Selective projections collect matching indices before allocating item storage,
and zero/one-result sorts no longer build a full-backlog ranking map. All work
remains off the UI update loop. Query correctness, canceled completions, rapid
50k editor requests and stale projection/preparation fencing pass with `-race`.
These results qualify only filter latency: navigation, external-update and RSS
misses reported by the same harness remain separate work, not passing budgets.

### Cold-cache correction

The original 10k cold completed-list p95 was 62,859.37 ms; the 50k initial
population was still running after 15 minutes. FTS5 stores `path UNINDEXED`,
but cache refresh deleted by path even for each **new** record before inserting
it. Each pointless deletion scanned all preceding search rows, making cold
population quadratic. New records now insert directly; changed existing records
still delete their previous search text in the same transaction. Cache version,
stat validation, query parity and atomic commit semantics are unchanged.
Existing native cache regression tests pass without modifications.

The final 10k cold completed-list p95 is 2,239.06 ms (20 samples). Initial cold
population plus first list at 50k completed in 12.98 s (one observation, not a
p95 or a separate startup-budget pass). Warm list availability and the other
misses above remain real; this correction does not declare them solved.

The one-hour soak ran on the pre-correction binary. Its warm unchanged-cache
path is unaffected by skipping deletes for new cache entries. The 10k native
watcher's specified two-second in-place-edit stat sweep accounts for recurring
background work. The owner clarified “about 0%” as ≤ 2% of one core at 10k,
retaining that two-second sweep; D26 and §11 record this decision. The numeric
CPU result is not relabeled as zero or hidden behind host-wide normalization.
A discarded Darwin bulk-metadata prototype reduced a short observation to about
1% of one core but did not justify adding unsafe platform ABI parsing; the
existing watcher remains unchanged.
