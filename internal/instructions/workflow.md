# Agent workflow

Follow the project's coordination policy. Next remains read-only. When
coordination is configured, start claims before writing and done writes then
releases the caller's matching claim. Use one stable explicit session via
BL_SESSION, or the global --session flag. Never invent resource keys: use show's
resource. If ownership is uncertain, retain the same session and recover with Worklease
rather than attempting a fresh acquire.

```sh
bl claim ID --session loop-a --json
bl heartbeat --session loop-a --json
bl release ID --session loop-a --json
```

A verified run-managed claim is never acquired, renewed, or released by bl.
MCP calls require session and optional run explicitly; server environment is not
caller ownership. Inspect both transition.status and transition.claim after a
status change, even on errors: a failed write retains acquisition, and a failed
release leaves the status written.

Pick a ready item and read its full intent, criteria, history, and file scope:

```sh
bl next --explain --json
bl show ID --json
```

If data.items is empty, inspect explanations rather than guessing readiness.
Substitute the selected ID for ID below. Substitute the opaque version from
data.item.version (show), or data.outcomes[0].item.version (each successful
write), for VERSION. Never reuse an old version after a write.

```sh
bl start ID --if-version VERSION --json
bl note ID "Plan: implement the full item and verify every criterion." --if-version VERSION --json
```

Implement the entire item and run its required checks. Read criteria again
before using their one-based indices. Record concrete evidence under each
criterion, not in Notes by number:

```sh
bl show ID --json
bl ac ID check 1 --evidence "Required verification command passed." --if-version VERSION --json
```

A stale version exits 4 without writing. Reread the item and reassess the
operation; do not blindly retry. Every successful write returns a new version.
For example, a start response has this shape (versions are opaque placeholders):

```json
{"schemaVersion":1,"ok":true,"command":"start","data":{"outcomes":[{"id":"ID","item":{"source":"tasks","id":"BL-1","title":"Example","status":"in-progress","category":"active","priority":"","version":"NEW_VERSION"}}]}}
```

Finish every acceptance criterion and delivery obligation, including commits or
integration if the project requires them. Persist blockers and the exact next
step if genuinely blocked; do not mark incomplete work done. Reread the final
item, then finish using its current version:

```sh
bl show ID --json
bl done ID --if-version VERSION --json
bl show ID --json
```

A successful done response uses the same outcomes shape, with command "done",
status set to workflow.done, category "done" for the default configuration,
and a new version. Follow the project coordination policy for any external claim.
Start and done require configured workflow.start and workflow.done statuses.
