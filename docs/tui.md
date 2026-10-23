# TUI

**Status:** Draft UX spec. BL-16 through BL-25 implement and refine it. Every key is remappable through user config `keys:`, and this file is the source for the default keymap and help text.

## Git commits

With trusted `git.autoCommit: true`, consecutive native item edits share one
commit after two seconds without another successful edit. Quit flushes the
remaining batch after the source workers stop. Timers, Git, and file checks run
outside the Bubble Tea update loop. Skips and Git hook failures are warnings,
shown in the status line while the TUI runs and on stderr after quit;
successful item writes remain. See [CLI Git behavior](cli.md#git-auto-commit)
for path preservation, unsafe states, and the message format.

## Implemented list and detail shell

Bare `bl` opens the list when stdin and stdout are terminals; `bl tui`
explicitly requires both. The frame paints a loading state before native
sources load, then consumes immutable engine projections. Native startup first
publishes up to 256 complete items in filename order, using stat-validated cache
batches when available and parsing changed or uncached files. This partial list
is navigable and editable while full validation, cache population and search
indexing continue on the source worker. Missing dependency targets remain unknown;
readiness, ordering and counts can change when the remaining items arrive. Header
badges show each source as synced `✓`, syncing `↻`, partial `~`, or error `!`.
A partial list is not a complete
backlog, and a loading shell is not counted as first usable content. Untrusted project hooks, actions, or adapters
replace the badges with a persistent `⚠ Untrusted project config · bl trust --show`
warning; untrusted adapters are not opened. Native sources remain browsable, and
trust-state reads happen during configuration loading, not on the UI loop.
Rows show priority, ID, status, title with its labels, open blockers, child
progress, assignee, and age since the last update. Column widths come from the
prepared rows, never from the update loop. As a pane narrows, secondary columns
drop so the title keeps at least two-fifths of the width: age first, then
assignee, child progress, blockers, claim, and finally the status name, leaving
its icon. Grouping by status shows only the icon. The list shows loading, not
"No matching items.", while a source syncs or a newer result is pending.

The status bar shows the active query, or the input, prompt or error that
replaces it. A notice replaces the query until the next key. On the right are a
pending prefix or count, marks, pending `●` and failed `!` edit counts in that
projection (including folded rows), `↻` while results are in flight, and the
cursor position.

Next, Active, Mine, Open and Board are built-in tabs. Next ranks ready items
without groups; Active, Mine and Open group by status. `j`/`k`
and arrows move by display row (including group headers); counts, `gg`, `G`,
`NG`, half/full pages, `[`/`]` group jumps and `za` folding work. `gt`/`gT`
cycle tabs. Live refreshes keep folds and keep the cursor on the same item or
group header. Each tab retains its query, folds and stable-item selection;
reordered rows keep the same item, and a missing item falls back to the first row.
A digit waits 350 ms to distinguish `1`–`9` view shortcuts from count-prefixed
motions; unavailable numbers do nothing. `gt`/`gT` reach every saved tab. `R`
requests a background refresh. `?` toggles help. The palette follows the
terminal's light or dark background, and neutral shades are mixed from its own
foreground and background colors. `NO_COLOR` disables styling;
`--high-contrast` uses bold and underline, never faint text. All external text
is sanitized and widths use terminal cells.

`enter`, `l` or `right` opens detail from a list (`enter` on a group header
folds it). At 120 columns or more it opens beside the list; below 120 columns
it replaces the list. `tab` focuses the detail pane or advances its tabs, and
`shift-tab` moves to the previous tab. In detail focus, `j`/`k` and page keys
scroll, `m` expands a capped body, and `enter`, `esc` or `q` close the pane.
`J`/`K` select the next or previous item from the list or the detail pane.
At split widths, `h`/`left` returns focus to the list without closing detail,
so held navigation keeps the header current while postponing body work; a
second `h` closes it.
The detail header follows selection immediately. Full item content, including
comments, is read only after selection settles for 50 ms; a single bounded
worker renders Markdown off the update loop and keeps a bounded version/width/
theme/tab/expansion cache. Bodies initially render at most 32 KiB; `m` shows
up to 128 KiB, with a 128 KiB output cap even after expansion. The Overview
shows declared milestone names beside their keys and preserves undeclared values.
Relations and readiness summaries come from the live
projection, so linked-item status changes appear without rereading the detail.

`/` edits the current query live. Left/right, home/end, backspace/delete and
ctrl-u edit the line; tab inserts completion, up/down choose a candidate, enter
applies and esc restores the query from before editing. Syntax errors include
zero-based byte positions and retain the last valid rows; unknown snapshot
values show a warning. Completion uses the loaded snapshot and configured values
on the bounded projection worker. Free-text ID/title matches are bold/underlined
(unless NO_COLOR is set).

`f`, `o`, and `z` open which-key menus; esc cancels. Field filters open a value
picker with completion; enter chooses and applies. `fr` shows ready items and
`fd` includes both open and closed items (not archives). `F`/`fc` clear predicates
while retaining sort and group. Sort reversal toggles every explicit sort key.
`zA` groups assignees, leaving `za` for folds; `zM`/`zR` fold/unfold all.

Item-field editing is implemented from the list, with version-checked engine
mutations, fuzzy pickers, marks, repeat, undo/redo, criterion toggles, archive
confirmation, and failure detail. `n`/`N` quick-add, full create/edit forms,
section/full-file editor sessions, comments, and notes are available. The Board
tab and saved board layouts support column navigation, horizontal scrolling,
and column-status quick-add defaults, including empty columns. Picker catalogs,
board preparation, and fuzzy filtering run in Bubble Tea commands; source I/O
and backlog-sized work stay off `Update` (D14).
Tree layouts support parent navigation, folds, ancestor context and descendant
progress. Go-to, the command palette, searchable help, key remapping and mouse
are implemented.
With coordination enabled, the wide list adds Claim (`mine`, holder agent/host,
or `expired`), also available as a saved-view `claim` column. `C` claims or releases
the selected item; `S` uses the same active-transition gate as the CLI. Claim queries
use one bulk overlay, polled every five seconds and after operations; expiry is
scheduled locally and reprojects without polling. No Worklease calls or backlog-sized
claim work run in Update/View or per row. An unavailable/redacted overlay is unknown,
not known-free; see docs/cli.md for provider-evidence and recovery requirements.

Capture a real render without opening a user's pane:

```sh
task cli:build
scripts/tui-snapshot --width 80 --height 24 -- bin/bl -C /path/to/native-project tui
scripts/tui-snapshot --width 160 --height 48 --keys g t g t g t -- bin/bl -C /path/to/native-project tui
```

The helper owns a private tmux server, waits for a settled render, writes pane
text to stdout, and shuts down only its own server, including on failure.

## Workspaces

User `workspaces` lists project paths; `defaultWorkspace` selects the workspace outside a repository. Inside a project the TUI loads the first workspace containing it and starts in project scope. `bl --workspace NAME` or `bl tui --workspace NAME` starts in workspace scope. Each project keeps its own sources, statuses, trust and write/claim context; an untrusted source stays inert without hiding trusted native sources.

`W` toggles the invoking project and workspace scopes, keeping the selected item when it is visible in both. The header names the scope. Wide workspace lists add Project and Source columns. Project names use `project.name`, falling back to the final component of the project identity. Internal references use the stable project-qualified source keys in [format.md](format.md); duplicate local source/ID pairs never overwrite one another. Relation resolution and readiness use only the loaded scope. Scope filtering, normalization and projection run on the worker, never on the update loop.

Five-project/50,000-item real-PTY startup on Apple M5 Max / Go 1.27.3:

```sh
BL_TUI_WORKSPACE_MEASURE=1 task cli:test PKG=./internal/tui -- -run '^TestWorkspaceMeasurements$' -count=1 -v -timeout=6m
```

| Cache | Shell paint | First usable content | Complete workspace |
| --- | --- | --- | --- |
| Cold, one run | 26.50 ms | 91.57 ms | 4.234 s |
| Warm, five runs, p95 | 27.70 ms | 91.43 ms | 1.034 s |

The deterministic fixture is five independent 10k projects, including colliding source and item IDs. A loading shell does not count as usable content. The harness drives the production store, projector and model in a 120×40 PTY, verifies the source column, and waits for all five sources to report complete coverage. Measurements report latency, not an absolute CI assertion.

## Engine foundation

The shell uses `OpenProjectStore` to reuse configured native sources.
`internal/engine.OpenStore`
provides the background engine: `Current` reads an immutable snapshot in O(1),
`Subscribe` delivers only the latest snapshot, and `Mutate`, `Undo`, `Redo`, and
`Refresh` enqueue bounded requests without source I/O on the caller. Full
request queues return `busy`; overlapping edits to one item are rejected.
An edit that expects a version this session's own edit replaced (the display
has not caught up yet) also returns `busy`, never a recorded conflict; only a
foreign change produces a conflict with both versions.
Unsubscribe when a view closes. Close the store outside the UI update loop.

