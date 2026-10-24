# External source adapter protocol v1

**Frozen** at major version 1. The [v1 JSON Schema](external-adapter/v1/schema.json)
defines every message. Optional new fields are compatible; changing a required
meaning, method, limit, or authorization needs a new major. Adapter versions are
independent of the protocol version.

An adapter is a source, not a claim authority or scheduler. The protocol derives
from Worklease v1.9.3's stdio framing, bounds, negotiation, and cancellation,
without receipts or recovery.

## Configure and check

```yaml
sources:
  - id: example
    type: external
    run: [/absolute/path/to/adapter, argument]
```

```sh
mise exec go -- go build -o /tmp/bl-reference-adapter ./cmd/bl-reference-adapter
bl adapter check /tmp/bl-reference-adapter
```

- `run` is argv, never shell text. One process serves one source, started in the
  control root.
- Project-declared adapters do nothing until `bl trust`; changing them needs
  re-trust. User config is trusted.
- **It isn't a sandbox.** An approved adapter has your file and network access.
  It gets only `PATH`, `HOME`, `USER`, `LOGNAME`, `LANG`, `LC_*`, XDG home
  variables, and `TMPDIR`; no provider tokens or Worklease credentials.
- Stderr is drained and discarded. Diagnostics never echo adapter output.

`bl adapter check` runs an adapter on your explicit say-so without trusting
project config. It negotiates, validates capabilities, lists one bounded page,
and reads the first item. `--timeout` (default 5 s, at most 1 min) bounds each
request. It can't certify writes. The read-only reference adapter exposes `REF-1`
at version `1`.

## Transport

| Rule | Value |
| --- | --- |
| Framing | One compact UTF-8 JSON-RPC 2.0 object per line, LF-terminated |
| Not allowed | Batches, blank lines, BOM, duplicate keys, unsolicited messages |
| IDs | Nonempty strings, unique for the process lifetime |
| Concurrency | One request in flight |
| Frame size | ≤ 1 MiB including LF |
| Result size | ≤ 786,432 bytes |
| List page | ≤ 100 items |
| Timeout | 5 s per request, or the caller's earlier deadline |

A response must match the outstanding ID and have exactly one of `result` or
`error`. Exceeding a limit fails the source; nothing is truncated. Cursors are
opaque strings; a repeated cursor is a protocol error. Frames aren't logged.

On timeout or cancellation, the host sends `$/cancelRequest` with `{"id": ID}`
when it can, then kills and reaps the process. A crashed or invalid adapter
affects only its source: the TUI shows an error badge, old rows stay as stale,
and the next operation restarts it after bounded exponential backoff.

## Negotiation

```json
{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolMajors":[1],"sourceId":"example"}}
```

```json
{"protocolVersion":1,"manifest":{"id":"example.adapter","version":"1.0.0","capabilities":["list","read"],"requiredFeatures":[]}}
```

`list` and `read` are mandatory. An unsupported major, a missing mandatory
capability, or an unknown required feature (none exist yet) refuses the
adapter. Unknown optional fields and capabilities grant nothing.

## Methods

Every request after `initialize` includes `sourceId` and an RFC 3339 `deadline`.

| Method | Params | Result |
| --- | --- | --- |
| `capabilities` | — | `{"capabilities": [...]}`, matching the manifest |
| `list` | `cursor` (empty first), `maxItems: 100`, `maxBytes: 786432` | `items`, `nextCursor` (empty at end), `coverage` |
| `read` | `id` | `item` |
| `apply` | `mutation` | `item` |

`coverage` is `complete`, `partial`, or `unknown`; a partial scan never claims
completeness or implies deletion.

Items and mutations use the JSON form of `internal/model` (exported Go field
names unless a JSON tag says otherwise; `Ref` is `{"source": "example", "id":
"X-1"}`). Items need a nonempty `Version` and the right source, and `read` and
`apply` must return the requested item. Relations are source-qualified refs.

Write capabilities are model operations: `set`, `add`, `remove`, `unset`,
`replace-section`, `append-note`, `append-comment`, `check-criterion`,
`uncheck-criterion`, `add-criterion`, `remove-criterion`, `set-criterion`,
`create`, `archive`, `unarchive`, `batch`, `restore`. Missing means unavailable.

Each mutation has `Ref`, `Op`, and `ExpectedVersion` (only `create` may omit
it). The adapter must check the version atomically and return `conflict` rather
than overwrite newer data. Nested batch operations need the same capability and
item scope. If a write's response is lost, the outcome is `unknown-outcome`;
nothing retries, so reread before deciding what to do.

## Errors

```json
{"jsonrpc":"2.0","id":"7","error":{"code":-32000,"message":"stale version","data":{"diagnostic":"conflict"}}}
```

Standard JSON-RPC codes keep their meanings. Diagnostics: `conflict`,
`not-found`, `unsupported-capability`, `unavailable-source`, `unknown-outcome`.
The host recognizes `not-found` by code `-32007`.
Host diagnostics are bounded and never include provider bodies or stderr.
Cancellation is a notification with no response; adapters must stop by the
deadline.

## Compatibility obligations

- Never put credentials in argv, config, items, errors, stdout, or stderr. v1
  has no host credential delivery; adapters handle their own authentication.
  Use an adapter-owned private store or helper. Host-managed OAuth needs an
  [eligible consumer and new major](oauth.md#jira-and-external-adapter-boundary),
  not an optional capability.
- New optional fields must keep old meanings.
- Reject unknown operations and mutation fields; never guess.
- A new major needs a new schema path and conformance fixtures first. Never
  change v1 in place to make a breaking adapter look compatible.
