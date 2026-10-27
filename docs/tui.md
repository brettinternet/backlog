# TUI

Run `bl` or `bl tui [QUERY] [--view|-V NAME]` in a terminal. QUERY is ANDed with
every tab's query for the session (`bl tui source:eng`); it filters only, so
`sort:` and `group:` are usage errors. `--view` starts on the first tab with that
exact name instead of the last one used. Every key is remappable, and the
[keymap table](#default-keymap) below is the tested source of the defaults.

## Principles

1. **Never wait.** Paint from cache, sync behind it, apply edits optimistically.
   Work in flight never blocks input (design.md D14, D15).
2. **Common actions take one or two keys.** Prefix keys open a which-key menu;
   `:` lists every action with its binding.
3. **Vim muscle memory.** Counts, `gg`/`G`, `ctrl-d`/`ctrl-u`, `.` repeat, `u`
   undo, `/` filter, `z` folds. Emacs motion and cancel keys work too.
4. **One program.** Views are tabs; layouts are list, board, and tree; detail is
   a pane.
5. **Show only what works.** Actions appear only when the source supports them
   (D10); the palette says why one is disabled.
6. **Color is never the only signal.** Glyphs and text carry meaning under
   `NO_COLOR` and `--high-contrast`.

Status edits offer only the statuses the item's source accepts (design.md
D35): GitHub's `open`, `closed`, `unknown`, and mapped labels or project
options; Linear's team states; Jira's transitions discovered for the issue,
omitting unsupported required-field transitions; and for other sources the
configured statuses, narrowed by `sources[].statuses`. Replace a
Jira single assignee through the confirmed CLI/MCP operation, not the additive
assignee picker ([Jira](jira.md#writes)).

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

| Glyph | Meaning |
| --- | --- |
| `▲▲` `▲` `■` `▼` | Priority: urgent, high, medium, low |
| `◌` `○` `◐` `✓` `✕` | Status category: backlog, todo, active, done, canceled |
| `⊘ N` | N open blockers |
| `▣ a/b` | Closed descendants / all descendants |
| `●` after the ID | Edit pending |
| `!` after the ID | Edit failed or conflicted; open the item for detail |
| `•` in the gutter | Marked |
| `✓` `~` `!` in the header | Source synced, partial, error; no mark until the first sync (or first sprint catalog) finishes, then the last result while a refresh runs |

**Header:** view tabs and one badge per source. Untrusted hooks, actions, or
adapters replace the badges with `⚠ Untrusted project config · bl trust --show`;
untrusted adapters aren't opened, but native sources stay browsable.

**Rows:** priority, ID, status, title with labels, open blockers, child
progress, assignee, and time since update. As the pane narrows, columns drop in
this order so the title keeps at least two-fifths of the width: updated,
assignee, children, blockers, claim, then the status name (the icon stays).
Grouping by status shows only the icon. Opaque provider IDs (Linear states and
users, Jira accountIds, GitHub Projects options) show the provider's name
everywhere, including group headers and detail; a configured status name wins
([D11](design.md#3-decision-register)).

**Status bar:** the query, or the active input, prompt, or error. A notice shows
until the next key. On the right: a pending prefix or count, marks, pending `●`
and failed `!` counts, `↻` while results or a sync take over 300 ms, and the
cursor position. A loading indicator, once shown, stays at least 300 ms, so
nothing flashes.

At 30 rows or taller, a blank row separates the body from the header and from
the status bar. At 120 columns or wider, detail opens beside the list; narrower, it replaces the
list. Pickers, quick-add, and which-key menus open above the status bar. The
palette, go-to, and the full form are centered panels. Help fills the body.

## Startup

The frame paints immediately, then the first 256 items in filename order, then
the rest. The partial list is usable and editable, but readiness, order, and
counts can change, and missing blockers show as unknown until loading finishes.
While a source syncs, an empty list stays blank, then shows loading after 300 ms,
never "No matching items." Switching tabs keeps the old rows until the new ones
are ready, or loading shows.

`R` refreshes now, with `↻` once it takes over 300 ms. For remote sources it
runs a full reconciliation, which catches relation edits and deletions that the
minute poll misses ([linear.md](linear.md#sync)). Remote sources poll once a
minute and show up to 256 cached rows before the network answers. Opening an
item's detail reads it fresh and updates its row; an item the provider no
longer has turns stale. Cached rows appear in Open, but a stale item is
never ready in Next, and a stale done blocker doesn't satisfy a dependency.
Quota errors stay visible until the provider's retry time.

## Views and layouts

| Tab | Query | Groups |
| --- | --- | --- |
| Next | `is:ready sort:next` | none |
| Active | `category:active` | status |
| Mine | `assignee:me is:open` | status |
| Open | `is:open` | status |
| Board | `is:open -category:backlog` | status columns (open statuses only) |

Saved views follow, in order: project `views`, user `views`, then user
`projects.<project-id>.views`. Same-named views in different layers are separate
tabs; names within a layer must be unique.

```yaml
views:
  - name: My bugs
    query: 'assignee:me label:bug is:open'
    sort: priority
    group: milestone
    layout: list        # list | board | tree
    columns: [id, priority, status, title, updated]
  - {name: Sprint, query: "sprint:current", layout: board}
```

Explicit `sort` and `group` override query directives. Columns are `id`,
`priority`, `status`, `blockers`, `children`, `claim`, `assignee`, `updated`, and
`title`, in display order.

`gt`/`gT` cycle tabs; `1`–`9` jump to one (a digit waits 350 ms in case it starts
a count). Each tab keeps its query, folds, and selected item across refreshes,
following the item into another group; if it disappears, the row taking its
place is selected. A tab whose query, layout, or columns differ from its
definition is marked `•`; `F` resets it, and `ctrl-s` keeps the edit as a view.

`ctrl-s` saves the current view. Edit the name (`ctrl-u` clears it), press `tab`
to switch between user and project config, then `enter`. Saving replaces a
same-named view in that file or appends a new one, rewriting only that entry
under a file lock. The tab appears at once and persists.

| Layout | Navigation | Edits |
| --- | --- | --- |
| List | Group headers with counts. `za` folds, `[` `]` jump groups. | All item keys |
| Board | A column per status (or group value). `h`/`l` change column, `j`/`k` move within it. | `<`/`>` move to the previous or next status the source accepts |
| Tree | Parent/child hierarchy. `h` collapses or goes to the parent; `l` expands or goes to the first child. | All item keys |

**Board:** configured statuses in workflow order, including empty ones, then
undeclared statuses. The Board tab hides drafts, which are never ready
(design.md D29); `fb` shows them. A status or category filter drops empty
columns; a negated one drops only the columns it excludes. Other group keys use
their group order. Columns are at
least 28 cells wide, scroll horizontally, and keep their own cursor. `<`/`>`
always change workflow status, even when grouped by something else. Quick-add
uses the focused column's status. `l` navigates instead of opening the label
picker.

**Tree:** follows `parent` (filling gaps from explicit child relations). A filter
keeps each match's ancestors, marked `·` and faint (underlined in high
contrast); they stay selectable. `za` toggles a branch; `zM`/`zR` collapse or
expand all. `▣ a/b` counts all descendants, including filtered-out ones. Missing
parents become roots; cycles are cut so each item appears once (`bl check`
finds them). Indentation is capped for very deep trees.

## Navigation

`j`/`k` and arrows move by display row, including group headers. Counts work
(`5j`, `25G`), as do `gg`, `G`, `H`/`M`/`L` (top, middle, or bottom of the
screen), half and full pages, `[`/`]`, and `za`. On a board, these move within
the focused column.

`enter`, `l`, or `right` opens detail (`enter` on a group header folds it). In
detail, `j`/`k` and page keys scroll, `tab`/`shift-tab` change tabs, `m` shows
more of a cut-off body (otherwise it picks a milestone), and `enter`, `esc`, or
`q` close (`enter` follows a relation in Relations). Edit and copy keys work in
detail on the item shown, never on marks. If an edit moves the item out of the
view, detail keeps showing it until you move. `J`/`K` select the next or
previous item from either pane, counting from where a moved item was. At split
widths, `h` returns to the list without closing detail; a second `h` closes it.
Filter, sort, group, fold, and layout keys (`f`, `o`, `z`, `F`, `B`, `T`) act
on the list from split detail too, and detail keeps its item if it leaves the
view. Narrower, they need the list focused.

In detail, `/` finds text in the tab in view, case-insensitively, jumping as
you type from where it opened. `enter` keeps the search; `n`/`N` then step
through matching lines, wrapping, instead of creating items. The search carries
across tabs and items; `esc` clears it before closing detail.

`ctrl-p` opens go-to: fuzzy search over every ID and title, including closed and
archived items (up to 100 matches). `enter` opens detail without changing the
view; `esc` or `q` returns.

`:` opens the palette: every action, its bindings, and why it's disabled if it
is. `?` opens the same list as read-only help. Both are searchable.

## Detail

The header (ID, status, priority, title) and tab counts follow the selection
immediately. The body loads once the selection rests for 50 ms; switching tabs
or `m` renders at once. The previous body stays until the new one is ready, or
loading shows.

| Tab | Content |
| --- | --- |
| Overview | Set fields (milestone names beside keys), readiness and why, description |
| Criteria | Acceptance criteria and definition of done, with progress |
| Notes | Plan, notes, summary |
| Comments | Comments |
| Relations | Blockers → item → what it blocks, then parent, children with progress, duplicate of, related, each with live status. Blocking, children, and related include inbound links; related omits items linked by blocks. |

In Criteria and Relations, `j`/`k` move a cursor over the entries. In
Relations, `enter` opens the entry's item in detail, as go-to does: the view and
list selection stay. `backspace` returns to the previous item, back through
every jump to the original selection. A target that isn't loaded shows `?`,
and following it says why: its source isn't configured, failed, or is still
loading, or the item isn't in it.

Bodies render up to 32 KiB; `m` raises the limit to 128 KiB. An indicator shows
when content is still cut off.

## Filtering, sorting, grouping

In the list, `/` edits the query live, after a space so typing starts a new clause.
`up`/`down` highlight a candidate; an unfinished word highlights the first. `tab` inserts the highlighted candidate, or the first.
`enter` inserts the highlighted candidate, then applies the query if it parses;
a field name stays open for its value. `esc` restores the previous query.
Syntax errors show a byte position and keep the last valid rows; unknown values
warn. Neither shows while candidates still match the word being typed. ID and
title matches are bold and underlined.

`f`, `o`, and `z` open menus for filter, sort, and group/fold. Field filters open
a value picker. `fr` toggles ready-only; `fd` toggles closed items (not archived);
`fb` toggles drafts (`-category:backlog`). Each toggles only its own term, so
`is:mine` and other `is:` terms stay.
`fc` clears filters but keeps sort and group. `F` resets the tab to its
definition: query, layout, columns, and folds. `oo` reverses every sort key.
`zA` groups by assignee, since `za` folds.

## Editing

Edits apply at once, show `●` until confirmed, and roll back with `!` on failure.
Opening a failed item shows the reason, and for a conflict, both versions.
Nothing retries automatically. `u`/`ctrl-r` undo and redo through version
checks; an external edit breaks the undo chain. Undoing a create archives the
item. GitHub has no archive: undo deletes the issue or closes it as not planned,
the status line says which, and there's no redo.

**Pickers** preselect current values and fuzzy-filter. Assignees and labels are
multi-select, configured labels first. Milestones show names, hide archived
entries, then list undeclared keys in use; typing a new key sets it. Sprints
(Linear and Jira only) list the source's open sprints, current and next first;
opening the picker loads the catalog if no view needed it yet. Status
order follows config, then the source's undeclared keys. `<`/`>` step to the
nearest accepted status in that order, skipping columns the item's source
can't take; with none in that direction the status bar says so, naming the
source. Assignee and status pickers and filter completion show
provider names for opaque IDs and write the ID. Pickers never edit config. Unsupported operations show the
missing capability in the status bar instead of opening.

**Marks:** `space` marks an item (outside the Criteria tab), `V` starts a visual
range, `ctrl-a` marks the view. Edits from the list apply to the marks, or to
the selection when nothing is marked. `esc` clears marks or closes detail. `.`
repeats the last edit on the current targets.

**Status keys:** `S` and `D` resolve `workflow.start` and `workflow.done` per
item source, as `bl start` and `bl done` do. A refused item names its source.

**Copy** with OSC 52: `yy` ID, `yt` title, `yb` branch name, `yu` URL, `ym`
Markdown link. The branch name is the lowercase ID plus an ASCII title slug, at
most 48 characters, cut at a word: `bl-12-debounce-detail-rendering`.

`O` opens the item URL. Only absolute HTTP(S) URLs without credentials open,
through the platform opener, never a shell.

### Quick-add

`n` opens a one-line prompt. Tokens become chips as you type; `tab` opens the
full form.

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
| `+source` | Source to create in |

Everything else is the title. Escape a token with `\` (`\#1`) or quote it.
`bl new` and MCP use the same parser (`internal/query`). References resolve like
`bl new`.

`n` preselects the source `bl new` would use: `defaults.source`, else the only
source that can create. Without either, it asks for one first. `+source`
switches it. `N` creates a child of the selected item, in its source. Sources
that can't create disable both.
On a board, the focused column sets the status.

### Forms and editors

The full form covers every field the source supports, plus description and
criteria. `tab`/`shift-tab` or `up`/`down` move between fields, `enter` saves,
`esc` cancels. Lists are comma-separated. Description, plan, notes, and summary
accept `\n` for line breaks. Criteria take one entry per line, as in item files:

```text
- [ ] Handles empty input
- [x] Preserves CRLF
  Evidence: go test ./internal/format passed
Plain text is an unchecked entry
```

Invalid values stay in the form with an error. Backlog.md forms omit `rank` and
`urgent`.

| Key | Opens in `$EDITOR` | Saves by |
| --- | --- | --- |
| `e` | Description | Replacing it |
| `E` | Native: the whole item file. Others: the full form. | Replacing it |
| `c` | A new comment | Appending |
| `i` | Notes | Replacing them |

A source that can only append notes opens an empty note instead. `c` comments
as the account user on GitHub, Linear, and Jira, else as `me.default`; without
one, it is disabled.

If the item changed while you edited:

| Key | Choice |
| --- | --- |
| `y` | Keep yours (checked against the new version) |
| `t` | Keep theirs |
| `r` | Reopen with both versions in conflict markers |

Invalid frontmatter, or leftover `<<<<<<< yours`/`>>>>>>> theirs` markers, offer
`r` reopen or `d` discard. Markers are never written to an item.

## Actions

`!` lists configured actions whose `when` query matches the item; the palette
and help include them too, with disabled reasons. `confirm` actions ask `y`/`n`.
Attached actions suspend the TUI like `$EDITOR`; detached ones return at once.
Bind one with `key:` in its declaration or `action:NAME` in user `keys`. See
[Automation](automation.md).

## Coordination

With coordination enabled, wide lists add a Claim column (`mine`, the holder's
`agent@host`, or `expired`), also available as a view column. `C` claims or
releases the selected item. `S` and other active transitions claim first; closing
releases your claim. Several items can't be activated at once. Claims refresh
every five seconds and after each operation. If a newer UI request cancels a
poll, it uses the last completed claim list so external status changes reach
lists and boards without waiting for another provider call. Claims retry on the
next poll; expiry checks still apply. A canceled startup lookup, with no prior
list, retries on the newer request. Canceled results never replace completed
claim state. An unavailable or redacted claim list shows as unknown, never as free. See [CLI coordination](cli.md#coordination).

## Workspaces

```yaml
# ~/.config/backlog/config.yaml
workspaces:
  - {name: mine, projects: [~/dev/app, ~/dev/api]}
defaultWorkspace: mine
```

Inside a project, `bl` starts in project scope; `W` toggles to the first
workspace containing it, keeping the selection when visible. `--workspace NAME`
starts in workspace scope. Outside any repository, `defaultWorkspace` opens.

Each project keeps its own sources, statuses, trust, and write and claim
context. Wide workspace lists add Project (`project.name`, or the last part of
the project ID) and Source columns. Relations and readiness use only the loaded
scope; [workspace references](format.md#frontmatter-keys) disambiguate projects.

## Mouse

Click a row to select it; double-click (within 400 ms) to open it. Clicking a
group header folds or unfolds it. Click tabs to switch. Clicking a board column
header focuses it; clicking a list header sorts by ID, priority, status, title,
or update time, and again to reverse. Click a criterion to select it, or its box
to toggle it. Click a relation to select it; double-click to open it. Click the
query in the status bar to edit it there. The wheel moves three rows, or scrolls
detail under the pointer; a sideways swipe moves one board column. Drag the
divider to resize.

In the palette, pickers, and completion, the wheel moves the highlight and a
click picks a row, or toggles it in a multi-select. Clicking the query being
edited moves its cursor. Other prompts ignore the mouse.

## Default keymap

Keys use Bubble Tea names (`down` is ↓, `ctrl+d` is Ctrl-D, `alt+x` is Alt-X).
Spaces separate a sequence (`g g`). Keys typed into a text field or picker stay
there; `ctrl-c` quits.

Emacs aliases: `ctrl+n` down, `ctrl+v`/`alt+v` page, `alt+<`/`alt+>` first or
last row, `ctrl+g` cancel, `alt+x` palette. `ctrl+p` stays go-to. Prompts,
pickers, and menus take `ctrl+g` to cancel and `ctrl+n`/`ctrl+p` to move.
Text fields edit like a shell prompt: `ctrl+a`/`ctrl+e` start and end,
`ctrl+b`/`ctrl+f` and `alt+b`/`alt+f` move by character and word,
`ctrl+d`/`alt+d` delete forward, `ctrl+w`/`alt+backspace` delete a word back,
and `ctrl+u`/`ctrl+k` delete to the start or end. Where a field has a list
(filter completion, the palette, pickers), `ctrl+j`/`ctrl+k` move instead, as in
fzf. Alt keys need a terminal that sends Option/Alt as Meta.

Remap in user config by action ID. An override replaces every default binding;
an empty value unbinds. `ctrl+` takes a letter or arrow; `alt+` takes any
character except a capital letter. Unknown actions, invalid keys, collisions,
and prefix conflicts stop the TUI at startup and name both bindings. To swap
two actions, override both:

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
| `move-down` | `j`, `down`, `ctrl+n` | Move down |
| `move-up` | `k`, `up` | Move up |
| `next-item` | `J` | Next item |
| `previous-item` | `K` | Previous item |
| `first` | `g g`, `home`, `alt+<` | First row |
| `last` | `G`, `end`, `alt+>` | Last row, or row N |
| `screen-top` | `H` | Top of screen |
| `screen-middle` | `M` | Middle of screen |
| `screen-bottom` | `L` | Bottom of screen |
| `half-down` | `ctrl+d`, `pagedown` | Half page down |
| `half-up` | `ctrl+u`, `pageup` | Half page up |
| `page-down` | `ctrl+f`, `ctrl+v` | Page down |
| `page-up` | `ctrl+b`, `alt+v` | Page up |
| `previous-group` | `[` | Previous group |
| `next-group` | `]` | Next group |
| `left` | `h`, `left` | Back / parent / left |
| `right` | `l`, `right` | Open / child / right |
| `go-to` | `ctrl+p` | Go to item |
| `detail` | `enter` | Toggle detail |
| `next-detail-tab` | `tab` | Next tab |
| `previous-detail-tab` | `shift+tab` | Previous tab |
| `back` | `backspace` | Back to previous item |
| `split-left` | `ctrl+left` | Shrink list |
| `split-right` | `ctrl+right` | Grow list |
| `next-view` | `g t` | Next view |
| `previous-view` | `g T` | Previous view |
| `board` | `B` | Board layout |
| `tree` | `T` | Tree layout |
| `query` | `/` | Edit query / find |
| `reset-view` | `F` | Reset view |
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
| `sprint` | `w` | Sprint… |
| `blocker` | `b` | Blockers… |
| `parent` | `P` | Parent… |
| `title` | `t` | Edit title |
| `description` | `e` | Edit description |
| `edit` | `E` | Edit all fields |
| `comment` | `c` | Comment |
| `note` | `i` | Edit notes |
| `criterion` | `x` | Toggle criterion |
| `claim` | `C` | Claim / release |
| `archive` | `d d` | Archive |
| `new` | `n` | New item / next match |
| `new-child` | `N` | New child / previous match |
| `repeat` | `.` | Repeat edit |
| `undo` | `u` | Undo |
| `redo` | `ctrl+r` | Redo |
| `actions` | `!` | Actions… |
| `open-url` | `O` | Open URL |
| `mark` | `space` | Mark |
| `visual` | `V` | Visual range |
| `mark-all` | `ctrl+a` | Mark all |
| `cancel` | `esc`, `ctrl+g` | Cancel / clear |
| `yank-id` | `y y` | Copy ID |
| `yank-title` | `y t` | Copy title |
| `yank-branch` | `y b` | Copy branch |
| `yank-url` | `y u` | Copy URL |
| `yank-markdown` | `y m` | Copy Markdown link |
| `help` | `?` | Help |
| `palette` | `:`, `alt+x` | Commands |
| `quit` | `q` | Quit / back |
| `quit-now` | `ctrl+c` | Quit now |
| `filter-status` | `f s` | Filter: Status |
| `filter-priority` | `f p` | Filter: Priority |
| `filter-assignee` | `f a` | Filter: Assignee |
| `filter-label` | `f l` | Filter: Label |
| `filter-type` | `f t` | Filter: Type |
| `filter-milestone` | `f m` | Filter: Milestone |
| `filter-is:ready` | `f r` | Filter: Toggle ready |
| `filter-is:open,closed` | `f d` | Filter: Toggle done |
| `filter-drafts` | `f b` | Filter: Toggle drafts |
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

## Git commits

With trusted `git.autoCommit: true`, consecutive native edits share one commit
two seconds after the last successful edit. Quitting flushes the batch. Skips and
hook failures show in the status bar, and on stderr after quitting; the edits
remain. See [CLI auto-commit](cli.md#git-auto-commit).

## State

UI state lives in `~/.local/state/backlog/ui/<sha256>.json` (`$XDG_STATE_HOME`),
per project: last view, each tab's selection, the detail tab and whether it's open, and the split width (`ctrl-left`/`ctrl-right`
resize in five-cell steps). Folds survive tab switches but not restarts.

Invalid, unsupported, or oversized state is ignored with a warning. A missing
view falls back to Next; a missing item to the first row. Tab definitions are
config, not state: every launch starts each tab from its declared query,
layout, and columns.

## Rendering

All provider text is stripped of terminal control sequences (design.md D21).
Widths use terminal cells. Colors adapt to the terminal's light or dark
background, mixing neutrals from its own palette. `NO_COLOR` turns styling off;
`--high-contrast` uses bold, underline, and reverse instead of faint text.

Interactive sessions use 20% GC heap growth instead of the CLI's fixed 300 MiB
soft memory target. Explicit `GOGC` and `GOMEMLIMIT` win and are restored on
exit. See [benchmarks](benchmarks.md#tui) for measurements.

## Implementation

Nothing on the Bubble Tea update loop does I/O or work proportional to the
backlog (D14).

- `internal/engine.OpenStore` runs sources in the background. `Current` returns
  an immutable snapshot in O(1); `Subscribe` delivers only the latest.
  `Mutate`, `Undo`, `Redo`, and `Refresh` enqueue bounded requests. `Observe`
  publishes a fresh detail read from its background command. A full queue
  or an overlapping edit to one item returns `busy`, as does an edit against a
  version this session's own pending edit replaced. Only a foreign change is a
  conflict.
- `NewProjector` filters, sorts, groups, and prepares rows on one latest-wins
  worker; superseded requests can't publish.
- Detail rendering, Markdown (Glamour), go-to, completion, pickers, board and
  tree preparation, action discovery, claim polling, config writes, and Git
  commits all run in commands or workers. Navigation and `View` touch only the
  viewport.
- Native sources watch directory entries only: two descriptors per source on
  macOS and Linux. A two-second stat sweep catches in-place edits, which don't
  signal a directory on macOS. `EMFILE`/`ENFILE` fall back to 50 ms stat
  polling. A partial or failed refresh never proves deletion.

To capture a real render without opening a pane:

```sh
task cli:build
scripts/tui-snapshot --width 80 --height 24 -- bin/bl -C /path/to/native-project tui
scripts/tui-snapshot --width 160 --height 48 --keys g t g t g t -- bin/bl -C /path/to/native-project tui
```

The helper runs a private tmux server, waits for the render to settle, prints the
pane, and shuts down its own server.
