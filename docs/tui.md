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
D35): GitHub's `open`, `closed`, and mapped labels or project
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
progress, linked change, assignee, and time since update. The change column
shows the furthest stage of the item's most advanced change, such as `⇡#45 ✓`
([Changes](changes.md)), and appears once any row has one. As the pane narrows,
columns drop in this order so the title keeps at least two-fifths of the width:
updated, assignee, children and change, blockers, claim, then the status name
(the icon stays).
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
longer has turns stale. With detail focused, `R` rereads just that item,
bypassing the detail cache. Cached rows appear in Open, but a stale item is
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
undeclared statuses. Keys a status `includes` share its column, and cards keep
their own status name (design.md D36). The Board tab hides drafts, which are never ready
(design.md D29); `fb` shows them. Empty columns stay unless the query's status,
category, or `is:open`/`is:closed` terms exclude that status; other terms filter
cards, not columns. Other group keys use their group order. A card shows priority, ID, open blockers, child progress, and
assignee above its title; as it narrows, assignee drops first, then children,
then blockers. A column without cards is only as wide as its header; columns
with cards share the rest, each at least 28 cells, so how many fit depends on
which are in view. Columns stay in place and order; one changes width only when
it gains its first card or loses its last. Columns scroll horizontally and keep
their own cursor. Column labels use the default foreground (the focused one
bold), and a faint rule fills the rest of each header, so the row stays distinct
from the muted tabs above without a spacer row, under `NO_COLOR` and high
contrast too. `<`/`>`
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
`q` close (`enter` follows a relation in Relations, opens a change view from
Changes, and in History opens a commit's diff or expands another entry). Edit and copy keys work in
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
is. With an item selected, its title names the item, and the item's actions
(see [Actions](#actions)) come first. `?` opens the same list as read-only
help. Both are searchable by label, action ID, key, and common verbs
(`close` finds Done, `delete` Archive, `rename` Edit title). Go-to, the palette,
and pickers match each query word separately, in any order; whole words rank
above partial and fuzzy matches.

Outside them, a key is shown only where it applies and nothing else suggests
it, in faint text: `m more` and `⌫ back` in the detail header, and `! resume` on
a selected session row in History.

## Detail

The header (ID, status, priority, claim, title) and tab counts follow the
selection immediately. A pane too narrow for every tab drops the counts, then
scrolls the tabs to keep the active one visible, with `‹` and `›` marking the
rest. The claim shows its holder (`mine` or `agent@host`) and
remaining lease, such as `mine 12m` or `agent@host <1m`, or `expired`, from the
polled claim list. The body loads once the selection rests for 50 ms; switching tabs
or `m` renders at once. Another item's or tab's body stays until the new one is
ready, or loading replaces it. A reload of the page in view (resize, `m`, `R`, a
new version) keeps its body and shows loading on the header row.

| Tab | Content |
| --- | --- |
| Overview | Set fields (milestone names beside keys), readiness and why, description |
| Criteria | Acceptance criteria and definition of done, with progress |
| Notes | Plan, notes, summary |
| Comments | Comments |
| Relations | Blockers → item → what it blocks, then parent, children with progress, duplicate of, related, each with live status. Blocking, children, and related include inbound links; related omits items linked by blocks. |
| Changes | Linked worktrees, branches, pull requests, and landed commits, furthest first ([Changes](changes.md)) |
| History | Events, claims, sessions, comments, linked changes, commits to the item file, and provider activity (`github:`, `linear:`, `jira:`), oldest first (design.md D40) |

In Criteria and Relations, `j`/`k` move a cursor over the entries. In
Relations, `enter` opens the entry's item in detail, as go-to does: the view and
list selection stay. `backspace` leaves a change view, else returns to the
previous item, back through every jump to the original selection, then closes
detail; the detail header shows `⌫ back` while there is a previous item. A target that isn't loaded shows `?`,
and following it says why: its source isn't configured, failed, or is still
loading, or the item isn't in it.

Changes lists one entry per change, with `j`/`k` moving over them. Its first
line reads stage glyph (`◇` worktree, `⎇` branch, `⇡` pull request, `◆`
landed, `?` unknown or stale), `#45` or the branch, title, state, checks (`✓`
passing, `✕` failing, `●` pending), review decision, `+120 −30`, and age. A
branch or worktree counts `+/−` from its merge base with the default branch,
including a worktree's uncommitted files. A second line names the branch, the
worktree relative to the checkout root (or under `~`), how far an open branch
is ahead of (`↑2`) and behind (`↓1`) the default branch, and dirty files; a
third says why a change is unknown or stale. A pull request's review decision
is followed by its unresolved review threads, such as `2 unresolved`. Git
counts these on a worker each time the tab renders. `!` on an entry lists the actions that name a
`{change.*}` placeholder ([Automation](automation.md#placeholders-and-environment)), run on
that change; `O` opens its URL. `R` reloads changes along with sources.

`enter` on an entry opens its change view in place of the item's tabs, with
Overview, Files, Diff, Commits, and Conversation tabs; `backspace` returns to
the entry. Overview lists the change's state, branch, worktree, base, head,
checks, review, file and commit counts with `+/−`, and uncommitted files. For a
pull request it also lists each check run and commit status as an entry (`O`
opens the one under the cursor), each requested or submitted reviewer with
their state, the unresolved thread count beside the review, and the
description. Conversation lists review threads (file, line, resolved or not)
and comments, unresolved threads first, each with its comments' authors, ages,
and text; `O` opens the one under the cursor, and `m` shows more when the text
is cut off. On both, `j` and `k` scroll until the next entry shows, through a
long entry or the description past the last check. Files lists one entry per
changed file: status (`A` added, `M` modified, `D` deleted, `R` renamed,
`B` binary, `U` uncommitted), path, and `+/−`. Commits lists the change's
commits newest first, one line each, and `ys` copies the one under the cursor.
`enter` on a commit opens that commit's Diff in a change view one level under
the change; `backspace` returns to Commits with the cursor on it. A local
branch or worktree is read from Git against its merge base, a landed change
from its landed commits, and a pull request without a local head from the
forge. Git and forge reads run on the detail worker for the change shown, and
a worktree's uncommitted files are skipped, saying so, when repository config
defines a filter driver. `!`, `O`, and `yp` act on the change in view.

Diff shows each file's header line, then its hunks: unified (old and new line
numbers, sign, code) when the pane is under 120 columns, side by side at 120
or wider. Code is highlighted by file type, changed lines are tinted green and
red, and the changed words of a paired line are marked; high contrast marks
them in reverse and no color leaves them plain. A cursor marks one line: `j`
and `k` move it, scrolling keeps it on screen, and `]`/`[` move it to the next
or previous file, `]c`/`[c` to the next or previous hunk, `]t`/`[t` to the next
or previous step of the change's tour, and `/` to each match.
Each line is a file and line number on the old or new side at the change's
head: a context line on both, a split row on each half, and a file or hunk
header names only its file. The cursor stays on its line when a file expands or
the layout changes.
A file's diff over 32 KiB shows its size until `m` draws it; patches are cut
at 128 KiB, saying so. A branch or worktree diffs against its merge base with
the default branch, plus a worktree's uncommitted changes; a landed change
diffs its merges against their first parents; a pull request without a local
head shows the forge's per-file patches and says which files the forge left
out. Git runs on the detail worker with external diff and textconv drivers
off, and each file renders there, cached by head, file, width, and theme.
A tour stored with [`bl tour`](cli.md#bl-tour-id-file) puts each step's number,
title, cited criterion, and Markdown narrative above its line, or its range's
first line, on the step's side when split, and marks each line it covers with
`▎` after that side's line number. A step's lines outside every hunk come in
with three lines of context; lines the file lacks are noted under the file's
header. `]t`/`[t` put the cursor on a step's first line and keep as much of
its range in view as fits. A tour written for an earlier head is carried
forward through the diff from its commit to the head, as a forge carries
review comments: a banner names both commits and how many steps are outdated,
each step whose lines are unchanged shows at the lines they moved to, a
renamed file's at its new path, and a step whose lines were edited or deleted,
or had lines added inside its range, is outdated, showing its lines as of the
tour's commit under its file's header, where `]t`/`[t` put the cursor. The
mapping reads Git objects only, on the detail worker, cached by the tour's
commit and the head. A tour whose commit is no longer in the head's history,
as after a rebase, or whose step is on a file with uncommitted changes, shows
only that it is stale.

A pull request's change view drafts a pending review. `c` opens `$EDITOR` on
the comment for the Diff line under the cursor, on a reply to the
Conversation thread under it, on the Overview draft entry under it, and
otherwise on the review's comment; saving a blank buffer removes that part.
A line of an uncommitted file can't be commented on, since it may be another
line at the head.
Drafts are kept per pull request under the state directory's `reviews/`, so
they survive restarts, and Overview lists them under Draft. `r` opens the
submit prompt: `c` comment, `a` approve, `r` request changes, `d` discard. The
forge rereads the pull request first and refuses while a line comment was
drafted on another head (marked `stale`, never posted onto other lines) or a
reply's thread is gone; it then sends the review and the replies in one call
that is never retried. Parts the forge refused stay in the draft with its
reasons. When the call's outcome is unknown, the draft says so and `r` offers
only discard: check the pull request before drafting again. Without a forge
that posts reviews or an account that may write ([GitHub](github.md)), `c`
and `r` are disabled with the reason.
Review actions on a single commit's Diff are disabled with the reason, since
drafts anchor to the pull request's head.
`enter` on a commit in History opens that commit's Diff in the change view;
`backspace` returns to the commit's entry. A single commit's Diff starts with
its SHA, author, age, and full message, subject and body wrapped to the pane
as `git show` does, above the first file; branch and pull request Diffs don't.
The message isn't an entry: `j`/`k`, file and hunk jumps, and line anchors
pass over it. Git reads the body for that one commit; a commit Git lacks, such
as one from a pull request without a local head, shows the forge's message
and why its files don't show.

History opens scrolled to the newest entry, with `j`/`k` moving over entries.
Each row reads time, source, summary, then member count and actor. The time
follows `ls -l`: `Oct 26 17:20` within six months, `Oct 26  2025` before. The
source label is `item` (edits and readiness), `claim`, `agent` (a session),
`handoff`, `comment`, `change`, `git`, `action`, `hook`, or the remote provider
(`github`, `linear`, `jira`); agents, commits, comments, and remote activity
are colored, and the column is dropped below 48 cells. An edit's summary names
each field: `status todo → in-progress`, `labels +windows -human`,
`criteria 2/5 → 3/5`, or `plan changed` for long text. Each session is one row
(agent, phases, duration, state unless it is the last phase, `resumable` or
the check that refuses it, such as `checkout removed`, the first line of its
handoff, and its live [contact](cli.md#contact) channels)
with its entries folded under it. Each linked change is one row (label, title,
state, checks, review) spanning its first event to its merge, close, or last
landed commit, with its worktree, branch, opened, merged, and closed events and
landed commits folded under it. `▸` marks a row that expands: `enter` opens a
group (or a commit's diff), a cut-off summary wrapped in full, and the entry's detail, wrapped with
hanging indents. An edit's detail lists what the summary abbreviates, per field:
added and removed lines (`+`, `-`, green and red, `…` for skipped unchanged
lines), and checked or unchecked criteria (`[x]`, `[ ]`). A session's detail
aligns its fields. `!` on a session row narrows the actions menu's Sessions to
`Resume` and `Fork` for that session, each disabled with its reason when
refused; a resumable session row shows `! resume` at its right end while
selected, and nowhere else. A provider that isn't `ok` shows as one
dim line at the top, such as `claims: unavailable (coordination not
configured)`. History shows the newest 200 entries; `m` raises it to 2,000.
It loads on a worker when the tab opens and is cached by item version and
event-log position. Provider activity is fetched only then, never during sync.

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
Markdown link, `yp` the URL of the change under the Changes cursor (else the
item's most advanced change), and `ys` the SHA of the commit under the History
cursor or the head of the change under the Changes cursor. The branch name is the lowercase ID plus an ASCII title slug, at
most 48 characters, cut at a word: `bl-12-debounce-detail-rendering`.

`O` opens the item URL, or the change's on a Changes entry. Only absolute
HTTP(S) URLs without credentials open,
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

`+` on a pull request item (`github-pulls`) adopts it: quick-add opens with the
pull request's title, escaped so it stays the title, and its URL in `refs`. The
source and status are the ones `n` would pick, never the pull request source, and
tokens still apply. Saving is one create, so the pull request item disappears
once the new item is in the snapshot ([GitHub](github.md#adopting)).

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

`!`, or a right-click on a row, opens the item's menu, titled with its ID. It
lists configured actions whose `when` query matches the item, then its agent
sessions, then the built-in actions likely to apply, each with its key: Status…,
Start (not yet started) or Done (active), Priority…, Assign me (not yours) or
Assignees…, Labels…, Edit title, Comment, Claim / release, Copy ID, Copy branch,
Open URL, and Archive. Built-in actions that are disabled are left out; the
palette has every action and says why one is disabled. The palette and help
include configured actions too, with disabled reasons. The item's actions and
sessions load on a worker. A menu reopened on the same item shows the ones it
last loaded at once and refreshes them in place. Otherwise the menu waits up to
150 ms for them before drawing, so they don't push its rows down under the
cursor. `confirm` actions ask `y`/`n`.
Attached actions suspend the TUI like `$EDITOR`; detached ones return at once.
Bind one with `key:` in its declaration or `action:NAME` in user `keys`. See
[Automation](automation.md). An action that names a `{change.*}` placeholder
runs on a linked change, so it is listed from a Changes entry and disabled
elsewhere with `runs on a linked change`.

Below them, `Sessions` lists the item's recorded agent sessions, newest first,
as `Resume claude · 2h` or `Fork claude · 2h` for a closed item. A refused
session shows its reason. Enter runs it like an action; a failure reports its
exit status and stderr tail, and the session stays listed. See
[Agent sessions](automation.md#agent-sessions).

## Coordination

With coordination enabled, wide lists add a Claim column (`mine`, the holder's
`agent@host`, or `expired`), also available as a view column. `C` claims or
releases the selected item. `S` and other active transitions claim first; closing
releases your claim. A bulk edit can't activate marked items; start each one. Without
`--session`, `BL_SESSION`, or `WORKLEASE_SESSION_ID`, each item has its own
session, `tui:` plus a hash of its claim, so you can hold several claims and reopening the TUI
anywhere resumes them (design.md D39). An explicit session holds one claim.
While the TUI runs, it renews your claims at half the first remaining lease; a
failed renewal shows a notice and retries until the lease lapses. After
quitting, your claims lapse within one lease. Claims refresh
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
the project ID) and Source columns. Each item's claim, its lease, and renewal
come from its own project's coordination, as in [Coordination](#coordination).
Relations and readiness use only the loaded scope; [workspace references](format.md#frontmatter-keys) disambiguate projects.

## Mouse

Click a row to select it; double-click (within 400 ms) to open it. Clicking a
group header folds or unfolds it. Click tabs to switch, or `‹`/`›`
to show the hidden tab beside them. In detail, click `⌫ back` or
`m more`/`m less` to run them. Right-click a row, or detail, to open the item's
`!` menu. Clicking a board column
header focuses it, and its edge `‹`/`›` focus the hidden column beside them; clicking a list header sorts by ID, priority, status, title,
or update time, and again to reverse. Click a criterion to select it, or its box
to toggle it. Click a relation to select it; double-click to open it. Click a
change to select it. Click a
History entry, or any line of its expansion, to select it; click its `▸` or
double-click to expand or collapse it, and click `! resume` to run it. Click the
query in the status bar to edit it there. The wheel moves three rows, or scrolls
detail under the pointer; a sideways swipe moves one board column. Drag the
divider to resize. Drag across detail text to select it, moving past an edge to scroll;
release copies it (OSC 52).

In the palette, pickers, and completion, the wheel moves the highlight and a
click picks a row, or toggles it in a multi-select. Clicking the query being
edited moves its cursor. A click outside the palette or `!` menu closes it; a
right-click there opens the menu for the row under it. Other prompts ignore the
mouse.

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
| `back` | `backspace` | Back |
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
| `refresh` | `R` | Refresh |
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
| `review` | `r` | Submit review… |
| `note` | `i` | Edit notes |
| `criterion` | `x` | Toggle criterion |
| `claim` | `C` | Claim / release |
| `archive` | `d d` | Archive |
| `new` | `n` | New item / next match |
| `new-child` | `N` | New child / previous match |
| `adopt` | `+` | Adopt pull request |
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
| `yank-change` | `y p` | Copy change URL |
| `yank-commit` | `y s` | Copy commit SHA |
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
- On Windows, one overlapped `ReadDirectoryChangesW` handle per source reports
  file name, size, and last-write changes, which include in-place edits, so
  there's no per-item sweep. A notification buffer overflow still signals, and
  the refresh rescans the directory. If the watch fails to open or fails later,
  or a two-second check finds the directory moved or replaced, the source falls
  back to 50 ms stat polling.

To capture a real render without opening a pane:

```sh
task cli:build
scripts/tui-snapshot --width 80 --height 24 -- bin/bl -C /path/to/native-project tui
scripts/tui-snapshot --width 160 --height 48 --keys g t g t g t -- bin/bl -C /path/to/native-project tui
```

The helper runs a private tmux server, waits for the render to settle, prints the
pane, and shuts down its own server.
