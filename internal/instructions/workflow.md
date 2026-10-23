# Agent workflow

Use one stable, distinct session for the whole loop. With coordination configured,
select, claim, and start in one operation; never treat a status change as a claim.
Plain `bl next --explain --json` is read-only.

```sh
bl next --claim --start --session loop-a --explain --json
bl show ID --json
bl heartbeat --session loop-a --json
```

Read the selected item's full intent, criteria, history, and file scope before
editing. Renew at least once per half TTL, including during long work. Use the
same session on subsequent calls, via BL_SESSION or the global --session flag.
Never invent resource keys; use the resource returned by show.

Claimed selection skips confirmed contention in one ranked snapshot, holds at
most one claim, and stops on an uncertain acquisition with unknown-outcome.
Keep the same session for recovery: a held open item is resumed without acquiring,
and a pending acquire replays its exact retained request. Unresolved ownership,
missing items, and closed held items refuse fresh selection. Do not change sessions
to bypass uncertainty. Inside a verified run, selection returns only the run's
item; bl never acquires, renews, or releases a run-managed claim.

MCP callers pass ownership explicitly on every call; server environment is not
caller ownership. The equivalent loop uses these tool arguments (substitute the
configured active/done status keys):

```json
{"tool":"next","arguments":{"claim":true,"start":true,"session":"loop-a"}}
{"tool":"show","arguments":{"id":"ID"}}
{"tool":"heartbeat","arguments":{"session":"loop-a"}}
{"tool":"update","arguments":{"id":"ID","version":"VERSION","fields":{"status":"done"},"session":"loop-a"}}
```

For supervised MCP calls, also pass run explicitly. Inspect the claim result and
transition.status and transition.claim, including on errors: a failed status write
retains acquisition, and a failed release leaves the status written. A successful
done finishes its caller-managed release before returning, so the same session
can select its next item.

If data.items is empty, inspect explanations rather than guessing readiness.
Substitute the selected ID for ID below. Substitute the opaque version from
data.item.version (show), or data.outcomes[0].item.version (each successful
write), for VERSION. Never reuse an old version after a write.

```sh
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

Finish every acceptance criterion and delivery obligation, including commits or
integration if required. Persist blockers and the exact next step if genuinely
blocked; do not mark incomplete work done. Reread the final item, then finish:

```sh
bl show ID --json
bl done ID --session loop-a --if-version VERSION --json
bl show ID --json
```

A successful done response has data.outcomes, status set to workflow.done,
category done for the default configuration, and a new version. Claimed start
requires workflow.start and done requires workflow.done. Continue the loop with
`bl next --claim --start --session loop-a --json` only after completion.
