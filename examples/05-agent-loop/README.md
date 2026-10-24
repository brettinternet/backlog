# Agent loop

Several agents share one queue. `coordination: {worklease: {}}` makes moving
into an active status a claim, so no two sessions take the same item. Needs
[Worklease](https://github.com/brettinternet/worklease) 1.10.1 or later.

Two agents ask for work at the same time and get different items:

```sh
bl next --claim --start --session agent-a
bl next --claim --start --session agent-b
```

```text
BL-2  in-progress  high  Add sign-in page
BL-4  in-progress  low  Write onboarding docs
```

```text
$ bl claim BL-2 --session agent-c
bl: resource is already claimed (holder brett)
```

One agent's loop, with the same session throughout:

```sh
bl heartbeat --session agent-a
bl note BL-2 'Form and validation done' --session agent-a
bl ac BL-2 check 1 2 --session agent-a
bl done BL-2 --session agent-a
bl next --claim --start --session agent-a
```

```text
renewed until 2026-10-23 13:39:06
BL-2  in-progress  high  Add sign-in page
BL-2  in-progress  high  Add sign-in page
BL-2  done  high  Add sign-in page
BL-3  in-progress  medium  Add password reset
```

`done` writes, then releases the claim. Heartbeat before half the claim TTL.
Run `bl instructions workflow` for the full rules an agent should follow.
Agents on several hosts share claims through a
[remote authority](../../docs/cli.md#remote-authority); `bl` commands don't
change.

## Handing off from the TUI

After `bl trust`, two actions start work on a ready item:

| Action | Runs |
| --- | --- |
| `worktree` | `git worktree add -b bl-3-add-password-reset .worktrees/bl-3-add-password-reset` |
| `agent` (`ctrl+a`) | `claude -p 'Implement BL-3. …'` under `worklease run`, which holds and renews the claim |

A supervised agent must not claim, heartbeat, or release itself; the run owns
the claim. See [agent handoff](../../docs/automation.md).
