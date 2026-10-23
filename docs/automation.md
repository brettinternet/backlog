# Actions, hooks, and agent handoff

## Actions

Run `bl action NAME ID` or MCP `action` with `name` and `id`. In the TUI,
`!` lists matching actions; `:` and `?` also include them. `when` uses the shared
item query, including readiness. Missing or unsafe placeholders disable an action
with the reason. Project actions require `bl trust`, just like hooks.

`confirm: true` prompts in the TUI and interactive CLI. Noninteractive CLI callers
must pass `--yes`; MCP callers must set `confirm: true`. Refusal never launches
the program. A non-detached TUI action releases and restores the terminal;
`detach: true` hands the prepared invocation to a background runner immediately.
All three adapters use the same hook executor, minimal environment, JSON stdin,
30-second execution limit and bounded result capture.

Action stdin is an **unlogged invocation context** with the event fields, a fresh
`id`, `name`, empty `type`, `origin: action`, item, resource when available, cause
and depth. It is not an extra persisted event type. The action environment also
includes `BL_EVENT_ORIGIN=action` so nested `bl` writes retain their origin.
Each execution appends one `action.finished` with outcome, exit code, duration,
and result, then dispatches matching after hooks. Each explicit invocation has a
new identity even when the item version is unchanged. Runs beyond causal depth 3
are refused. Cancellation still records the completed execution.

Action keys share the TUI keymap: exact and prefix conflicts fail startup. Use
`action:NAME` in user `keys` overrides to remap or unbind a configured action.

## Hooks

Hooks subscribe to appended events. Committed project hooks stay inert until the
configuration is trusted with `bl trust`; user-configured hooks are trusted.
Hooks run argv directly, never through a shell. A `before` hook can veto only
`item.created`, `item.updated`, and `item.archived` mutations. It receives the
proposed event as JSON on stdin; a nonzero exit or timeout rejects the write
with exit 5 and the hook's first stderr line. `before` hooks run in declaration
order, and the first failure wins. A hook ends when its process exits: `bl` stops
reading output that a backgrounded child still holds open one second later, and
the child keeps running.

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

The agent handoff hook chains after the `work` action and consumes its
`action.finished` event and JSON result from `wt --format=json`:

```yaml
actions:
  - name: work
    key: ctrl+x
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
bl next --claim --start --json
bl show 12 --json
bl heartbeat --json
bl done 12 --json
```

Heartbeat at least once per half TTL. Inspect separate status/claim outcomes on
failure: a failed status write retains the acquisition, and a failed release
leaves the status written. An occupied session reports `claim-held` rather than
contention. Plain `next` is read-only; `next --claim --start` resumes the session's
open item or walks one ranked snapshot, skipping contention. Uncertain acquire
stops the walk; recover with the same session rather than claiming another item.

## Supervised workers and worktrees

Launching agents and preparing worktrees are explicit action/hook examples,
not built-in `bl` behavior. Obtain the exact `resource` from `bl show --json`;
never substitute Worklease's host-local `backlog-md` key for a native item.
For runner integrations, the resource is the `{resource}` placeholder and
`BL_RESOURCE` value. Item/claim events carry the provider-neutral `resource`.
Both hooks and actions use the executor described above.

Independent worktree, agent, and Herdr workspace actions:

```yaml
actions:
  - name: worktree
    run: [wt, switch, --create, "{branch}", --base, main, --no-cd, --format=json]
  - name: agent
    run: [worklease, run, --detach, --name, "{id}", --ref, "{ref}", --resource, "{resource}", --, pi, -p, "Implement {id}"]
  - name: workspace
    run: [herdr, workspace, create, --cwd, "{root}", --label, "{id}", --no-focus]
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
