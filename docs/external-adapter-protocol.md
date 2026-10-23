# External source adapter protocol v1

**Frozen:** protocol major 1. The [v1 JSON Schema](external-adapter/v1/schema.json)
fixes request and response shapes. Additive optional fields are compatible; changing
required meanings, methods, limits or authorization requires a new major. Adapter
semver is independent of protocol major. This derives from Worklease v1.9.3's
stdio framing, bounds, negotiation and cancellation, without receipts or recovery.
It is a source interface, not a claim authority or a scheduler.

## Configure and check

```yaml
sources:
  - id: example
    type: external
    run: [/absolute/path/to/adapter, argument]
```

`run` is argv, never shell text. One process handles one configured source. Committed
entries remain inert until `bl trust`; changing the privileged config invalidates
trust. User-private config is trusted. The working directory is the control root.
This is **not a sandbox**: an approved adapter has the user's filesystem and network
privileges. The host supplies only PATH, HOME, USER, LOGNAME, LANG, LC_* and XDG home
variables and TMPDIR, not ambient provider tokens or Worklease credentials. Stderr
is drained and discarded; diagnostics never echo adapter payloads.

Build the read-only reference with `mise exec go -- go build -o /tmp/bl-reference-adapter ./cmd/bl-reference-adapter`.
Run `bl adapter check /tmp/bl-reference-adapter`. This is explicit execution consent,
not an implicit trust grant for project config. `--timeout 5s` (before EXEC) bounds
each request, at most one minute. `--json` uses the usual CLI envelope. Check performs
negotiation, capability validation, a bounded list and a read of its first item
when nonempty; it cannot certify a third
party's write implementation. The reference exposes `REF-1` at version `1` and has
no persistent state or write capability.

## Transport and limits

Stdin and stdout carry compact UTF-8 JSON-RPC 2.0 objects, each followed by LF.
No batches, blank lines, BOM, duplicate keys or unsolicited messages are allowed.
IDs are nonempty strings, unique for the process lifetime. Responses must match the
outstanding ID and contain exactly one of `result` and `error`. The host serializes
requests (one in flight) and does not log frames. Frame limit including LF is
1 MiB; serialized result limit is 786432 bytes. List pages contain at most 100 items.
Budget violations fail the source, never silently truncate it. Cursors are opaque,
source-scoped strings; a repeated cursor is a protocol failure.

The host bounds each operation by a five-second default timeout or the caller's
earlier deadline. A timeout/cancellation sends `$/cancelRequest` with `{ "id": ID }`
when possible, then terminates and reaps the child. A crashed or invalid process
is isolated from other sources. Subsequent operations restart after bounded
exponential backoff; successful recovery clears the source error. Source failures
flow through the existing engine source-error state and TUI error badge, retaining
old rows as stale. No I/O happens on the Bubble Tea update loop.

## Negotiation and methods

First request:

```json
{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolMajors":[1],"sourceId":"example"}}
```

Result:

```json
{"protocolVersion":1,"manifest":{"id":"example.adapter","version":"1.0.0","capabilities":["list","read"],"requiredFeatures":[]}}
```

`list` and `read` are mandatory. Unsupported selected major, missing mandatory
capabilities, or unknown required features refuse negotiation. No required features
are currently defined. Unknown optional manifest fields and capabilities do not
grant permission. `capabilities` returns `{ "capabilities": ["list", "read"] }`,
agreeing with the manifest. Every post-initialize request includes `sourceId` and a
finite RFC3339 `deadline`. Methods:

| Method | Additional params | Result |
| --- | --- | --- |
| `capabilities` | none | `capabilities`: string array |
| `list` | `cursor` (empty initially), `maxItems`: 100, `maxBytes`: 786432 | `items`, `nextCursor` (empty at end), `coverage` |
| `read` | `id` | `item` |
| `apply` | `mutation` | `item` |

Coverage is `complete`, `partial` or `unknown`. A finished partial scan must never
claim completeness or imply deletion. The host follows pages with bounded requests.
Items and mutations use the JSON representation of `internal/model` (exported Go
field names except explicit JSON tags; `Ref` is `{ "source": "example", "id": "X-1" }`).
Items require a nonempty `Version` and matching source; reads and writes must return
the requested item. Relations are source-qualified refs, never executable text.

Write capability names are model operations: `set`, `add`, `remove`, `unset`,
`replace-section`, `append-note`, `append-comment`, `check-criterion`,
`uncheck-criterion`, `add-criterion`, `remove-criterion`, `create`, `archive`,
`unarchive`, `batch`, `restore`. Absence means unavailable. Each mutation includes
`Ref`, `Op` and `ExpectedVersion`; only create may omit the expected version. The
adapter must enforce it atomically and report `conflict` rather than overwriting
newer data. Nested batch operations require the same authorization and item scope.
A dispatched write whose response is lost has `unknown-outcome`; it is never
retried automatically. Explicitly reread provider state before deciding what to do.

Errors use `{ "code": INTEGER, "message": STRING, "data": { "diagnostic": STRING } }`
in the JSON-RPC `error` field. Standard JSON-RPC codes retain their meanings;
provider diagnostics include `conflict`, `not-found`, `unsupported-capability`,
`unavailable-source`, and `unknown-outcome`. Host diagnostics are bounded and do
not expose provider bodies or stderr. Cancellation is a notification, not a request,
and receives no response. Adapters must stop provider work by the supplied deadline.

## Compatibility obligations

Do not put credentials in argv, config, items, errors, stdout or stderr. V1 defines
no host credential delivery method. Adapters manage their own private authentication.
New optional fields must preserve old interpretation. Unknown requested operations
or mutation fields must be rejected, not guessed. Publish a new schema path and
conformance fixtures before introducing a new major; never change v1 in place to
make a breaking adapter appear compatible.
