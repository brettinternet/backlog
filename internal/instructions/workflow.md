# Agent workflow

Use this foundation workflow only after satisfying the project's coordination
policy. There is no built-in claim operation yet: next is read-only and start
only changes status. This is not a safe concurrent-agent loop by itself.
Coordination/resource/transition guidance arrives with BL-32; the claimed,
stable-session CLI/MCP loop arrives with BL-33. Do not invent resource keys or
treat a status transition as a claim.

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
and a new version. Follow the external coordination policy to release any claim.
Start and done require configured workflow.start and workflow.done statuses.
