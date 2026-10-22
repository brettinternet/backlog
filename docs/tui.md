# TUI

**Status:** Draft UX spec. BL-16 through BL-25 implement and refine it. Every key is remappable through user config `keys:`, and this file is the source for the default keymap and help text.

## Implemented list and detail shell

Bare `bl` opens the list when stdin and stdout are terminals; `bl tui`
explicitly requires both. The frame paints a loading state before native
sources load, then consumes immutable engine projections. Source badges show
syncing, synced, stale, or error. Rows show ID, priority, raw status, open
blockers, child progress, title and pending/failed glyphs. When the list (or
the list pane beside detail) is narrower than 80 columns, only ID, title and
edit glyphs remain. The list shows loading, not "No matching items.", while a
source syncs or a newer result is pending. The footer reports the active query and
pending/failed counts in that projection, including folded rows.

Next, Active, Mine and All are built-in list tabs, grouped by status. `j`/`k`
and arrows move by display row (including group headers); counts, `gg`, `G`,
`NG`, half/full pages, `[`/`]` group jumps and `za` folding work. `gt`/`gT`
cycle tabs. Live refreshes keep folds and keep the cursor on the same item or
group header. Each tab retains its query, folds and stable-item selection;
reordered rows keep the same item, and a missing item falls back to the first row.
A digit waits 350 ms to distinguish `1`–`9` view shortcuts from count-prefixed
motions; unavailable numbers do nothing. `gt`/`gT` reach every saved tab. `R`
requests a background refresh. `?` toggles help. `NO_COLOR` disables styling;
`--high-contrast` uses bold and underline, never faint text. All external text
is sanitized and widths use terminal cells.

`enter` toggles detail. At 120 columns or more it opens beside the list; below
120 columns it replaces the list. `tab` focuses the detail pane or advances its
tabs, and `shift-tab` moves to the previous tab. In detail focus, `j`/`k` and
page keys scroll, `m` expands a capped body, and `esc`/`q` close the pane.
At split widths, `h`/`left` returns focus to the list without closing detail,
so held navigation keeps the header current while postponing body work.
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
Claim columns await BL-32.

Capture a real render without opening a user's pane:

```sh
task cli:build
scripts/tui-snapshot --width 80 --height 24 -- bin/bl -C /path/to/native-project tui
scripts/tui-snapshot --width 160 --height 48 --keys g t g t g t -- bin/bl -C /path/to/native-project tui
```

The helper owns a private tmux server, waits for a settled render, writes pane
text to stdout, and shuts down only its own server, including on failure.

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
a partial or failed refresh never proves deletion.

## Principles

1. **Never wait.** Paint from the cache at once, sync behind it, and apply edits optimistically. A spinner marks work in flight; it never blocks input (design.md D14, D15).
2. **Every common action is one or two keys.** Prefix keys open a which-key popup that names the next keys. `:` opens a fuzzy command palette listing every action with its binding.
3. **Vim muscle memory.** Counts, `gg`/`G`, `ctrl-d`/`ctrl-u`, `.` to repeat, `u` to undo, `/` to filter, `z` for folds.
4. **One program, every view.** Views are tabs. Layouts are list, board, and tree. Detail is a pane.
5. **Show only what works.** Actions appear only when the source supports them (D10). Disabled reasons show in the palette.
6. **Color is never the only signal.** Glyphs and text carry meaning under `NO_COLOR` and `--high-contrast`.

## Layout

```text
 backlog · project · Next 12  Active 3  Mine 5  All 318  Board          tasks ✓ synced
  ID     P  Status       Title                                   ⊘   ▣    Claim
  BL-12  ▲  To Do        Debounce detail rendering while…        ─   ─    ─
> BL-14  ▲  To Do        Board layout with column moves          1   ─    mine
  BL-15  ■  In Progress  Go-to-item fuzzy finder                 ─   2/3  agent@mbp
──────────────────────────────────────────────────────────────────────────────────
 is:ready sort:next                                      2 pending · ? help · : cmd
```

At 120 columns or more, the detail pane opens beside the list. Below that, it replaces the list. Below 80 columns, secondary columns hide. The header shows one badge per source (synced, syncing ⠋, stale, error). The footer shows the active query, pending and failed edits, and hints.

