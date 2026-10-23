# Development

## Setup

Install [mise](https://mise.jdx.dev/). Then, from the repository root:

```sh
mise install
task init      # toolchain, Go modules, Git hooks
```

`mise.toml` pins Go, Staticcheck, govulncheck, Task, Lefthook, Betterleaks,
git-cliff, Worktrunk, and Worklease. `task init` is safe to rerun, and
Worktrunk's pre-start hook runs it in new checkouts.

## Build and run

```sh
task cli:build          # writes bin/bl
task cli:run -- --help  # run from source
task cli:run            # open the empty TUI shell in a terminal
task cli:tidy           # reconcile go.mod/go.sum after changing imports
task cli:test PKG=./internal/project -- -count=1  # focused tests
```

## Checks

Use the smallest check that covers your change.

| Task | What it does |
| --- | --- |
| `task fix:staged` | Format staged Go files and re-stage them |
| `task check:staged` | Pre-commit hooks on staged files: gofmt and a secret scan. Run before committing. |
| `task test` | `go test ./...` and the Python script tests |
| `task check` | gofmt, module tidiness/integrity, `go vet ./...`, Staticcheck, changelog generation |
| `task race` | `go test -race ./...` |
| `task security` | Betterleaks over the history, plus `govulncheck ./...` |
| `task bench` | Benchmarks against the fixture generator (BL-12). Report only. |
| `task ci` | The gate: security, check, script tests, race, smoke. The race run covers `go test`. |
| `task backlog:next` | First ready native item from the primary checkout's backlog |

## Repository backlog

This repository's own tasks are native `backlog/BL-N.md` files. Agents follow
the claim protocol in AGENTS.md:

```sh
task backlog:cli -- instructions workflow
task backlog:cli -- next --claim --start --session SESSION --json
task backlog:cli -- heartbeat --session SESSION   # every ≤ 7 min
task backlog:cli -- ac BL-12 check 1 --evidence 'COMMAND: RESULT' --if-version VERSION
task backlog:cli -- done BL-12 --session SESSION
```

`task backlog:next` is read-only. Claims use Worklease's generic policy with
source `github.com/brettinternet/backlog`. Keep one session per loop; after an
uncertain result, reuse it to recover the pending request instead of picking new
work.

The wrapper builds committed primary `main`, never dirty files or the current
worktree, into `<git-common-dir>/backlog-cli/<key>/bl` under a build lock. The
key hashes the tree without `backlog/`, so task commits reuse the build. CLI
changes reach the shared backlog only after merging. `task backlog:test` checks
this.

Backlog.md is an import format here, not a dev dependency: its CLI, MCP,
status-as-claim, and `backlog-md:` launchers are disabled. Don't resume a
pre-cutover supervisor. The old backlog is in Git history; rolling back means
stopping all writers and reconciling native changes first.

## Tests

These rules apply to every new or edited test.

- **Write tests only when they matter.** A test must catch a realistic regression. Cover each behavior once, at the lowest layer that computes it: format rules in `internal/format`, readiness in `internal/engine`, query semantics in `internal/query`.
- **Keep adapter tests narrow.** Tests in `internal/cli`, `internal/mcp`, and `internal/tui` cover argument handling, rendering, JSON shape, exit codes, and key routing, against fakes. Don't repeat engine rules there.
- **Golden files.** Put fixtures and golden files under the package's `testdata/`. Regenerate them with `-update`, and review the diff.
- **Fuzz untrusted input.** Parsers of input `bl` does not control get fuzz targets: item files, Backlog.md files, the query language, quick-add tokens, adapter frames, and provider JSON.
- **Time.** Inject clocks. Never `time.Sleep` to wait for something; poll for an observable condition with a bounded deadline.
- **Isolation.** Tests never read the user's real config, state, or cache. Use `internal/testkit.Isolate(t)`, which points `HOME`, `USERPROFILE`, and XDG directories at `t.TempDir()` and clears `BL_*` and `WORKLEASE_*` overrides. Tests using it must not run in parallel.
- **Parallelism.** Call `t.Parallel()` unless the test changes process-global state.
- **Real providers.** Real `bd`, `worklease`, GitHub, Linear, or Jira round trips are skipped unless `BL_PROVIDER_TESTS=1`. Everything else fakes the provider.
- **Benchmarks.** Write `Benchmark*` functions, or gate measurements behind `BL_BENCH_*` or `BL_TUI_*` variables. The default run never asserts absolute latency. Assert algorithmic guarantees with counters, such as "no file reads on navigation".
- **TUI.** Test the TUI through model `Update`/`View` with a fixed size and theme. Real-terminal checks use PTY tests or `scripts/tui-snapshot` (BL-16), which captures a tmux pane at a given size so agents can inspect a render without a human.
- **Flaky tests are bugs.** Fix the cause instead of retrying, widening tolerances, or skipping.

## Toolchain

The pinned Go in `mise.toml` is the only supported toolchain. The `go.mod` directive tracks its minor version. `bl` ships as binaries, and its packages are `internal/`.

To upgrade tools, run `task setup:upgrade`, review the `mise.toml` diff, and run `task ci`.

## Releases

`task release:snapshot` builds unpublished archives in `dist/` for Darwin and
Linux on arm64 and amd64: a static `bl` with version, commit, and UTC build time, plus
`checksums.txt`. `task release:test` (also in `task ci`) tests the CI gate and
installer against local fixtures. Neither needs credentials or sibling
repositories.

Pushing a tag runs the release workflow. It publishes only if the latest
main-push `CI` run for that exact commit passed, checked before building and
again before publishing. If CI hadn't finished, rerun the workflow after it
passes. Notes come from git-cliff.

Owner checklist:

- [ ] Merge the release commit to `main` and wait for CI.
- [ ] Run `task ci`, `task release:snapshot`, and `task release:test`.
- [ ] Tag that commit with an unused `vMAJOR.MINOR.PATCH` (a `-PRERELEASE` suffix
  publishes a prerelease that the installer's `latest` skips) and push the tag.
- [ ] Check the notes, four archives, and checksums; after installing, check
  `bl version`.
- [ ] Optionally update the Homebrew tap (separate owner step).

Install (needs curl, tar, and sha256sum or shasum):

```sh
curl -fsSL https://raw.githubusercontent.com/brettinternet/backlog/main/install.sh -o /tmp/bl-install.sh
sh /tmp/bl-install.sh
BL_VERSION=v1.2.3 BL_INSTALL_DIR=/opt/bin sh /tmp/bl-install.sh
```

It verifies the archive's SHA-256 before installing to `$HOME/.local/bin` (add
it to `PATH`). `BL_RELEASE_BASE_URL` selects a mirror with GitHub's
`latest/download` and `download/TAG` layout; checksums catch corruption, not a
compromised mirror.

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
