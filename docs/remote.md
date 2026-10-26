# Remote sources

[GitHub](github.md) and [Linear](linear.md) share the remote boundary in
`internal/source/remote`: one request scheduler per account, credential
resolution, redaction, and a SQLite cache. Jira isn't available yet. No network
I/O runs on the Bubble Tea update loop.

## Accounts

Accounts live only in user config, never in committed project config:

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
    - name: oauth-work
      host: github.com
      user: alice
      oauth: true
```

Each account uses exactly one of:

| Method | Notes |
| --- | --- |
| `credential: [argv]` | Helper runs without a shell, minimal environment, 15 s timeout, 4 KiB output limits. Its output is never echoed. |
| `env: NAME` | Read from that variable |
| `oauth: true` | `bl auth login`; see [OAuth](oauth.md). Never falls back to the other methods. |

The credential must authenticate as `user`; it's checked against the provider's
identity endpoint and refreshed before writes. Credentials stay in memory,
print as `[REDACTED]`, and are left out of JSON. Rows, findings, and errors are
redacted before reaching the cache, events, or hooks; a cursor containing a
credential is refused. Hook environments never get account variables.

## HTTP

- HTTPS to the configured host only; redirects off; responses capped at 8 MiB.
- GitHub uses Bearer authorization. Linear API keys use raw authorization;
  Linear OAuth uses Bearer.
- Each account has one bounded, cancelable queue. Separate accounts run
  concurrently.
- `Retry-After`, `X-RateLimit-Remaining/Reset`, `RateLimit-Remaining/Reset`,
  and Linear's `X-RateLimit-Requests/Complexity-Remaining/Reset` set a retry
  deadline, even on success. Until then, requests fail fast with a
  `ScheduleDiagnostic` carrying `RetryAt`; nothing sleeps or retries writes.
- ETags are memory-only, per account, credential, URL, and `Accept`. A `304` is
  not an empty list: the adapter reuses its data or enumerates anyway.

This scheduler and helper handling come from Worklease v1.9.3, without its
receipt and recovery journals (D24).

## Sync contract

`Load(ctx, since)` gets the last committed cursor and returns:

| Field | Meaning |
| --- | --- |
| `Coverage` | `complete`, `partial`, or `unknown` |
| `Incremental` | Changes since the cursor, not a full list |
| `Stale` | Refs whose freshness isn't proven |
| `Cursor` | Next cursor. Empty on an incomplete response keeps the old one. |

- Only a fresh, complete, full enumeration removes unseen rows. A partial
  response marks unseen rows stale; a complete incremental response keeps
  unchanged rows. An incremental response over an incomplete base can't become
  complete.
- Rows, freshness, and the cursor commit in one SQLite transaction. Errors
  commit nothing. A per-source file lock serializes read, network, and commit
  across processes, without holding a transaction during network I/O.
- A fresh `Read` replaces that item's cached row, or marks it stale on
  `not-found`, without moving the cursor. It skips the write while a refresh
  holds the lock rather than wait on the network.
- `Cursor.Reconcile` asks for a full scan instead of a change window for one
  load. The TUI sets it on `R`; adapters that always enumerate ignore it.
- A warm start decodes at most two 128-row batches and marks them stale. A
  missing or damaged cache falls back to a full provider load. Correctness never
  depends on the cache.

Reads accept partial data honestly. `ls`, `next` (plain or claimed), and go-to
use fresh rows; stale rows show but are never ready, and stale or missing
blockers are unknown, in workspaces too. Unknown coverage, error findings, and
duplicate IDs still fail the read. Archived rows load only when requested.
Events and deletion still need complete coverage.

## Writing an adapter

1. After checking trust, wrap the backend: `remote.New(backend, cache.Store, secrets)`.
2. Share one `remote.Secrets` and `remote.Credentials` across a client's
   adapters, and use one host and verified principal per `remote.Client` quota.
3. The wrapper prepares common Set and Batch edits and their inverses without
   touching raw fields. Other operations need their own preparation.
4. `Apply` must enforce `ExpectedVersion` atomically where the provider can.
   Without compare-and-swap (GitHub, Linear), reread immediately before one
   mutation, document the remaining race, and never retry an uncertain write.
5. Optionally return `Delta.TextSearch` candidates for `text:`; the evaluator
   rechecks them, and a failure or missed match disables narrowing.

## Tests

```sh
task cli:test PKG='./internal/source/remote ./internal/engine ./internal/tui ./internal/hooks'
```

Covers quotas and account isolation over fake HTTPS, credential canaries,
durable cursors, stale-dependency safety, bounded warm reads, source isolation,
and a real TUI paint while the network is blocked. `BenchmarkRemoteWarmCache10k`
reports warm read cost.