| Row glyph | Meaning |
| --- | --- |
| `▲▲` `▲` `■` `▼` | Priority: urgent, high, medium, low |
| `⊘ N` | N open blockers |
| `▣ a/b` | Child progress |
| Claim column | Worklease holder: `mine`, `agent@host`, or `expired` |
| `●` before the title | Pending local edit |
| `!` before the title | Failed or conflicted edit; open the item for detail |

## Views and layouts

The built-in tabs are Next (`is:ready`, sort `next`), Active (`category:active`),
Mine (`assignee:me`), and All open. They precede project `views`, user `views`,
and user `projects.<project-id>.views`, each in declaration order. Same-named
views in different layers remain separate tabs; names within a layer must be
unique. This additive tab catalog does not change D16's list-replacement rule
for effective configuration. The built-in Board tab follows All and shows open
items grouped by status.

A view has `name`, `query`, optional `sort`, `group`, `layout`, and `columns`.
Explicit sort/group fields override query directives. Columns may be `id`,
`priority`, `status`, `blockers`, `children`, and `title`, in display order;
without columns the standard list is used. Below 80 columns, ID/title remain.
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

The detail pane has these tabs:

- **Overview:** fields, plus readiness and why
- **Description**
- **Criteria:** criteria and definition of done
- **Plan/Notes**
- **Comments**
- **Relations:** blockers, blocking, parent, children, each with live status

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
prefixes, displays its available continuations. Planned workspace, coordination,
and configured-action entries remain explicitly disabled until their tasks ship.

User config maps stable action IDs to one key sequence; an empty value unbinds
an action. An override replaces all aliases. Unknown actions, invalid key names,
exact collisions and ambiguous prefix bindings fail TUI startup with both
conflicting action IDs and bindings. Swap bindings by overriding both actions:

```yaml
keys:
  move-down: J
  move-up: K
  first: v v
  workspace: ''
```