`NewProjector` owns a single latest-wins worker. Pass a snapshot and query to
`Request`; consume prepared row groups from `Results`. Superseded requests are
canceled and cannot publish stale results. Readiness, filtering, sorting and
grouping all run in the background, never in Bubble Tea callbacks.

Edits publish a pending snapshot before source I/O, including while another
item's write is queued. Confirmation installs the source version; a failure
rolls back and retains its error. Conflicts retain local and current source
versions and never retry automatically. Session undo/redo use version-checked
inverse mutations; native inverses preserve original bytes except `updated`.
An external edit breaks the undo chain rather than granting permission to
restore bytes from before that edit. Undo/redo wait for pending writes. Undo
of creation archives the new item; redo unarchives it. Archived session items
remain available for undo but are excluded by normal projections.

Native sources watch directory entries only: two descriptors on macOS
(kqueue plus directory), two on Linux (inotify plus a cancellation eventfd),
independent of item count. Watches block without timeouts, so idle costs no
wakeups beyond the sweep.
Atomic edits, creates, deletes and renames signal immediately. A two-second
stat sweep catches in-place edits on macOS, which do not signal the directory;
the 150 ms filesystem-event budget applies to directory-signaled changes.
`EMFILE`/`ENFILE` degrade to 50 ms stat polling, with no per-file watches. All
watchers and workers stop on context cancellation. Source coverage is explicit:
a partial or failed refresh never proves deletion. Remote warm starts publish at
most 256 cached rows before waiting for the network, with unknown coverage.
Cached rows remain visible in Open, but stale items are not ready in Next and a
stale done blocker cannot satisfy a dependency. Fresh rows replace stale rows
even when their provider version is unchanged. Remote source workers poll once
per minute; `R` requests an earlier refresh. Quota errors remain visible while
the account scheduler refuses requests before its retry deadline.

