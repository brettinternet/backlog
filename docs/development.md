# Development

## Setup

Install [mise](https://mise.jdx.dev/). Then, from the repository root:

```sh
mise install
task init      # toolchain, Go modules, Git hooks
```

`mise.toml` pins Go, Staticcheck, govulncheck, Task, Lefthook, Betterleaks, git-cliff, Worktrunk and Worklease. `task init` is repeatable: it downloads modules and installs/updates hooks without uninstalling them first. Worktrunk's pre-start hook runs the same initialization for new checkouts.

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
| `task test` | `go test ./...` |
| `task check` | gofmt, module tidiness/integrity, `go vet ./...`, Staticcheck, changelog generation |
| `task race` | `go test -race ./...` |
| `task security` | Betterleaks over the history, plus `govulncheck ./...` |
| `task bench` | Benchmarks against the fixture generator (BL-12). Report only. |
| `task ci` | The gate: security, check, test, race, smoke |
| `task backlog:next` | First ready native item from the primary checkout's backlog |

## Repository backlog

This repository tracks work in native `backlog/BL-N.md` files, not the import
provider. From any checkout, use `task backlog:cli -- instructions workflow`
and follow the repository's AGENTS.md claim protocol. `task backlog:next` is
read-only; `task backlog:cli -- next --claim --start --session SESSION --json`
selects, claims, and starts. Native coordination is enabled and uses Worklease's
generic policy with source `github.com/brettinternet/backlog`, never the
`backlog-md` policy. Keep one stable distinct session per loop, heartbeat through
`task backlog:cli -- heartbeat --session SESSION` at least every 7 minutes and
before half TTL, and finish through `task backlog:cli -- done ID --session SESSION`.
Done waits for caller-managed release. On uncertainty, retain the session and
recover its pending request rather than selecting new work.

The wrapper builds an archived snapshot of committed primary `main`, not dirty
primary files or the invoking worktree, into
`<git-common-dir>/backlog-cli/<key>/bl` under a cross-process build lock. The
key hashes the committed tree without `backlog/`, so task-state commits reuse the
build, and a new build removes older ones.
Primary must be on `main`. Changing CLI code requires merging it before the
shared backlog can use it. `task backlog:test` checks this build boundary.
Acceptance checks use `ac ID check N --evidence 'COMMAND: RESULT'`; use the
version from `show --json` with `--if-version` for index-based changes.

Legacy Backlog.md CLI/MCP, status-only claiming, and launchers deriving
`backlog-md:` keys are disabled locally. Do not resume a pre-cutover supervisor;
start a fresh native loop using AGENTS.md. Backlog.md remains an import/adapter
format supported by the product, not a development dependency. The pre-cutover
backlog remains recoverable from Git history; any rollback requires stopping all
writers and reconciling native changes first, not starting both providers.

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

`task release:snapshot` builds nonpublishing archives in `dist/` for Darwin and
Linux, arm64 and amd64. Each contains a static `bl` with version and full commit
metadata; `checksums.txt` records SHA-256 hashes. `task release:test` exercises
the fail-closed CI gate and installer against local fixtures. It also runs in
`task ci`. No sibling repositories or release credentials are needed locally.

The tag-triggered release workflow checks the latest main-push `CI` run for the
exact tag commit before building and again immediately before publishing. Missing,
pending, failed, wrong-commit, fork, and pull-request runs cannot authorize a
release. It generates notes with git-cliff and publishes the four archives and
checksums. If CI has not finished, rerun the release workflow after CI passes.

Owner checklist:

- [ ] Merge the intended release commit to `main` and wait for its CI to pass.
- [ ] Run `task ci`, `task release:snapshot`, and `task release:test` locally.
- [ ] Choose an unused `vMAJOR.MINOR.PATCH` tag (optional `-PRERELEASE` suffix,
  published as a GitHub prerelease that the installer's `latest` skips),
  create it on that exact commit, and explicitly push the tag to trigger release.
- [ ] Inspect the GitHub release notes, four archives, and checksums; verify
  `bl version` reports the intended version and commit after installation.
- [ ] Optionally update the Homebrew tap with the published archive URLs and
  checksums. This is a separate owner action, not part of this pipeline.

Install a published release (requires curl, tar, and sha256sum or shasum):

```sh
curl -fsSL https://raw.githubusercontent.com/brettinternet/backlog/main/install.sh -o /tmp/bl-install.sh
sh /tmp/bl-install.sh
```

The installer verifies the selected archive's SHA-256 before extraction or
replacement. Defaults: latest release, `$HOME/.local/bin`. Set `BL_VERSION=v1.2.3`
and/or `BL_INSTALL_DIR` to override. `BL_RELEASE_BASE_URL` selects a trusted release
mirror using GitHub's `latest/download` and `download/TAG` layout; checksums protect
against corruption, not a compromised mirror. Add the install directory to PATH.

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
