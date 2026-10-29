# Actions, hooks, and agent handoff

Actions run on request. Hooks run when [events](events.md) happen. Both run argv
directly, never through a shell, and project-declared ones do nothing until
`bl trust`.

## Example: worktree and agent handoff

The `work` action creates a worktree. When it succeeds, the `agent` hook starts
a claimed agent in it, using the path from `wt`'s JSON output:

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

`{resource}` needs coordination; without it the hook is refused rather than
invent a key. A readiness notification:

```yaml
hooks:
  - name: notify-ready
    on: item.readiness
    if: "is:ready"
    run: [tell, notify, "Ready: {id}"]
```

Independent actions for a worktree, an agent, and a Herdr workspace:

```yaml
actions:
  - name: worktree
    run: [wt, switch, --create, "{branch}", --base, main, --no-cd, --format=json]
  - name: agent
    run: [worklease, run, --detach, --name, "{id}", --ref, "{ref}", --resource, "{resource}", --, pi, -p, "Implement {id}"]
  - name: workspace
    run: [herdr, workspace, create, --cwd, "{root}", --label, "{id}", --no-focus]
```

Tests run these examples with fake `tell`, `wt`, `worklease`, and `herdr`.

## Actions

```sh
bl action work 12 --yes
bl action work 12 --dry-run
```

Run from the CLI, MCP `action` (`name`, `id`, `confirm`), or the TUI `!` menu,
palette, or `key:`. An action is offered only when its `when` query matches the
item; missing or unsafe placeholders disable it with a reason. `--dry-run`
(MCP `dryRun: true`) prints the expanded argv, cwd, and environment names, or
that reason, without running, logging, or confirming.

| Field | Meaning |
| --- | --- |
| `name` | Identifier; also `action:NAME` in user `keys` |
| `run` | Argv |
| `when` | Item query |
| `cwd` | Working directory (placeholders allowed) |
| `key` | TUI binding; collisions stop TUI startup |
| `confirm` | Ask first. Noninteractive CLI needs `--yes`; MCP needs `confirm: true`. Refusal runs nothing. |
| `detach` | Hand off to a background runner and return at once. Otherwise the TUI suspends like `$EDITOR`. |

An action's stdin is an unlogged invocation context: the event fields with a
fresh `id`, its `name`, an empty `type`, `origin: action`, the item, the
resource when available, and cause and depth. `BL_EVENT_ORIGIN=action` is set
so nested `bl` writes keep their origin. Every run, even with an unchanged
item, appends one `action.finished` event, then triggers matching hooks. Runs
time out after 30 seconds and are refused beyond causal depth 3.

## Hooks

| Field | Meaning |
| --- | --- |
| `name` | Identifier, matched by `hook:NAME` |
| `on` | Event type |
| `if` | Query over the item, plus `changed:`, `hook:`, `action:`, `outcome:` |
| `phase` | `after` (default) or `before` |
| `cwd` | Working directory (placeholders allowed) |
| `run` | Argv |
| `timeout` | Duration |

| Phase | Behavior |
| --- | --- |
| `before` | Only for `item.created`, `item.updated`, `item.archived`. Runs in order with the proposed event on stdin. The first nonzero exit or timeout rejects the write (exit 5) with the hook's first stderr line. |
| `after` | Handed to a detached `bl` runner by the process that appended the event. Up to four run concurrently per event. Each appends a `hook.finished` event with `outcome` (`ok`, `failed`, `timeout`, `refused`), exit code, duration, result, and `message`. Nothing retries. |

Chain dependent steps on `hook.finished` with `if: "hook:NAME outcome:ok"`. No
hook runs for an event deeper than 3; nested `bl` calls inherit cause and depth.
A hook ends when its process exits; `bl` stops reading output a backgrounded
child holds open after one second, and the child keeps running.