## Principles

1. **Never wait.** Paint from the cache at once, sync behind it, and apply edits optimistically. A spinner marks work in flight; it never blocks input (design.md D14, D15).
2. **Every common action is one or two keys.** Prefix keys open a which-key popup that names the next keys. `:` opens a fuzzy command palette listing every action with its binding.
3. **Vim muscle memory.** Counts, `gg`/`G`, `ctrl-d`/`ctrl-u`, `.` to repeat, `u` to undo, `/` to filter, `z` for folds.
4. **One program, every view.** Views are tabs. Layouts are list, board, and tree. Detail is a pane.
5. **Show only what works.** Actions appear only when the source supports them (D10). Disabled reasons show in the palette.
6. **Color is never the only signal.** Glyphs and text carry meaning under `NO_COLOR` and `--high-contrast`.

## Layout

```text
 backlog   Next   Active   Mine   Open   Board                                  ✓ tasks
       ID          Title                                       ⊘        ▣  Assignee  Updated
▌ ▾ ○ To Do  3
   ▲   BL-30    ○  Run hooks on events with results, chaini…   ⊘ 3                         1h
   ■   BL-24 ●  ○  Add go-to, the command palette  #tui        ⊘ 1           @brett        3h
   ▼   BL-41    ○  Sync Jira Cloud issues  #jira                                          12h
  ▾ ◐ In Progress  1
   ▲   BL-53    ◐  Add opt-in OAuth login for remote sources         ▣ 1/5  brett         1d
 is:open group:status                                                                  1/4
```