<!-- keymap:start -->
| Action ID | Keys | Action |
| --- | --- | --- |
| `move-down` | `j`, `down` | Move down / scroll detail |
| `move-up` | `k`, `up` | Move up / scroll detail |
| `first` | `g g` | First row |
| `last` | `G` | Last row / count G to row N |
| `half-down` | `ctrl+d`, `pagedown` | Half page down |
| `half-up` | `ctrl+u`, `pageup` | Half page up |
| `page-down` | `ctrl+f` | Full page down |
| `page-up` | `ctrl+b` | Full page up |
| `previous-group` | `[` | Previous group / board column |
| `next-group` | `]` | Next group / board column |
| `left` | `h`, `left` | Tree parent / board left / focus list |
| `right` | `l`, `right` | Tree child / board right / labels in list |
| `detail` | `enter` | Toggle detail |
| `next-detail-tab` | `tab` | Focus detail / next detail tab |
| `previous-detail-tab` | `shift+tab` | Focus detail / previous detail tab |
| `cancel` | `esc` | Close detail / clear marks / cancel |
| `next-view` | `g t` | Next view |
| `previous-view` | `g T` | Previous view |
| `go-to` | `ctrl+p` | Go to any item (including archives) |
| `help` | `?` | Search help |
| `palette` | `:` | Command palette |
| `workspace` | `W` | Toggle project / workspace scope |
| `refresh` | `R` | Resync sources |
| `quit` | `q` | Back from detail / quit |
| `quit-now` | `ctrl+c` | Quit |
| `query` | `/` | Edit query |
| `clear-filter` | `F` | Clear filter |
| `save-view` | `ctrl+s` | Save view |
| `status` | `s` | Status picker |
| `previous-status` | `<` | Previous workflow status |
| `next-status` | `>` | Next workflow status |
| `priority` | `p` | Priority picker |
| `assignees` | `a` | Assignee picker |
| `assign-me` | `A` | Toggle assigning yourself |
| `milestone` | `m` | Milestone picker / expand detail body |
| `blocker` | `b` | Blocker editor |
| `parent` | `P` | Parent picker |
| `title` | `t` | Edit title |
| `description` | `e` | Edit description in editor |
| `edit` | `E` | Edit whole item |
| `comment` | `c` | Add comment |
| `note` | `i` | Append note |
| `criterion` | `x` | Toggle criterion (Criteria tab) |
| `start` | `S` | Start item |
| `done` | `D` | Finish item |
| `claim` | `C` | Claim / release |
| `archive` | `d d` | Archive (confirm) |
| `repeat` | `.` | Repeat last edit |
| `undo` | `u` | Undo |
| `redo` | `ctrl+r` | Redo |
| `new` | `n` | Quick-add |
| `new-child` | `N` | Quick-add child |
| `actions` | `!` | Actions menu |
| `yank-id` | `y y` | Copy ID |
| `yank-title` | `y t` | Copy title |
| `yank-branch` | `y b` | Copy branch name |
| `yank-url` | `y u` | Copy URL |
| `yank-markdown` | `y m` | Copy Markdown link |
| `open-url` | `O` | Open safe provider URL |
| `mark` | `space` | Mark item / toggle criterion |
| `visual` | `V` | Visual range |
| `mark-all` | `ctrl+a` | Mark current view |
| `split-left` | `ctrl+left` | Shrink list pane |
| `split-right` | `ctrl+right` | Grow list pane |
| `filter-status` | `f s` | Filter: status |
| `filter-priority` | `f p` | Filter: priority |
| `filter-assignee` | `f a` | Filter: assignee |
| `filter-label` | `f l` | Filter: label |
| `filter-type` | `f t` | Filter: type |
| `filter-milestone` | `f m` | Filter: milestone |
| `filter-is:ready` | `f r` | Filter: ready only |
| `filter-is:open,closed` | `f d` | Filter: show done |
| `filter-clear` | `f c` | Filter: clear |
| `sort-next` | `o n` | Sort: next |
| `sort-priority` | `o p` | Sort: priority |
| `sort-rank` | `o r` | Sort: rank |
| `sort-updated` | `o u` | Sort: updated |
| `sort-created` | `o c` | Sort: created |
| `sort-due` | `o d` | Sort: due |
| `sort-id` | `o i` | Sort: ID |
| `sort-reverse` | `o o` | Sort: reverse |
| `group-status` | `z s` | Group: status |
| `group-category` | `z c` | Group: category |
| `group-priority` | `z p` | Group: priority |
| `group-assignee` | `z A` | Group: assignee |
| `group-label` | `z l` | Group: label |
| `group-milestone` | `z m` | Group: milestone |
| `group-type` | `z t` | Group: type |
| `group-parent` | `z P` | Group: parent |
| `group-none` | `z n` | Group: none |
| `group-fold` | `z a` | Group: toggle fold |
| `group-fold-all` | `z M` | Group: fold all |
| `group-unfold-all` | `z R` | Group: unfold all |
| `view-1` | `1` | Jump to view 1 |
| `view-2` | `2` | Jump to view 2 |
| `view-3` | `3` | Jump to view 3 |
| `view-4` | `4` | Jump to view 4 |
| `view-5` | `5` | Jump to view 5 |
| `view-6` | `6` | Jump to view 6 |
| `view-7` | `7` | Jump to view 7 |
| `view-8` | `8` | Jump to view 8 |
| `view-9` | `9` | Jump to view 9 |
<!-- keymap:end -->

Pickers preselect the current value and fuzzy-filter as you type. Assignee and
label pickers are multi-select; configured labels appear before labels in use.
Milestones show configured names, omit archived entries, then show undeclared
milestone keys in use. Typing an undeclared milestone key sets it as free text;
no picker edits project configuration. Status order is the configured status
order. `S`/`D` use workflow start/done, falling back to the first active/done
status; `S` does not acquire a claim yet (BL-32). Unsupported source
operations do not open a picker or write; the footer shows the unavailable
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
status or title (custom column layouts ignore header sorting). The wheel moves
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

Pickers are fuzzy, keyboard-first, and preselect current values. The full create and edit form covers every capability-supported field, plus description and criteria editing.

The quick-add prompt previews parsed tokens as chips; `tab` opens the complete
form. In the form, tab/shift-tab or up/down move between fields, enter saves,
and esc cancels. List values are comma-separated; description, plan, notes,
and summary accept `\\n` for line breaks. Criteria and definition of done use
JSON arrays of `{ "text": "...", "checked": false }` entries, with optional
`evidence`. Invalid values remain in the form with an inline error. `N` defaults
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
oversized state is ignored with a visible footer warning. Missing views fall
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

All provider text is sanitized before rendering. CSI, OSC, and other control sequences are stripped (design.md D21). Width uses terminal cells (`x/ansi`). Under `NO_COLOR` there is no styling, and `--high-contrast` uses bold, underline, and reverse instead of faint text.
