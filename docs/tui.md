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
confirmation, and failure detail. Picker catalogs and fuzzy filtering run in
Bubble Tea commands; source I/O and backlog-sized work stay off `Update` (D14).
Board/tree layouts, key remapping, the command palette, and mouse remain planned.
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
for effective configuration. Board is added when its layout ships.

A view has `name`, `query`, optional `sort`, `group`, `layout`, and `columns`.
Explicit sort/group fields override query directives. Columns may be `id`,
`priority`, `status`, `blockers`, `children`, and `title`, in display order;
without columns the standard list is used. Below 80 columns, ID/title remain.
Unavailable layouts (including board/tree for now) retain their setting but
render a list with a footer notice.

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

The detail pane has these tabs:

- **Overview:** fields, plus readiness and why
- **Description**
- **Criteria:** criteria and definition of done
- **Plan/Notes**
- **Comments**
- **Relations:** blockers, blocking, parent, children, each with live status

Markdown renders asynchronously and is cached. While the selection moves, the header fields update at once and the body updates once the selection settles. Bodies are capped at 32 KiB by default; `m` expands the cap to 128 KiB, and an indicator remains if content is still truncated.

## Default keymap

**Navigation**

| Keys | Action |
| --- | --- |
| `j`/`k`, `↓`/`↑` | Move. A count prefix works (`5j`). |
| `gg`, `G`, `NG` | First row, last row, row N |
| `ctrl-d`/`ctrl-u`, `ctrl-f`/`ctrl-b` | Half page, full page |
| `[`/`]` | Previous or next group |
| `enter` | Toggle the detail pane and focus it when opening |
| `tab`/`shift-tab` | In list focus, focus detail; in detail focus, cycle tabs |
| `j`/`k`, arrows, page keys | In detail focus, scroll the current body |
| `m` | In detail focus, show more of a capped body (or show less) |
| `h`/`←` in split detail | Focus the list without closing the pane |
| `esc` | Close detail, clear marks, or cancel |
| `1`–`9`, `gt`/`gT` | Jump to a view; next or previous view |
| `ctrl-p` | Go to any item by fuzzy ID or title, including done and archived |
| `?`, `:` | Help (searchable); command palette |
| `W` | Toggle between project and workspace scope |
| `R` | Resync sources |
| `q`, `ctrl-c` | In detail, `q` goes back; otherwise quit. `ctrl-c` quits. |

**Filter, sort, and group**

| Keys | Action |
| --- | --- |
| `/` | Edit the query with completion (query.md). `enter` applies; `esc` restores the previous query. |
| `f` + key | Quick filter. The second key picks the filter: `s` status, `p` priority, `a` assignee, `l` label, `t` type, `m` milestone, `r` ready only, `d` show done, `c` clear. |
| `F` | Clear the filter |
| `o` + key | Sort. The second key picks the sort: `n` next, `p` priority, `r` rank, `u` updated, `c` created, `d` due, `i` ID, `o` reverse. |
| `z` + key | Group and fold. The second key picks the group: `s` status, `c` category, `p` priority, `A` assignee, `l` label, `m` milestone, `t` type, `P` parent, `n` none. `za` toggles a fold, `zM`/`zR` fold or unfold all. |
| `ctrl-s` | Save the current query, sort, group, and layout as a view |

**Item edits**

These apply to the marked items if any, otherwise to the current item. An edit takes effect at once, and `u` reverts it.

| Keys | Action |
| --- | --- |
| `s` | Status picker |
| `<`/`>` | Previous or next status in workflow order |
| `p` | Priority picker. Hotkeys inside it: `u`, `h`, `m`, `l`, `n`. |
| `a`, `A` | Assignee picker; toggle assigning yourself |
| `l` | Label picker: multi-select, type to filter or create. Configured labels come first. |
| `m` | Milestone picker. Shows names and hides archived milestones. Typing an undeclared key sets it as free text. |
| `b` | Blocker editor: fuzzy item picker to add, `d` to remove |
| `P` | Parent picker |
| `t` | Edit the title inline |
| `e`, `E` | Edit the description in `$EDITOR`; edit the whole item (the file for native sources, a full form for others) |
| `c`, `i` | Add a comment; append a note (native sources) |
| `x`, `space` | In the Criteria tab, toggle the criterion under the cursor |
| `S`, `D` | Start at workflow status (claim acquisition is BL-32); Done status |
| `C` | Claim or release (Worklease) |
| `dd` | Archive (asks for confirmation) |
| `.` | Repeat the last edit on the current or marked items |
| `u`, `ctrl-r` | Undo, redo |
| `n`, `N` | Quick-add; quick-add a child of the current item. In board layout, the new item takes the column's status. |
| `!` | Actions menu (design.md §8). An action's own `key` binds directly. |
| `y` + key | Yank. The second key picks what to copy: `y` ID, `t` title, `b` branch name (`{branch}`, design.md §8), `u` URL, `m` Markdown link. |
| `O` | Open the item's URL or provider page (absolute HTTP(S), no credentials) |

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

The mouse works too. Click a row to select it and double-click to open it. Click a tab or column header. The wheel scrolls. Dragging the split divider resizes the panes.

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

`$EDITOR` editing suspends the TUI, edits a temporary file for the chosen section (or the item file itself for `E` on native sources), and reapplies the result as a mutation. Before writing, it checks the version that was open when editing started. A conflict offers to keep yours, keep theirs, or reopen with both. Invalid frontmatter after `E` offers to reopen or discard.

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

## Rendering and safety

All provider text is sanitized before rendering. CSI, OSC, and other control sequences are stripped (design.md D21). Width uses terminal cells (`x/ansi`). Under `NO_COLOR` there is no styling, and `--high-contrast` uses bold, underline, and reverse instead of faint text.