The header holds the tabs and source badges, then the column header marks the
sort column with an arrow. The status bar is the last line. At 120 columns or
more, the detail pane opens beside the list; below that, it replaces the list.
Pickers, quick-add and which-key menus open as sheets above the status bar;
the palette, go-to and the full form open as centered panels; help fills the
body.

| Glyph | Meaning |
| --- | --- |
| `▲▲` `▲` `■` `▼` | Priority: urgent, high, medium, low |
| `◌` `○` `◐` `✓` `✕` | Status category: backlog, todo, active, done, canceled |
| `⊘ N` | N open blockers |
| `▣ a/b` | Child progress |
| Claim column | Worklease holder: `mine`, `agent@host`, or `expired` |
| `●` after the ID | Pending local edit |
| `!` after the ID | Failed or conflicted edit; open the item for detail |
| `•` in the gutter | Marked item |

## Views and layouts

The built-in tabs are Next (`is:ready sort:next`), Active (`category:active`),
Mine (`assignee:me is:open`), and Open (`is:open`); all but Next group by
status. They precede project `views`, user `views`,
and user `projects.<project-id>.views`, each in declaration order. Same-named
views in different layers remain separate tabs; names within a layer must be
unique. This additive tab catalog does not change D16's list-replacement rule
for effective configuration. The built-in Board tab follows Open and shows open
items grouped by status, without the closed columns its query cannot fill.

A view has `name`, `query`, optional `sort`, `group`, `layout`, and `columns`.
Explicit sort/group fields override query directives. Columns may be `id`,
`priority`, `status`, `blockers`, `children`, `claim`, `assignee`, `updated`,
and `title`, in display order; without columns the standard list is used.
Narrow panes drop secondary columns in the standard order.
Set `layout: tree` on a saved view to show the hierarchy instead of query groups.

`ctrl-s` opens a name prompt, initially using the current name. `ctrl-u` clears
the name; type a replacement and press enter. The default destination is user
config; tab toggles to project config. Esc cancels. Saving replaces the
same-named entry only in the chosen destination or appends a new entry. Only
the changed entry is rewritten (the views value for a flow-style sequence);
unrelated config stays byte-identical. Writes reread under a file lock and
use temp/fsync/rename. The saved tab appears immediately in catalog order and
survives restart. Config I/O runs in a command, not the update loop.

| Layout | Navigation | Edits |
| --- | --- | --- |
| List | Grouped headers with counts. `za` folds a group, `[` `]` jump between groups. | All item edit keys |
| Board | One column per status (or per group key). `h`/`l` move between columns, `j`/`k` within one. | `<`/`>` move the card to the previous or next status |
| Tree | Parent and child hierarchy with progress. `h` collapses or moves to the parent; `l` expands or moves to the first child. | All item edit keys |

Boards show configured status columns in workflow order, including empty
columns, followed by undeclared statuses. A query with no grouping also uses
status columns. Other group keys use the projection's group order. Each column
shows its count, remembers its cursor, and scrolls independently. At least one
column fits; columns target a minimum width of 28 cells and scroll horizontally
to keep the focused column visible. `h`/`l` and left/right (also `[`/`]`) change
columns; `j`/`k`, counts, page keys, and `gg`/`G` move within the focused column.
An empty column can be focused but has no selected item. In boards, `l` navigates
rather than opening the label picker. `<`/`>` always change workflow status,
even when grouped by priority or another non-status key. Pending moves and
rollbacks follow the selected item's stable reference while it remains in the
query. Quick-add uses the focused status column, even when empty; other group
keys retain the normal create default. Boards ignore list folds. Preparation
runs off-loop; navigation is constant-time and rendering visits visible cards
only, independent of backlog size.

