# Agent workflow

Use one stable, distinct session for the whole loop. Use the harness's own
session or loop-run ID (for example CLAUDE_CODE_SESSION_ID or PI_SESSION_ID)
unchanged as SESSION, with no prefix or suffix, so the recorded claim leads back
to a transcript; generate a UUID only when the harness has none. With
coordination configured, select, claim, and start in one operation; never treat
a status change as a claim.
Plain `bl next --explain --json` is read-only.

```sh
bl next --claim --start --session SESSION --explain --json
bl show ID --json
bl heartbeat --session SESSION --json
```

Read the selected item's full intent, criteria, history, and file scope before
editing. Renew at least once per half TTL, including during long work. Wrap any
command that may block longer than that, such as a full test gate, so the claim
renews while it runs; bl exits with the command's status:

```sh
bl heartbeat --session SESSION -- task ci
```

Use the
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
caller ownership. Also pass agent (your kind: claude, codex, or pi) so events
record which agent ran the session; agentSession defaults to session. The
equivalent loop uses these tool arguments:

```json
{"tool":"next","arguments":{"claim":true,"start":true,"session":"SESSION","agent":"claude"}}
{"tool":"show","arguments":{"id":"ID"}}
{"tool":"heartbeat","arguments":{"session":"SESSION"}}
{"tool":"update","arguments":{"id":"ID","version":"VERSION","operation":"done","session":"SESSION","agent":"claude"}}
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
bl ac ID check 1 --dod --evidence "Definition-of-done check passed." --if-version VERSION --json
```

Write plan or summary sections without an editor, and reword an entry in place:

```sh
bl edit ID --section summary --text "What changed and how it was verified." --if-version VERSION --json
bl ac ID set 2 "Clearer criterion text." --if-version VERSION --json
```

A stale version exits 4 without writing. Reread the item and reassess the
operation; do not blindly retry. Every successful write returns a new version.

Finish every acceptance criterion and delivery obligation, including commits or
integration if required. Persist blockers and the exact next step if genuinely
blocked; do not mark incomplete work done.

Before done, before release, and at the end of any unclaimed phase on an item,
such as PR follow-up or review, record a short handoff for the next person or
agent: what you did and tried, the commands that reproduce your state, and the
next step. It needs your session but no claim; the newest handoff per session
wins.

```sh
bl handoff ID "Done: X. Reproduce: task ci. Next: Y." --session SESSION --json
```

Reread the final item, then finish:

```sh
bl show ID --json
bl done ID --session SESSION --if-version VERSION --json
bl show ID --json
```

A successful done response has data.outcomes, a done-category status, and a new
version. Start and done resolve per item source: the workflow key if the source
accepts it, else its only active or done status, else its default; otherwise
they are refused, naming the source, before any claim. Continue the loop with
`bl next --claim --start --session SESSION --json` only after completion.
