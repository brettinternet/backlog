# User guide

## Install

Binaries are published for macOS and Linux on arm64 and amd64. The installer
needs curl, tar, and `sha256sum` or `shasum`:

```text
curl -fsSL https://raw.githubusercontent.com/brettinternet/backlog/main/install.sh -o /tmp/bl-install.sh
less /tmp/bl-install.sh
sh /tmp/bl-install.sh
```

It verifies the archive checksum, then installs to `~/.local/bin`; add that to
`PATH`.

| Variable | Effect |
| --- | --- |
| `BL_VERSION=v1.2.3` | Install a tag instead of the latest release |
| `BL_INSTALL_DIR=DIR` | Install somewhere else |
| `BL_RELEASE_BASE_URL=URL` | Download from a mirror with GitHub's release layout. Checksums catch corruption, not a compromised mirror. |

To build from source, see [Development](development.md#setup).

## Capture work

The examples in this guide form one session in a fresh Git repository:

```sh
bl init
bl new 'Ship the first feature !high #release' --ac 'Feature works'
bl new 'Review the feature <BL-1' --status draft
bl ls
bl show BL-1 --json
bl next --explain --json
bl set BL-2 status=todo
```

`bl init` never overwrites existing config. Quick-add tokens set fields:

| Token | Field |
| --- | --- |
| `!high` | Priority |
| `@name` | Assignee |
| `#label` | Label |
| `^BL-1` | Parent |
| `<BL-1` | Blocked by |
| `~m-1` | Milestone |
| `:feature` | Type |

Quote the title so the shell leaves `!`, `<`, and `#` alone. A draft is never
ready and keeps its ID when promoted. BL-2 stays out of `next` until BL-1 is
closed. [Readiness](readiness.md) has the full rules.

## Import Backlog.md

Import reads the files directly. To switch a repository over, commit the
Backlog.md files, then replace them with the native project in place:

```text
bl import backlog-md --in-place --dry-run
bl import backlog-md --in-place
bl check
```

Review the result with `git status` and commit it. To import into a separate
directory and leave the source unchanged, use `--to` with an empty or absent
directory outside the current checkout, or project discovery finds the old
backlog first:

```sh
bl import backlog-md old-backlog --to ../imported --dry-run --json
bl import backlog-md old-backlog --to ../imported --json
git -C ../imported init --quiet
bl -C ../imported ls
```

Read the mapping report before switching. Stop every writer of the old backlog
first; never run two writers during a migration. See [Import](import.md) for the
field mapping and a full cutover rehearsal. To keep Backlog.md files in place
instead, configure a [Backlog.md source](backlog-md.md).

## TUI

Run `bl` (or `bl tui`) in a terminal.

| Goal | Keys |
| --- | --- |
| Move; first/last | `j`/`k`, `gg`/`G` |
| Open detail; next tab; close | `enter` or `l`, `tab`, `esc` |
| Next or previous item | `J`/`K` |
| Next view; board | `gt`, `5` |
| Board column; move card | `h`/`l`, `<`/`>` |
| Filter; clear; save view | `/`, `F`, `ctrl-s` |
| Go to item; palette; help | `ctrl-p`, `:`, `?` |
| Create; edit title; description | `n`, `t`, `e` |
| Status; priority; assignees | `s`, `p`, `a` |
| Mark; repeat; undo; redo | `space`, `.`, `u`, `ctrl-r` |
| Quit | `q` or `ctrl-c` |

Emacs keys work too: `ctrl-n`, `ctrl-v`/`alt-v`, `alt-<`/`alt->`, `ctrl-g`,
and `alt-x`. Text fields take shell-prompt editing keys, and menus also move
with `ctrl-j`/`ctrl-k`.

Built-in views are Next, Active, Mine, Open, and Board; Board hides drafts until
`fb`. `T` and `B` switch the current view to a tree or board. The filter bar
uses the same [query language](query.md) as the CLI. Edits apply at once and
show `●` until the source confirms; failures and conflicts stay visible. See
[TUI](tui.md) for the full keymap.

Off a terminal, bare `bl` prints help and `bl tui` fails.

## Configuration

| Layer (lowest first) | Location |
| --- | --- |
| Built-in defaults | |
| User config | `~/.config/backlog/config.yaml` (`$XDG_CONFIG_HOME`, also on macOS) |
| Project config | `backlog.yaml` at the control root |
| Private project config | `backlog.local.yaml` at the control root |
| User per-project overrides | `projects.<project-id>` in user config |
| Environment | `BL_HIGH_CONTRAST=true` |
| Flags | `--high-contrast` |

Mappings merge; lists replace. Linked worktrees use the primary checkout's
backlog and both config files. Routing reads primary shared, primary local,
invoking shared, then invoking local; `root: checkout` loads that checkout's
files. Either file marks a project. Legacy `.config/backlog.yaml` is rejected;
run `git mv .config/backlog.yaml backlog.yaml`.

```sh
bl config show --json
bl trust --show --json
bl doctor --json
```

`config show` prints each effective value and where it came from. `doctor`
checks local health; add `--online` to allow network probes.

Hooks, actions, adapters, network sources, and `git.autoCommit` from either
project file do nothing until you run
`bl trust`. Inspect `bl trust --show` first. Changing them requires trusting
again. Keep credentials in user config or the environment, never in project
config. `backlog.local.yaml` uses the project schema, not user accounts.
Ignore `/backlog.local.yaml`; `doctor` warns if it is tracked or not ignored.
`bl` never writes this file or edits `.gitignore`. Repo files settle `project.id`
before looking up user `projects.<id>` overrides.

Other settings:

- `NO_COLOR=1` disables styling.
- `git: {autoCommit: true}` commits only the files `bl` writes ([details](cli.md#git-auto-commit)).
- User-config `workspaces` browse several projects together: `W` toggles in the
  TUI, and `--workspace NAME` works with `ls` and `next`.

Schemas: [project](schemas/project-config.json), [user](schemas/user-config.json).
Design: [configuration and trust](design.md#5-projects-configuration-and-trust).

## Agent loop

Without coordination, a status is not a claim. For shared ownership, install
Worklease 1.10.1 or later, point every worker at one authority, and add to
project config:

```yaml
coordination: {worklease: {}}
```

The guide's doc test appends this to the fresh project and uses a fake
authority. Don't append a duplicate key to a configured project.

```sh
printf '\ncoordination: {worklease: {}}\n' >> backlog.yaml
export BL_SESSION=guide-loop
bl next --claim --start --session "$BL_SESSION" --json
bl show BL-1 --json
bl heartbeat --session "$BL_SESSION" --json
bl note BL-1 'Implemented and verified the feature' --session "$BL_SESSION" --json
version=$(bl show BL-1 --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["item"]["version"])')
bl ac BL-1 check 1 --if-version "$version" --evidence 'Feature verification passed' --session "$BL_SESSION" --json
bl done BL-1 --session "$BL_SESSION" --json
```

Rules for a real loop:

- Use one unique session for the whole loop.
- Read the whole item, do the work, verify it, and record real evidence before
  checking criteria or finishing.
- Pass `item.version` as `--if-version` when a write depends on a read,
  especially criterion numbers. Reread after each write.
- Heartbeat before half the claim TTL.
- Check both the status and claim outcomes. A failed status write keeps the
  claim; a failed release leaves the status written.
- `done` writes, then releases. To stop early, save progress and run
  `bl release ID --session SESSION`.
- After an uncertain acquire, retry with the same session. Never switch sessions.
- Under a verified `worklease run`, the supervisor owns the claim. Don't claim,
  renew, or release.
- MCP callers pass `session` (and `run`) on every claim-affecting call.

Claims coordinate cooperating clients; they don't lock files. The installed
version's guidance:

```sh
bl instructions workflow
bl instructions json
```

See [CLI coordination](cli.md#coordination), [agent handoff](automation.md), and
[MCP](mcp.md).

## Hooks and actions

Hooks run when events happen. Actions run on request from the CLI, MCP, or the
TUI `!` menu. Both run argv directly, never a shell:

```yaml
actions:
  - name: inspect
    run: [git, status, --short]
hooks:
  - name: notify-ready
    on: item.readiness
    if: 'is:ready'
    run: [tell, notify, 'Ready: {id}']
```

Merge these into project config, check `bl trust --show`, then run `bl trust`.
Hooks get the event as JSON on stdin and a minimal environment without
credentials. Only `before` hooks can veto a write. See [Automation](automation.md)
and [Events](events.md).

## Demo

![Fixture TUI tour](demo.gif)

[`demo.tape`](demo.tape) records a 48-item fixture (seed 1, fixed timestamps,
isolated HOME and XDG) touring Open, detail, Board, and help. To re-record:

```text
task cli:build
task docs:demo
```

Recording needs VHS 0.11.0, FFmpeg, ttyd, Go, and VHS's default monospace font.
Content and keys are deterministic, but GIF bytes vary with fonts, FFmpeg, and
frame timing, so don't use it for pixel or timing comparisons.

`task docs:test` runs every `sh` block in README and this guide in temporary
repositories and checks README's doc index.
