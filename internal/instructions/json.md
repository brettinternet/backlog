# JSON and exit codes

Request --json on commands used by automation. Parse exactly one envelope on
stdout; diagnostics belong on stderr. Check ok and the process exit code.
Do not parse human tables. Envelope schemaVersion is 1.

```json
{"schemaVersion":1,"ok":true,"command":"next","data":{"items":[]}}
{"schemaVersion":1,"ok":false,"command":"show","error":{"reason":"not-found","message":"item not found: 999","details":{}}}
```

Read summaries include source, id, title, status, category, priority, and version.
Show returns data.item plus metadata and sections; next returns data.items.
With --explain, next also includes explanations for included and excluded
candidates in a nonempty snapshot. Reasons include blocker, blocker-unknown,
excluded-label, query, and limit; inspect readiness and reasons, not just inclusion.

New and edit return the summary directly in data. Other writes return
data.outcomes in input order. Batches attempt all IDs and are atomic per item,
not per batch. On partial failure, ok is false but successful outcomes remain;
inspect each outcome before retrying. Guarded writes accept one item only.

Exit codes and reasons:
- 0: success, including no ready candidates.
- 1: internal/unclassified failure.
- 2: usage (including invalid query, command, or flag).
- 3: not-found.
- 4: conflict (stale version or occupied target).
- 5: rejected or invalid-config.
- 6: unavailable (unsupported source or incomplete load).
- 7: contention (reserved for coordination).

Session-required (2), claim-held/run-mismatch (4), and contention (7) are
coordination reasons; this foundation release has no claim operations.
Treat item versions as opaque strings and pass them verbatim to
--if-version whenever a write depends on previously read content.
