# Hooks and agent handoff

Hooks subscribe to appended events. Committed project hooks stay inert until the
configuration is trusted with `bl trust`; user-configured hooks are trusted.
Hooks run argv directly, never through a shell. A `before` hook can veto only
`item.created`, `item.updated`, and `item.archived` mutations. It receives the
proposed event as JSON on stdin; a nonzero exit or timeout rejects the write
with exit 5 and the hook's first stderr line. `before` hooks run in declaration
order, and the first failure wins.

`after` hooks are handed to a detached `bl` runner by the process that appended
the event. Hooks for one event run concurrently with a bound of four. Chain
dependent work with `hook.finished` and `if: "hook:NAME outcome:ok"`. Each
started after hook appends one `hook.finished` event with its outcome (`ok`,
`failed`, `timeout`, or `refused`), exit code, duration, and result. No hook runs
for events deeper than 3; `bl` subprocesses inherit the cause and depth so the
same limit applies to nested writes.

`cwd` defaults to the directory of the originating command and stays the same
through event chains; sync-originated chains default to the control root. An
explicit `cwd` may use placeholders. A hook's stdout is captured. It may write
one JSON object of at most 8 KiB to `BL_RESULT`; that file takes precedence. If
the file is empty, a single JSON object of at most 8 KiB on stdout is the result.
Malformed or oversized result files fail the hook; ordinary non-JSON stdout is
not a result.

Placeholders work inside each argv element and `cwd`: `{id}`, `{ref}`,
`{source}`, `{url}`, `{root}`, `{branch}`, `{resource}`, and
`{result.KEY}`. `result.KEY` is a top-level string in the triggering event's
result. Missing values, control characters, leading `-`, and values longer
than 1 KiB refuse the hook before it runs. Metacharacters are passed literally;
`bl` does not interpolate or invoke a shell.

The hook process receives only `PATH`, `HOME`, `XDG_CONFIG_HOME`,
`XDG_STATE_HOME`, `XDG_CACHE_HOME`, and these `bl` variables:
`BL_ITEM_ID`, `BL_ITEM_REF`, `BL_SOURCE`, `BL_PROJECT_ID`, `BL_ROOT`,
`BL_BRANCH`, `BL_RESOURCE` (when available), `BL_EVENT_ID`, `BL_HOOK_DEPTH`,
and `BL_RESULT`. Credentials, session authority, and unrelated environment
variables are not inherited. The triggering event is JSON on stdin.

A readiness notification can use the shared query language and `{id}`:

```yaml
hooks:
  - name: notify-ready
    on: item.readiness
    if: "is:ready"
    run: [tell, notify, "Ready: {id}"]
```

The agent handoff hook chains after the `work` action. BL-31 supplies action
execution; this hook consumes its `action.finished` event and the JSON result
from `wt --format=json`:

```yaml
actions:
  - name: work
    key: x
    when: "is:ready"
    run: [wt, switch, --create, "{branch}", --base, main, --no-cd, --format=json]
hooks:
  - name: agent
    on: action.finished
    if: "action:work outcome:ok"
    cwd: "{result.path}"
    run: [worklease, run, --detach, --name, "{id}", --ref, "{ref}", --resource, "{resource}", --, pi, -p, "Implement {id}"]
    timeout: 30s
```

Hook behavior and these notification/agent command shapes are exercised in
`internal/hooks` tests with fake `tell`, `wt`, and `worklease` programs. The
agent example requires coordination to provide `{resource}`; without it, the
hook is refused instead of inventing a key.

## Coordination and agent handoff

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
Hook execution is covered above; action execution is delivered separately.

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