Trees follow parent fields (source-less references use the item's source).
Explicit child relations fill absent parents. Siblings retain query order;
nonmatching ancestors use stable reference order. Filtering retains the ancestor
chain of each match, with a `·` context marker and faint text (underline in
high-contrast mode). Context ancestors remain selectable and editable.
`h`/left collapses an expanded branch, otherwise selects its parent;
`l`/right expands a collapsed branch, otherwise selects its first visible child.
Leaves ignore `l`; roots with no expanded children ignore `h`. In trees `l`
navigates rather than opening the label picker. `za` toggles the selected branch;
`zM`/`zR` collapse/expand all. Folds survive refreshes by stable item reference.
`▣ a/b` counts closed (done or canceled) descendants out of all unique descendants,
including filtered-out children, not just direct children or visible rows.
Missing parents become roots; malformed cycles are cut into a deterministic
forest so each item appears at most once (use `bl check` to diagnose the data).
Indentation is capped by viewport width for very deep trees. Hierarchy building,
ancestor closure, progress and flattening run off-loop; navigation uses prepared
parent indices and rendering visits only the viewport.

The detail pane starts with the item's ID, status, priority and title, then
these tabs, with counts where they apply:

- **Overview:** set fields, readiness and why, and the description
- **Criteria:** acceptance criteria and definition of done, with progress
- **Notes:** plan, notes and summary
- **Comments**
- **Relations:** a dependency flow from blockers through the item to what it
  blocks, then parent, children with progress, related items and duplicates,
  each with live status

Markdown renders asynchronously and is cached. While the selection moves, the header fields update at once and the body updates once the selection settles. Bodies are capped at 32 KiB by default; `m` expands the cap to 128 KiB, and an indicator remains if content is still truncated.

## Default keymap

Bindings below use Bubble Tea names (`down` is ↓, `ctrl+d` is Ctrl-D).
Spaces separate a sequence (`g g`); counts prefix motions (`5j`, `25G`).
The same contextual action runs from a key or the palette. Digit shortcuts wait
350 ms so they can become counts. Keys in an active text editor/picker remain
local to that input; Ctrl-C quits there.

`ctrl-p` loads a cache-backed ID/title catalog, including closed and archived
items, on a single background worker. Typing fuzzy-filters the catalog with
latest-wins results and at most 100 matches; enter opens the selected item's
detail without changing the current view or its query. Esc/q returns to the view.
Each new go-to session refreshes the catalog. No source I/O or catalog scans run
on the update loop.

`:` lists every action, its effective bindings, and any disabled reason. Type to
fuzzy-search, use arrows to select and enter to run. `?` opens the same searchable
catalog as read-only help. Esc closes either. Every prefix, including remapped
prefixes, displays its available continuations. Workspace scope is disabled when no workspace contains the current project. `!` lists configured actions whose `when` query matches the selected
item; the palette and help include the same matching actions and disabled
placeholder/trust reasons. Action discovery and preparation run off the update
loop. `confirm` prompts capture the target item; y/enter runs and n/esc cancels.
Attached actions suspend and restore the terminal; detached actions return at once.
Configured keys share collision validation and use stable IDs `action:NAME`.

User config maps stable action IDs to one key sequence; an empty value unbinds
an action. An override replaces all aliases. Unknown actions, invalid key names,
exact collisions and ambiguous prefix bindings fail TUI startup with both
conflicting action IDs and bindings. Swap bindings by overriding both actions:

```yaml
keys:
  move-down: J
  move-up: K
  next-item: j
  previous-item: k
  first: v v
  workspace: ''
```

<!-- keymap:start -->
| Action ID | Keys | Action |
| --- | --- | --- |
| `move-down` | `j`, `down` | Move down |
| `move-up` | `k`, `up` | Move up |
| `next-item` | `J` | Next item |
| `previous-item` | `K` | Previous item |
| `first` | `g g`, `home` | First row |
| `last` | `G`, `end` | Last row, or row N |
| `half-down` | `ctrl+d`, `pagedown` | Half page down |
| `half-up` | `ctrl+u`, `pageup` | Half page up |
| `page-down` | `ctrl+f` | Page down |
| `page-up` | `ctrl+b` | Page up |
| `previous-group` | `[` | Previous group |
| `next-group` | `]` | Next group |
| `left` | `h`, `left` | Back / parent / left |
| `right` | `l`, `right` | Open / child / right |
| `go-to` | `ctrl+p` | Go to item |
| `detail` | `enter` | Toggle detail |
| `next-detail-tab` | `tab` | Next tab |
| `previous-detail-tab` | `shift+tab` | Previous tab |
| `split-left` | `ctrl+left` | Shrink list |
| `split-right` | `ctrl+right` | Grow list |
| `next-view` | `g t` | Next view |
| `previous-view` | `g T` | Previous view |
| `board` | `B` | Board layout |
| `tree` | `T` | Tree layout |
| `query` | `/` | Edit query |
| `clear-filter` | `F` | Clear filters |
| `save-view` | `ctrl+s` | Save view |
| `workspace` | `W` | Toggle workspace |
| `refresh` | `R` | Resync sources |
| `status` | `s` | Status… |
| `previous-status` | `<` | Previous status |
| `next-status` | `>` | Next status |
| `start` | `S` | Start |
| `done` | `D` | Done |
| `priority` | `p` | Priority… |
| `assignees` | `a` | Assignees… |
| `assign-me` | `A` | Assign me |
| `labels` | `#` | Labels… |
| `milestone` | `m` | Milestone… / more |
| `blocker` | `b` | Blockers… |
| `parent` | `P` | Parent… |
| `title` | `t` | Edit title |
| `description` | `e` | Edit description |
| `edit` | `E` | Edit all fields |
| `comment` | `c` | Comment |
| `note` | `i` | Add note |
| `criterion` | `x` | Toggle criterion |
| `claim` | `C` | Claim / release |
| `archive` | `d d` | Archive |
| `new` | `n` | New item |
| `new-child` | `N` | New child |
| `repeat` | `.` | Repeat edit |
| `undo` | `u` | Undo |
| `redo` | `ctrl+r` | Redo |
| `actions` | `!` | Actions… |
| `open-url` | `O` | Open URL |
| `mark` | `space` | Mark |
| `visual` | `V` | Visual range |
| `mark-all` | `ctrl+a` | Mark all |
| `cancel` | `esc` | Cancel / clear |
| `yank-id` | `y y` | Copy ID |
| `yank-title` | `y t` | Copy title |
| `yank-branch` | `y b` | Copy branch |
| `yank-url` | `y u` | Copy URL |
| `yank-markdown` | `y m` | Copy Markdown link |
| `help` | `?` | Help |
| `palette` | `:` | Commands |
| `quit` | `q` | Quit / back |
| `quit-now` | `ctrl+c` | Quit now |
| `filter-status` | `f s` | Filter: Status |
| `filter-priority` | `f p` | Filter: Priority |
| `filter-assignee` | `f a` | Filter: Assignee |
| `filter-label` | `f l` | Filter: Label |
| `filter-type` | `f t` | Filter: Type |
| `filter-milestone` | `f m` | Filter: Milestone |
| `filter-is:ready` | `f r` | Filter: Ready only |
| `filter-is:open,closed` | `f d` | Filter: Show done |
| `filter-clear` | `f c` | Filter: Clear |
| `sort-next` | `o n` | Sort: Next |
| `sort-priority` | `o p` | Sort: Priority |
| `sort-rank` | `o r` | Sort: Rank |
| `sort-updated` | `o u` | Sort: Updated |
| `sort-created` | `o c` | Sort: Created |
| `sort-due` | `o d` | Sort: Due |
| `sort-id` | `o i` | Sort: ID |
| `sort-reverse` | `o o` | Sort: Reverse |
| `group-status` | `z s` | Group: Status |
| `group-category` | `z C` | Group: Category |
| `group-priority` | `z p` | Group: Priority |
| `group-assignee` | `z A` | Group: Assignee |
| `group-label` | `z l` | Group: Label |
| `group-milestone` | `z m` | Group: Milestone |
| `group-type` | `z T` | Group: Type |
| `group-parent` | `z P` | Group: Parent |
| `group-none` | `z n` | Group: None |
| `fold` | `z a` | Toggle fold |
| `fold-open` | `z o` | Open fold |
| `fold-close` | `z c` | Close fold |
| `fold-all` | `z M` | Fold all |
| `unfold-all` | `z R` | Unfold all |
| `scroll-top` | `z t` | Scroll to top |
| `scroll-center` | `z z` | Scroll to center |
| `scroll-bottom` | `z b` | Scroll to bottom |
| `view-1` | `1` | View 1 |
| `view-2` | `2` | View 2 |
| `view-3` | `3` | View 3 |
| `view-4` | `4` | View 4 |
| `view-5` | `5` | View 5 |
| `view-6` | `6` | View 6 |
| `view-7` | `7` | View 7 |
| `view-8` | `8` | View 8 |
| `view-9` | `9` | View 9 |
<!-- keymap:end -->

Pickers preselect the current value and fuzzy-filter as you type. Assignee and
label pickers are multi-select; configured labels appear before labels in use.
Milestones show configured names, omit archived entries, then show undeclared
milestone keys in use. Typing an undeclared milestone key sets it as free text;
no picker edits project configuration. Status order is the configured status
order. `S`/`D` use workflow start/done, falling back to the first active/done
status. With coordination enabled, active transitions acquire first; closed
transitions write then release only matching caller-managed claims. Multi-item
activation is refused. Unsupported source
operations do not open a picker or write; the status bar shows the unavailable
capability.

`space` toggles a mark in the list; `V` starts a visual range and navigation
extends it; `ctrl-a` marks the current view. Edits target all marks/range items,
or the selected item when there are no marks. `esc` closes detail or clears
marks. `.` repeats the last edit against current targets and current versions.
Pending rows show `●`; failed rows show `!`. Opening detail shows the failure
reason and, for a conflict, both local and source versions. `u`/`ctrl-r` use the
engine's session history and never bypass optimistic version checks.

`yb` copies the shared design.md §8 branch name with OSC 52. The name is the
lowercase ID plus an ASCII title slug, capped at 48 characters on a word
boundary. `y` + `y`/`t`/`b`/`u`/`m` copies ID/title/branch/URL/Markdown link.
`O` launches only absolute HTTP(S) URLs without embedded credentials and uses
an argv-based platform opener, never a shell.

**Selection**

| Keys | Action |
| --- | --- |
| `space` | Mark or unmark the current item (outside the Criteria tab) |
| `V` | Visual range: move to extend, `esc` to end |
| `ctrl-a` | Mark every item in the view |

The mouse works too. Click a row to select it and double-click the same cell
within 400 ms to open it. Click a view tab or detail tab. Board column headers
focus that column, including empty columns; list headers sort by ID, priority,
status, title or update time, and clicking the sorted column reverses it. The wheel moves
three rows in the list/board or scrolls detail under the pointer. Dragging the
split divider resizes the panes, retaining the width in UI state. Mouse routing
is disabled while a modal input is open.

## Quick-add

`n` opens a single input line. Tokens become chips as you type, and `tab` expands the line into the full form.

```text
Debounce detail rendering !high @me #tui #perf ^BL-3 <BL-10 ~m-2 :feature
```

| Token | Field |
| --- | --- |
| `!urgent`, `!high`, `!medium`, `!low` | Priority. Unique prefixes work (`!h`). |
| `@handle`, `@me` | Assignee |
| `#label` | Label |
| `^ID` | Parent |
| `<ID` | Blocked by |
| `~milestone` | Milestone |
| `:type` | Type |

Everything else becomes the title. Escape a token with `\` (`\#1`), or put literal text in quotes. `bl new` accepts the same tokens. The parser lives in `internal/query` so the TUI, CLI, and MCP behave the same way.

## Forms and editors

Pickers are fuzzy, keyboard-first, and preselect current values. The full create and edit form covers every capability-supported field, plus description and criteria editing. Backlog.md forms omit native `rank`, and its priority picker omits `urgent`; shortcuts cannot select hidden provider choices.

The quick-add prompt previews parsed tokens as chips; `tab` opens the complete
form, before or after a title is typed. In the form, tab/shift-tab or up/down
move between fields, enter saves, and esc cancels. List values are
comma-separated; description, plan, notes, and summary accept `\\n` for line
breaks. Criteria and definition of done take one entry per line: `- [ ] text`
or `- [x] text` as in item files, or plain text for an unchecked entry, with an
indented `Evidence:` line after an entry to record its evidence. Invalid values
remain in the form with an inline error. `N` defaults
the parent to the selected item. Sources without create capability disable capture;
when multiple create-capable sources are configured, `n` offers a source picker
before quick-add; `N` uses the selected parent's source. Numeric parent and
blocker references resolve like `bl new`, including imported padded IDs and
the configured prefix for missing IDs. The optional column status default is
used when supplied by a board layout.

`e` opens the description in `$EDITOR`; native `E` edits a temporary copy of the
actual item file bytes (including unknown fields), while other sources use the
full form. `c` edits a new comment and `i` edits a note; these append on save.
The editor runs via Bubble Tea's terminal-suspending process command. Before
writing, it checks the version open when editing started. A conflict offers `y`
keep yours (retry against a freshly checked version), `t` keep theirs, or `r`
reopen with both versions in conflict markers. Invalid native frontmatter after
`E`, or a buffer that still has `<<<<<<< yours`/`>>>>>>> theirs` marker lines,
offers `r` reopen or `d` discard; markers are never written to an item. No conflict choice writes unchecked.

## State

UI state is kept per project identity in `$XDG_STATE_HOME/backlog/ui/<sha256>.json`
(default `~/.local/state/backlog/ui/`): last view, each tab's query (including
sort/group), layout, columns and selection, plus detail tab/open state and split
width. `ctrl-left`/`ctrl-right` resize the split in five-cell steps; a narrow
terminal clamps the rendered split without discarding the saved width.

