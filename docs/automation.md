# Coordination and agent handoff

Coordination is optional (`coordination: {worklease: {}}`) and uses Worklease's
schema-v2 CLI. See [provider evidence and recovery](cli.md#provider-evidence-and-recovery).
Never bypass uncertainty by choosing a new session.

## Caller-owned loops

Use one stable session across checkouts. `bl` invokes the coordinator from the
shared control root, not the invoking worktree. Pass authority/account selection
normally, but manage contextual claims through `bl` rather than calling Worklease
from a linked checkout with a different contextual handle root.

```sh
export BL_SESSION=agent-loop-a
bl show 12 --json
bl start 12 --json
bl heartbeat --json
bl done 12 --json
```

Heartbeat at least once per half TTL. Inspect separate status/claim outcomes on
failure: a failed status write retains the acquisition, and a failed release
leaves the status written. An occupied session reports `claim-held` rather than
contention. `next` is still read-only; claimed selection is separate BL-33 work.

## Supervised workers and worktrees

Launching agents and preparing worktrees are explicit action/hook examples,
not built-in `bl` behavior. Obtain the exact `resource` from `bl show --json`;
never substitute Worklease's host-local `backlog-md` key for a native item.
For runner integrations, the resource is the `{resource}` placeholder and
`BL_RESOURCE` value. Item/claim events carry the provider-neutral `resource`.
Hook/action runner support remains in its own delivery tasks.

Illustrative action argv:

```yaml
actions:
  - name: worktree
    run: [wt, switch, --create, "{branch}", --base, main, --no-cd, --format=json]
  - name: agent
    run: [worklease, run, --detach, --name, "{id}", --ref, "{ref}", --resource, "{resource}", --, pi, -p, "Implement {id}"]
```

A worker inherits a session and run identifier from its supervisor. `bl` verifies
the live run and its exact resource membership before status writes, then never
acquires, renews, or releases the supervisor's claim. A bare run ID or a session
name beginning with `run-` is not proof.

For a long-lived MCP server, pass `session` and optional `run` on **every**
claim-affecting call. Its inherited environment and preceding calls never select
caller ownership. Missing or mismatched context refuses side effects.

## Recovery

Offline handle inspection is evidence about retained state, not live ownership.
The coordinator distinguishes absent, held, pending, and unresolved state. Only
verified absence permits fresh acquisition. A pending acquire resumes using an
opaque adapter reference and Worklease's retained exact request; `bl` stores no
recovery journal or credential and never reconstructs request options.

Worklease 1.9.3's remote bare-handle acquire supports exact replay. Its local
inspection does not expose original options, so local recovery can require the
original Worklease command. Corrupt, stale, foreign, changed, or redacted state
must be investigated in the same session, not converted into an empty session.
