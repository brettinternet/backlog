# Development

## Setup

Install [mise](https://mise.jdx.dev/). Then, from the repository root:

```sh
mise install
task init      # toolchain, Go modules, Git hooks
```

`mise.toml` pins Go, Staticcheck, govulncheck, Task, Lefthook, Betterleaks, git-cliff, Worktrunk, Backlog.md, and Worklease.

## Build and run

```sh
task cli:build          # writes bin/bl
task cli:run -- --help  # run from source
```

## Checks

Use the smallest check that covers your change.

| Task | What it does |
| --- | --- |
| `task fix:staged` | Format staged Go files and re-stage them |
| `task check:staged` | Pre-commit hooks on staged files: gofmt and a secret scan. Run before committing. |
| `task test` | `go test ./...` |
| `task check` | gofmt, `go vet ./...`, Staticcheck, changelog generation |
| `task race` | `go test -race ./...` |
| `task security` | Betterleaks over the history, plus `govulncheck ./...` |
| `task bench` | Benchmarks against the fixture generator (BL-12). Report only. |
| `task ci` | The gate: security, check, test, race, smoke |
| `task backlog:next` | First ready To Do task from the primary checkout's backlog |

## Tests

These rules apply to every new or edited test.

- **Write tests only when they matter.** A test must catch a realistic regression. Cover each behavior once, at the lowest layer that computes it: format rules in `internal/format`, readiness in `internal/engine`, query semantics in `internal/query`.
- **Keep adapter tests narrow.** Tests in `internal/cli`, `internal/mcp`, and `internal/tui` cover argument handling, rendering, JSON shape, exit codes, and key routing, against fakes. Don't repeat engine rules there.
- **Golden files.** Put fixtures and golden files under the package's `testdata/`. Regenerate them with `-update`, and review the diff.
- **Fuzz untrusted input.** Parsers of input `bl` does not control get fuzz targets: item files, Backlog.md files, the query language, quick-add tokens, adapter frames, and provider JSON.
- **Time.** Inject clocks. Never `time.Sleep` to wait for something; poll for an observable condition with a bounded deadline.
- **Isolation.** Tests never read the user's real config, state, or cache. Use the shared test helper that points `HOME` and `XDG_*` at `t.TempDir()`. BL-1 adds it.
- **Parallelism.** Call `t.Parallel()` unless the test changes process-global state.
- **Real providers.** Real `bd`, `worklease`, GitHub, Linear, or Jira round trips are skipped unless `BL_PROVIDER_TESTS=1`. Everything else fakes the provider.
- **Benchmarks.** Write `Benchmark*` functions, or gate measurements behind `BL_BENCH_*` or `BL_TUI_*` variables. The default run never asserts absolute latency. Assert algorithmic guarantees with counters, such as "no file reads on navigation".
- **TUI.** Test the TUI through model `Update`/`View` with a fixed size and theme. Real-terminal checks use PTY tests or `scripts/tui-snapshot` (BL-16), which captures a tmux pane at a given size so agents can inspect a render without a human.
- **Flaky tests are bugs.** Fix the cause instead of retrying, widening tolerances, or skipping.

## Toolchain

The pinned Go in `mise.toml` is the only supported toolchain. The `go.mod` directive tracks its minor version. `bl` ships as binaries, and its packages are `internal/`.

To upgrade tools, run `task setup:upgrade`, review the `mise.toml` diff, and run `task ci`.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/). `scripts/check-commit-message` enforces them:

```text
feat(tui): add board layout
fix(format): preserve CRLF on edit
docs(backlog): complete BL-12
```

- Types: `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`, `revert`, `style`.
- Scopes are lowercase.
- Mark breaking changes with `!`.
- Backlog-only commits use `docs(backlog): …`. git-cliff omits them from the changelog.
