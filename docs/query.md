# Query language

**Status:** Frozen.

One syntax serves the TUI filter bar, `bl ls` and `bl next`, saved views, hook
`if:`, action `when:`, and MCP `query`. It reads like GitHub and Linear search:

```text
is:open label:tui,perf -label:wontfix priority:>=high assignee:me updated:<7d "detail pane"
```

| Example | Matches |
| --- | --- |
| `label:tui,perf` | Label `tui` or `perf` |
| `label:tui label:perf` | Both labels |
| `-label:wontfix` | Not labeled `wontfix` |
| `priority:>=high` | Urgent or high |
| `assignee:none` | Unassigned |
| `updated:<7d` | Updated in the last seven days |
| `due:<today` | Overdue |
| `detail pane` | ID or title contains `detail` and `pane` |
| `"detail pane"` | ID or title contains `detail pane` |
| `text:debounce` | Title or body contains `debounce` |
| `field.estimate:3` | Raw frontmatter `estimate` is `3` |
| `sort:priority,-updated group:label` | Sort and group (not a filter) |

## Grammar

```text
query   = [ term *( SP term ) ]             ; terms are ANDed
term    = [ "-" ] ( field ":" values | word | phrase )
values  = value *( "," value )              ; ORed within one field
value   = [ op ] ( bare | quoted )          ; op is <, <=, >, >= for ordered fields
word    = bare                              ; free-text match
phrase  = DQUOTE *( char | escape ) DQUOTE
escape  = BACKSLASH char                    ; includes quotes and backslashes
```

- An empty query matches everything. `-` negates a term.
- Whitespace separates terms outside quotes. Quoted and bare pieces can be
  joined into one value.
