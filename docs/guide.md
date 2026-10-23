# User guide

## Install

Published binaries support macOS and Linux on arm64 and amd64. The installer
requires curl, tar, and sha256sum or shasum. Download `install.sh` from this
repository, inspect it, then run `sh install.sh`. It verifies the release archive
checksum before replacement. The default destination is `~/.local/bin`; add it
to PATH. `BL_VERSION` pins a release tag and `BL_INSTALL_DIR` changes the destination.
Installation needs a published release and is an owner operation, not a doc test.
See [Development](development.md#releases) for release details and source setup.

## Initialize and capture

Run inside a Git repository. Initialization refuses to overwrite existing config.
The following examples form one session in a fresh repository:

```sh
bl init
bl new 'Ship the first feature !high #release' --ac 'Feature works'
bl new 'Review the feature <BL-1' --status draft
bl ls
bl show BL-1 --json
bl next --explain --json
bl set BL-2 status=todo
```

Quick-add tokens set priority (`!high`), assignee (`@name`), label (`#label`),
parent (`^BL-1`), blocker (`<BL-1`), milestone (`~m-1`), and type (`:feature`).
Quote tokens to keep shell metacharacters literal. Drafts keep their IDs when
promoted and are never ready. A blocked task stays out of Next until its blockers
are closed. [Readiness](readiness.md) defines the ranking and exclusions.

## Import Backlog.md

Import reads files directly, never runs Backlog.md, and never changes the source.
Here `old-backlog` is a legacy Backlog.md project. Use your actual path. The
native destination must be absent or empty, even during dry-run. Keep it outside
the current Git checkout so control-root discovery does not select the old project:

```sh
bl import backlog-md old-backlog --to ../imported --dry-run --json
bl import backlog-md old-backlog --to ../imported --json
git -C ../imported init --quiet
bl -C ../imported ls
```

Inspect the mapping report before switching tools. Drafts, archives, milestones,
unknown frontmatter, and references are preserved where supported; skipped
material is reported. See [Import](import.md). Do not keep two writers active
during a migration. Local [Beads](beads.md) is also supported; [remote adapters](remote.md)
have their own availability and authentication constraints.

## TUI tour

Run `bl` (or `bl tui`) with terminal stdin and stdout. Bare `bl` prints help off
terminal; `bl tui` refuses. The [demo](#demo) records a disposable fixture project.

| Goal | Keys |
| --- | --- |
| Move; jump first/last | `j`/`k`, `gg`/`G` |
| Open detail; switch detail tab; close | `enter`, `tab`, `esc` |
| Next view; board | `gt`, `5` |
| Board column; move card status | `h`/`l`, `<`/`>` |
| Filter; clear; save view | `/`, `F`, `ctrl-s` |
| Find any item; palette; help | `ctrl-p`, `:`, `?` |
| Create; edit title; description | `n`, `t`, `e` |
| Status; priority; assignees | `s`, `p`, `a` |
| Mark; repeat; undo; redo | `space`, `.`, `u`, `ctrl-r` |
| Quit | `q` or `ctrl-c` |

Next, Active, Mine, All, and Board are built in. Save a view with `layout: tree`
for parent/child navigation. CLI queries and the filter bar share the
[query language](query.md). Detail has Overview, Description, Criteria, Plan/Notes,
Comments, and Relations; unsupported edits are disabled. Writes are optimistic;
errors and conflicts remain visible. Long text opens `$EDITOR`. See
[TUI](tui.md) for the full keymap, conflict handling, and persisted state.

## Configuration

Config layers, lowest to highest: defaults, user config, project config,
user per-project overrides, environment, flags. Mappings merge; lists replace.
User config is `$XDG_CONFIG_HOME/backlog/config.yaml` (default
`~/.config/backlog/config.yaml`, also on macOS). Project config is
`.config/backlog.yaml` at the control root. Linked worktrees use the primary
checkout's backlog; `root: checkout` opts out.

```sh
bl config show --json
bl trust --show --json
bl doctor --json
```

These commands inspect effective values/provenance, privileged declarations,
and local health. `doctor --online` is an explicit opt-in to network probes.
Project programs and network sources are inert until `bl trust`; inspect their
argv and digest before granting consent. Semantic changes require re-trust.
Credentials belong in environment variables or user credential helpers, never
project config. `NO_COLOR=1` disables styling; `BL_HIGH_CONTRAST=true` or
`--high-contrast` selects high contrast. `git: {autoCommit: true}` commits only
the files `bl` wrote ([Git auto-commit](cli.md#git-auto-commit)). User-config
`workspaces` browse several projects together; `W` toggles scope in the TUI and
`--workspace NAME` selects one for `ls` and `next`. See the [project schema](schemas/project-config.json),
[user schema](schemas/user-config.json), and [design configuration](design.md#5-projects-configuration-and-trust).

## Agent loop and Worklease

Without coordination, status is not a claim. To enable shared ownership, install
Worklease >= 1.10.1 with the guarded release API, configure one shared authority
for every worker, and add this to project config:

```yaml
coordination: {worklease: {}}
```

For this fresh example project, the following appends that declaration. Do not
append a duplicate key to an already configured project. The doc test uses an
isolated fake authority, never your Worklease state.

```sh
printf '\ncoordination: {worklease: {}}\n' >> .config/backlog.yaml
export BL_SESSION=guide-loop
bl next --claim --start --session "$BL_SESSION" --json
bl show BL-1 --json
bl heartbeat --session "$BL_SESSION" --json
bl note BL-1 'Implemented and verified the feature' --session "$BL_SESSION" --json
version=$(bl show BL-1 --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["item"]["version"])')
bl ac BL-1 check 1 --if-version "$version" --evidence 'Feature verification passed' --session "$BL_SESSION" --json
bl done BL-1 --session "$BL_SESSION" --json
```

The version-extraction example requires Python 3. Use a unique, stable session
for your entire real loop, not `guide-loop`. Read the
selected item's complete intent before editing. Implement every requirement,
verify it, and record real evidence before checking criteria or finishing.
When a write depends on a read (especially criterion indices), pass its
`item.version` as `--if-version`; reread after each write. The example has one
writer; concurrent agents must guard those writes.

Renew before half the claim TTL. Inspect both status and claim outcomes: failed
status writes keep the claim; failed releases can leave the item closed but held.
Successful `done` writes then releases before returning. If stopping unfinished,
persist progress and use `bl release ID --session SESSION`. Uncertain acquisition
must replay with the same session, never a replacement session or invented key.
`bl show ID --json` reports the canonical resource; do not use Worklease's
host-local Backlog.md resource for a native task.

Under a verified `worklease run`, the supervisor owns selection, renewal, and
release: do not claim another item or manage its claim yourself. MCP callers
supply session and optional run on every claim-affecting call, regardless of the
server environment. Read the installed guidance:

```sh
bl instructions workflow
bl instructions json
```

See [CLI coordination](cli.md#coordination), [handoff and recovery](automation.md),
and [MCP](mcp.md). Claims coordinate cooperating clients, not arbitrary file writes.

## Hooks and actions

Hooks react to events; actions are explicit invocations from CLI, MCP, or the
TUI `!` menu. Programs receive argv directly, not shell strings. For example,
merge these declarations into project config, inspect them, then grant trust:

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

These optional examples require Git and Tell respectively. Hooks get JSON stdin
and a minimal environment, not credentials or caller session authority. Only
before hooks can veto writes; after hooks are asynchronous. Placeholders are
validated, results are bounded, and chains stop beyond depth 3. See
[Automation](automation.md) for action confirmation, worktree/agent handoffs,
and [Events](events.md) for the resumable stream.

## Demo

![Fixture TUI tour](demo.gif)

`demo.tape` uses the BL-12 generator: 48 items, seed 1, fixed fixture timestamps,
isolated HOME/XDG, no provider data, fixed viewport, theme and key sequence.
It shows All, detail, Board, and searchable help without modifying real tasks.
Build `bin/bl` with `task cli:build`, then run `task docs:demo` from the repository
root. Recording requires VHS 0.11.0, FFmpeg, ttyd, Go, and the VHS default monospace
font; install these separately. `task docs:test` runs every `sh` block in README
and this guide in temporary repositories and checks README's doc index.
The recorder removes only its own temporary fixture tree on normal exit.

The fixture content and interaction sequence are deterministic; GIF bytes can
vary across font/FFmpeg versions or asynchronous frame timing. Pin the recording
environment for pixel comparisons; do not use the animation as a timing test.
