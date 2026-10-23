# Remote source boundary

[GitHub Issues and Projects v2](github.md) and [Linear](linear.md) are available
through the configured source registry. `internal/source/remote` supplies their
shared scheduler, credentials, sanitization and SQLite cache. Jira remains upcoming. Native sources keep
their stat cache and directory watcher. The engine accepts both through the
existing source registry, without network I/O on the Bubble Tea update loop.

## Adapter contract

After checking the configured source's project trust declaration, wrap its
`source.Source` with `remote.New(backend, cache.Store, secrets)`. Share a
`remote.Secrets` and `remote.Credentials` across the client's adapters. Use the
same host and verified account principal for every `remote.Client` sharing a
quota. The default scheduler is process-wide; separate accounts run concurrently.

`Load(ctx, since)` receives the last committed opaque cursor. Its response uses:

- `Coverage`: complete, partial, or unknown.
- `Incremental`: changes since the cursor, rather than a full enumeration.
- `Stale`: refs whose freshness is unproven, even if their status is known.
- `Cursor`: the next resumable cursor; an empty cursor on an incomplete response
  preserves the previous cursor. Errors never commit rows or advance a cursor.

Only a fresh complete enumeration can delete unseen rows. Partial responses
retain unseen rows as stale; complete incremental responses keep unchanged rows.
An incremental response over an incomplete base cannot establish full coverage.
Rows, freshness, and cursors share one SQLite transaction. A per-source file lock
serializes the read/network/commit cycle across processes without holding a
SQLite transaction during network I/O. WAL warm readers do not take that lock.
Warm loading decodes at most two 128-row batches and marks every row stale.
An unavailable or damaged cache falls back to an uncursored provider load.
Nothing in the cache is required for correctness.

CLI lists, read-only and claimed `next`, and the Go To catalog accept partial
observations without claiming a complete inventory. Fresh rows can be candidates;
retained stale rows remain visible but never ready, and stale or missing blockers
remain unknown, including in workspace projections. Unknown coverage and error
findings or duplicate identities still fail these reads closed (including
malformed native files).
Archive requests are forwarded independently of the durable sync cursor; cached
archive rows do not leak into active-only lists. Event/deletion reconciliation
still requires complete coverage.

The wrapper prepares common Set/Batch field edits and provider-level inverses
without converting remote IDs or raw fields into native Markdown. Adapters
implementing other operations must supply their own preparation. Every provider
`Apply` must atomically enforce `ExpectedVersion` where the provider supports it,
not rely on the wrapper's preceding Read. APIs without compare-and-swap (GitHub and Linear)
require a fresh provider preflight immediately before a single mutation and must
document the remaining concurrent-write race. The framework never retries a write
whose remote outcome could be uncertain.

Optional `Delta.TextSearch` supplies text predicate candidates. Errors or missing
local matches disable the narrowing; the shared evaluator removes false
positives. Other predicates remain in-memory until an adapter exercises them.

## Credentials and HTTP

Accounts exist only in the user's config, not committed project config:

```yaml
accounts:
  github:
    - name: work
      host: github.com
      user: alice
      credential: [gh, auth, token, --hostname, github.com, --user, alice]
    - name: personal
      host: github.com
      user: bob
      env: BL_PERSONAL_GITHUB_TOKEN
```

Select exactly one helper argv or explicit environment variable per account.
`Credentials.Resolve` verifies the returned credential against `user` using the
provider's authenticated identity endpoint. Refresh immediately before writes.
Helpers run without a shell in a minimal environment, with a 15-second timeout,
4-KiB stdout/stderr limits, and descendant cancellation. Helper and verifier
errors never echo their output. Credential values stay in memory, format as
`[REDACTED]`, and are omitted from JSON. The remote source boundary redacts provider
rows, findings and errors before cache, event or hook consumers receive them;
a credential-bearing cursor is refused rather than replayed. Hook environments
retain the existing allowlist, never account environment variables.

`Client.Do` requires HTTPS on the configured host, explicit authorization, and
an account. GitHub uses Bearer authorization; Linear personal API keys use raw
authorization. It disables redirects, bounds responses to 8 MiB, consumes the body
inside the serialized account slot, and never exposes credential-bearing request
objects or raw transport errors. Queues are bounded and context-cancelable.
`Retry-After` seconds/HTTP dates, `X-RateLimit-Remaining/Reset` epoch headers,
`RateLimit-Remaining/Reset` relative headers, and Linear's
`X-RateLimit-Requests/Complexity-Remaining/Reset` millisecond epochs set the account retry deadline,
even on successful responses. Before that deadline requests fail with a
`ScheduleDiagnostic` carrying `RetryAt`, rather than sleeping or retrying writes.
GET ETags are memory-only and partitioned by account, credential, URL and Accept.
A 304 is not an empty enumeration: the adapter must reuse its representation,
or treat a conditional request only as a hint and independently enumerate (GitHub).

The scheduler and credential helper behavior derives from Worklease v1.9.3
`internal/queue/scheduler.go`, `credential_helper.go`, and the GitHub quota/ETag
paths. Receipt and recovery journals are intentionally absent (D24).

## Verification

`task cli:test PKG='./internal/source/remote ./internal/engine ./internal/tui ./internal/hooks'`
covers fake HTTPS quotas and account isolation, credential canaries, durable
cursors, stale dependency safety, bounded warm reads, source isolation and actual
TUI paint before a blocked network response. `BenchmarkRemoteWarmCache10k`
reports the warm read cost without an absolute timing assertion.