- `\` escapes the next character anywhere, including quotes, whitespace, and
  commas. A trailing `\` or unclosed quote is an error, as is an empty value.
- A quoted or escaped field prefix is plain text (`"label:tui"`).
- Commas separate alternatives only in field values. There is no `|` or `&`
  operator: a bare `|`, `||`, `&`, or `&&` term is an error. Quote or escape it
  to search for it.

Field names, `is:`/`has:` values, categories, statuses, priorities, claim
values, and outcomes ignore case. Labels, handles, IDs, and other exact values
don't. `none` (any case) means unset or empty where the table allows it.

## Text matching

Bare words and phrases match a case-insensitive substring of the ID or title.
`text:` matches a substring of the title or body (description, criteria, DoD,
plan, notes, summary, and comment bodies). Neither tokenizes, stems, or treats
wildcards specially.

A SQLite FTS5 trigram index narrows `text:` candidates, then the literal check
runs. Queries under three characters or containing NUL, and missing or stale
indexes, use the in-memory path. Results are identical either way. Remote
adapters may narrow `text:` with provider search; results are rechecked locally,
and if provider search fails or misses a local match, it's skipped for that
snapshot.

## Fields

| Field | Values |
| --- | --- |
| `id` | ID or number: `id:BL-1,BL-2`, `id:12`. A number matches the last hyphenated part, ignoring padding. `source:ID` picks a source; in a workspace use the full source key (`github.com%2Facme%2Fapp/tasks:BL-1`). |
| `status` | Status key or name |
| `category` | `backlog`, `todo`, `active`, `done`, `canceled` |
| `priority` | `urgent`, `high`, `medium`, `low`, `none`; ordered |
| `assignee` | Handle, `me`, or `none` |
| `label`, `type`, `milestone`, `source` | Exact value, or `none` |
| `parent` | ID, or `none` |
| `sprint` | `current` (active), `next` (earliest future), `none`, or a sprint key or name. Linear and Jira always; GitHub only with a Projects v2 source bound to an iteration field. |
| `blocked-by`, `blocks` | ID. `blocks` is derived from the loaded target's blockers; the item's own list counts only for targets that aren't loaded. |
| `created`, `updated`, `due` | Ordered. See [Dates](#dates). |
| `text` | Word or phrase in the title or body |
| `field.NAME` | Exact string form of a raw scalar, or membership in a raw list. `none` matches missing or null. Not ordered. |
| `claimed` | `me`, `others`, `none` (needs coordination) |
| `is` | `open` (backlog, todo, or active), `closed`, `ready`, `blocked`, `waiting`, `unknown`, `mine`, `claimed`, `archived`, `pending` (unconfirmed local edit) |
| `has` | `parent`, `children`, `blockers`, `assignee`, `due`, `criteria`, `claim` |
| `changed` | Field name or `readiness`. Hooks only: the event changed it. |
| `hook`, `action` | Name. Hooks only: matches `hook.finished` or `action.finished` from it. |
| `outcome` | `ok`, `failed`, `timeout`, `refused`. Hooks only, for `*.finished` events. |

Notes:

- `is:ready` and the other readiness values come from the engine
  ([readiness](readiness.md)). Stale items and stale blockers are `unknown`,
  even if the cached status is done.
- `is:mine` matches any of your configured handles.
- `has:blockers` means the item lists blockers, satisfied or not.
  `has:criteria` means acceptance criteria, not DoD.
- Claim terms use the bulk claim list. An expired claim is free. A failed or
  redacted list is unknown, so `claimed:none` never matches then.
- Hook-only terms need hook parsing mode and are false outside an event.
  `sprint:` is refused in hook mode: events carry no sprint catalog.
- `bl ls` and views add `is:open` when the query has no `is:` term.
  `is:archived` loads `archive/`. The parser itself adds nothing and does no I/O.

### Dates

| Value | Meaning |
| --- | --- |
| `due:2026-11-01` | That calendar day, in the clock's time zone |
| `due:<today` | Before today |
| `updated:<7d` | Less than seven days old |
| `updated:>2w` | More than two weeks old |
| `created:>=12h` | At least twelve hours old |

Relative ages use whole `h`, `d`, or `w` as fixed durations. Without an
operator, equality applies. Items with no timestamp, or an invalid or unset due
date, don't match.

## Sort and group

Views take `sort` and `group` parameters; the filter bar also accepts inline
`sort:` and `group:` terms, each at most once and never negated.

| Sort key | Order |
| --- | --- |
| `next` | Engine ranking ([readiness](readiness.md)); the default |
| `priority` | Urgent first |
| `rank` | Ascending, unset last |
| `updated` | Newest first |
| `created` | Oldest first |
| `due` | Earliest first, unset last |
| `id` | Natural |
| `title` | Case-insensitive |
| `status` | Workflow order |

Separate keys with commas; `-` reverses one key (`sort:priority,-updated`). Ties
break by `next`, then ID, then source. Items without a `next` rank follow ranked
ones.

`group:` takes one of `status`, `category`, `priority`, `assignee`, `label`,
`milestone`, `sprint`, `type`, `parent`, `source`, or `none` (the default). Items with
several labels or assignees appear in each group. Groups follow status order,
category order, priority order (unset last), or milestone catalog order
(including archived milestones); undeclared values follow, then other keys sort
by value. An empty group value is unset. Sort order holds within each group.
Sprint groups run current, next, other future sprints by start, closed sprints
(most recent first), then none.

## Errors

Errors give a zero-based UTF-8 byte position and a suggestion when one is close:

```text
x      lable:tui
unknown field "lable" at 7; did you mean "label"?
```

The TUI keeps the last valid result while you type. Ordered operators work only
on priority and dates. Unknown statuses, labels, types, milestones, sources, and
assignees are errors in the CLI and warnings in the TUI, as is `sprint:` when no
sprint-capable source is configured ("no source provides sprints"). The first
sprint term in a TUI session loads the sprint catalog; until it arrives, unknown
sprint names aren't warned about, and the results update when it lands. Unknown
IDs, raw fields, and text are fine; they just may not match.

## Completion

The TUI completes field names (including `field.NAME:` for raw keys in use,
lowercased), then values from config and the loaded items: statuses (with
names), labels, assignees, milestones (with names), IDs, and titles. Declared
labels and unarchived milestones come first, then values in use. An archived
milestone is offered only if an item uses it. Hook-only fields appear only in
hook mode. Candidates are values; the caller quotes them as needed.

## Shared quick-add parser

`internal/query.QuickAdd` parses [quick-add](tui.md#quick-add) text without
defaults, ID allocation, or I/O. It's shared by the TUI, `bl new`, and MCP.

- Quoting and escaping work as in queries. A token starting with a quote or
  escape is title text; quotes inside a token allow spaces (`#"two words"`).
- Title words join with single spaces. A title is required.
- Priority prefixes ignore case and must be unique (`!h`).
- `@me` needs a caller handle. Other handles keep their `@`.
- List tokens collect without duplicates; a repeated scalar token keeps the
  last value. `^` and `<` accept `source:ID`.
- Empty values, invalid priorities, and unfinished quotes or escapes fail with
  a position. The caller validates field values and references.