Startup reads state before running Bubble Tea; after quitting, the final model
is saved atomically outside the update loop. Invalid, unsupported-version or
oversized state is ignored with a visible status bar warning. Missing views fall
back to Next; missing selections fall back to the first row. State is disposable
and never recreates deleted config views. Session query/layout overrides take
precedence over a tab's config while that declaration is unchanged; editing
the view in config (or a new built-in) discards the stale override. Saving a
view locks the config file's directory and leaves no lock file behind. Folds are
retained while switching tabs but are not persisted across restarts.

## Go-to measurements

On Apple M5 Max / Go 1.27.3, run:

```sh
task cli:test PKG=./internal/tui -- -run '^$' -bench '^BenchmarkGoTo50k$' -benchtime=20x -count=1
```

The benchmark measures Update → worker results → View over a synthetic immutable
50k-item ID/title catalog (all closed), not initial native cache construction.
`TestGoToNativeCatalog` separately verifies native cache coverage for open,
closed and archived items; `TestGoToWorkerAndDetachedDetail` verifies that a
blocked catalog load does not block input and stale results cannot reopen it.
The target `BL-50000` is found after the final keystroke. Per-keystroke p95:

| Query | ms |
| --- | --- |
| `B` | 5.632 |
| `BL` | 5.851 |
| `BL-` | 6.036 |
| `BL-5` | 6.469 |
| `BL-50` | 6.808 |
| `BL-500` | 6.713 |
| `BL-5000` | 6.690 |
| `BL-50000` | 7.757 |

Real 120×20 tmux captures with isolated XDG state confirmed the palette and
searchable help (`?`, then `workflow`): both show effective bindings and disabled
reasons, without exposing the underlying board/list or duplicating its header.

## Rendering and safety

Interactive sessions use heap-proportional GC pacing (20% growth) instead of
the batch CLI's fixed 300 MiB soft memory target. This bounds allocation headroom
without forcing continuous GC when live data exceeds a fixed target. Explicit
`GOGC` and `GOMEMLIMIT` settings are respected unchanged, and the caller's runtime
settings are restored on exit. A small explicit memory target can still trade
responsiveness for memory. The reference-host TUI RSS budgets pass; see
[benchmarks.md](benchmarks.md#production-tui-memory-bl-58).

All provider text is sanitized before rendering. CSI, OSC, and other control sequences are stripped (design.md D21). Width uses terminal cells (`x/ansi`). Under `NO_COLOR` there is no styling, and `--high-contrast` uses bold, underline, and reverse instead of faint text.