`bl doctor` checks trusted hooks and actions statically: `run[0]` on `PATH`,
known placeholders, timeouts, queries, and `on` event types. Untrusted ones are
reported as inert and not inspected. Hooks have no dry run; use the doctor
check and `bl action --dry-run`. See [cli.md](cli.md#bl-doctor---online).

`cwd` defaults to where the originating command ran, through the whole chain;
for sync-originated chains, the control root.

A `hook.finished` or `action.finished` event that isn't `ok` carries `message`
when there's something to say: why the run was refused or couldn't start, or
for a nonzero exit or timeout the last lines of stderr. It's at most 1 KiB with
terminal controls stripped. It's display text for debugging, not a contract for
parsing; chain on `outcome:` instead. Stdout is never included.

## Results

A hook or action may write one JSON object (≤ 8 KiB) to the file in
`BL_RESULT`. If that file is empty, a single JSON object (≤ 8 KiB) on stdout is
the result, so tools like `wt --format=json` chain directly. Other stdout isn't
a result. A malformed or oversized result file fails the hook. The result
becomes `result` in the finished event, and its top-level strings are available
as `{result.KEY}` to hooks triggered by that event.

## Placeholders and environment

| Placeholder | Value |
| --- | --- |
| `{id}` | Item ID |
| `{ref}` | `source:ID` |
| `{source}` | Source ID |
| `{url}` | Provider URL |
| `{root}` | Control root |
| `{branch}` | `bl-12-debounce-detail-rendering`: lowercase ID and ASCII title slug, ≤ 48 characters |
| `{resource}` | Claim resource key (needs coordination) |
| `{result.KEY}` | Top-level string from the triggering event's result |

Placeholders expand inside argv elements and `cwd`. A missing value, control
characters, a leading `-`, or more than 1 KiB refuses the run. Shell
metacharacters pass through literally.

The process gets only `PATH`, `HOME`, `XDG_CONFIG_HOME`, `XDG_STATE_HOME`,
`XDG_CACHE_HOME`, and `BL_ITEM_ID`, `BL_ITEM_REF`, `BL_SOURCE`, `BL_PROJECT_ID`,
`BL_ROOT`, `BL_BRANCH`, `BL_RESOURCE` (when available), `BL_EVENT_ID`,
`BL_HOOK_DEPTH`, and `BL_RESULT`. No credentials, session, or other variables.
The triggering event is JSON on stdin.

## Agent sessions

`bl resume ID [SESSION]` and the TUI `!` menu reopen an agent session recorded
on the item's events, with the agent's command from user config, in the
session's recorded checkout. Repository config can't set these commands.

| Field | Meaning |
| --- | --- |
| `resume` | Argv that continues the session |
| `fork` | Argv that starts a new session from its transcript; a closed item uses it when set |
| `detach` | Run in the background like a detached action, with the 30-second timeout |

They take the placeholders above plus `{agentSession}`, `{session}`, `{agent}`,
`{cwd}`, and `{host}`, with the same checks. An attached command gets the
terminal, `TERM`, `COLORTERM`, and `LANG`, and no timeout. Built-ins:

| Agent | `resume` | `fork` |
| --- | --- | --- |
| `claude` | `claude --resume {agentSession}` | `claude --resume {agentSession} --fork-session` |
| `codex` | `codex resume {agentSession}` | `codex fork {agentSession}` |
| `pi` | `pi --session {agentSession}` | `pi --fork {agentSession}` |

A session isn't offered when it ran on another host, its checkout is gone, it
still holds its claim or a contact channel reports its agent running (the
reason lists the channels), or its agent has no command; the TUI shows why. Every
run ends in `action.finished` named `resume` or `fork` with the session.

To open the agent in a new Herdr pane beside the focused one (the `bl` pane
when started from the TUI):

```yaml
agents:
  claude:
    detach: true
    resume: [sh, -c, 'p=$(herdr pane split --direction right --cwd "$1" | jq -r .result.pane.pane_id) && herdr pane run "$p" claude --resume "$2"', sh, "{cwd}", "{agentSession}"]
    fork: [sh, -c, 'p=$(herdr pane split --direction right --cwd "$1" | jq -r .result.pane.pane_id) && herdr pane run "$p" claude --resume "$2" --fork-session', sh, "{cwd}", "{agentSession}"]
```

The values reach `sh` as arguments, never as script text. The minimal
environment drops `HERDR_PANE_ID`, so `herdr pane split` targets the focused
pane.

## Agent loops

```sh
export BL_SESSION="$CLAUDE_CODE_SESSION_ID"
bl next --claim --start --json
bl show 12 --json
bl heartbeat --json
bl done 12 --json
```

- Use one stable session across all checkouts, preferably the harness's own
  session or run ID unchanged, with no prefix or suffix, so claim history leads
  back to its transcript. `bl` calls Worklease from the
  control root, so manage claims through `bl`, not by calling Worklease from a
  linked worktree.
- Heartbeat at least every half TTL.
- A failed status write keeps the claim; a failed release leaves the status
  written. An occupied session reports `claim-held`, not contention.
- After an uncertain acquire, recover with the same session; never pick
  another item or session.
- Use the `resource` from `bl show --json` (`{resource}`, `BL_RESOURCE`). Never
  use Worklease's host-local `backlog-md` key for a native item.

See [CLI coordination](cli.md#coordination) for the full rules.

### Supervised workers

A worker started by `worklease run` inherits a session and run ID. `bl` verifies
the live run and that it holds this exact item's resource, then never acquires,
renews, or releases that claim. A bare run ID, or a session named `run-…`, isn't
proof.

A long-lived MCP server takes `session` and optional `run` on **every**
claim-affecting call. Its environment and earlier calls never count. Missing or
mismatched context refuses the call.

### Recovery

Offline inspection shows retained state, not live ownership. Only verified
absence allows a fresh acquire. A pending acquire resumes from Worklease's
retained request; `bl` keeps no journal or credentials. Corrupt, stale, foreign,
changed, or redacted state must be investigated in the same session, never
replaced with a new one. See [provider evidence](cli.md#provider-evidence-and-recovery).
