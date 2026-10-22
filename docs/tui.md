# TUI

**Status:** Draft UX spec. BL-16 through BL-25 implement and refine it. Every key is remappable through user config `keys:`, and this file is the source for the default keymap and help text.

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

The default views are Next (`is:ready`, sort `next`), Active (`category:active`), Mine, All open, and Board. A view is a query plus sort, group, layout, and columns. Project and user config add saved views, and `ctrl-s` saves the current one.

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

Markdown renders asynchronously and is cached. While the selection moves, the header fields update at once and the body updates once the selection settles.

## Default keymap

**Navigation**

| Keys | Action |
| --- | --- |
| `j`/`k`, `↓`/`↑` | Move. A count prefix works (`5j`). |
| `gg`, `G`, `NG` | First row, last row, row N |
| `ctrl-d`/`ctrl-u`, `ctrl-f`/`ctrl-b` | Half page, full page |
| `[`/`]` | Previous or next group |
| `enter` | Open or close the detail pane |
| `tab`/`shift-tab` | Detail tabs. In list focus, moves focus to detail. |
| `esc` | Back, clear marks, or cancel |
| `1`–`9`, `gt`/`gT` | Jump to a view; next or previous view |
| `ctrl-p` | Go to any item by fuzzy ID or title, including done and archived |
| `?`, `:` | Help (searchable); command palette |
| `W` | Toggle between project and workspace scope |
| `R` | Resync sources |
| `q`, `ctrl-c` | Quit. In detail, `q` goes back. |

**Filter, sort, and group**

| Keys | Action |
| --- | --- |
| `/` | Edit the query with completion (query.md). `enter` applies; `esc` restores the previous query. |
| `f` + key | Quick filter. The second key picks the filter: `s` status, `p` priority, `a` assignee, `l` label, `t` type, `m` milestone, `r` ready only, `d` show done, `c` clear. |
| `F` | Clear the filter |
| `o` + key | Sort. The second key picks the sort: `n` next, `p` priority, `r` rank, `u` updated, `c` created, `d` due, `i` ID, `o` reverse. |
| `z` + key | Group and fold. The second key picks the group: `s` status, `c` category, `p` priority, `a` assignee, `l` label, `m` milestone, `t` type, `P` parent, `n` none. `za` toggles a fold, `zM`/`zR` fold or unfold all. |
| `ctrl-s` | Save the current query, sort, group, and layout as a view |

**Item edits**

These apply to the marked items if any, otherwise to the current item. An edit takes effect at once, and `u` reverts it.

| Keys | Action |
| --- | --- |
| `s` | Status picker |
| `<`/`>` | Previous or next status in workflow order |
| `p` | Priority picker. Hotkeys inside it: `u`, `h`, `m`, `l`, `n`. |
| `a`, `A` | Assignee picker; toggle assigning yourself |
| `l` | Label picker: multi-select, type to filter or create |
| `m` | Milestone picker |
| `b` | Blocker editor: fuzzy item picker to add, `d` to remove |
| `P` | Parent picker |
| `t` | Edit the title inline |
| `e`, `E` | Edit the description in `$EDITOR`; edit the whole item (the file for native sources, a full form for others) |
| `c`, `i` | Add a comment; append a note (native sources) |
| `x`, `space` | In the Criteria tab, toggle the criterion under the cursor |
| `S`, `D` | Start (claims first with Worklease); Done |
| `C` | Claim or release (Worklease) |
| `dd` | Archive (asks for confirmation) |
| `.` | Repeat the last edit on the current or marked items |
| `u`, `ctrl-r` | Undo, redo |
| `n`, `N` | Quick-add; quick-add a child of the current item. In board layout, the new item takes the column's status. |
| `!` | Actions menu (design.md §8). An action's own `key` binds directly. |
| `y` + key | Yank. The second key picks what to copy: `y` ID, `t` title, `b` branch name (`bl-12-debounce-detail`), `u` URL, `m` Markdown link. |
| `O` | Open the item's URL or provider page |

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

UI state is kept per project in XDG state: last view, query, sort, group, layout, selection, detail tab, and split width. Quitting and reopening restores it exactly.

## Rendering and safety

All provider text is sanitized before rendering. CSI, OSC, and other control sequences are stripped (design.md D21). Width uses terminal cells (`x/ansi`). Under `NO_COLOR` there is no styling, and `--high-contrast` uses bold, underline, and reverse instead of faint text.
