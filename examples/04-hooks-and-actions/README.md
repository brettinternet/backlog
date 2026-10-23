# Hooks and actions

Actions run on request. Hooks run on [events](../../docs/events.md). Both run
argv directly, never through a shell, and do nothing until you trust them.

| Name | Kind | Does |
| --- | --- | --- |
| `branch` | action, `ctrl+b` in the TUI | `git switch --create bl-4-write-onboarding-docs` |
| `history` | action | `git log` for the item's file |
| `require-criteria` | `before` hook | Refuses `done` while criteria are unchecked ([script](hooks/require-criteria.py)) |
| `done-log` | `after` hook | Appends the ID to `done.log` |

Review, then trust:

```sh
bl trust --show
bl trust
```

A `before` hook that exits nonzero rejects the write:

```text
$ bl done BL-2
bl: 2 unchecked criteria, first: Valid credentials open the dashboard
```

Check the criteria and it goes through. `done-log` runs in the background:

```sh
bl ac BL-2 check 1 2
bl done BL-2
bl action branch BL-4 --yes
```

```text
$ cat done.log
BL-2
$ bl events
… item.updated tasks:BL-2
… item.readiness tasks:BL-2
… hook.finished tasks:BL-2
… action.finished tasks:BL-4
```

Hooks get the event as JSON on stdin and `{id}`, `{branch}`, `{root}`, and other
[placeholders](../../docs/automation.md#placeholders-and-environment). Editing
any hook or action requires `bl trust` again.
