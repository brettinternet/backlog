# Query language

**Status:** Draft. BL-6 freezes it.

One syntax serves the TUI filter bar, CLI arguments (`bl ls QUERY`, `bl next QUERY`), saved views, hooks (`if:`), actions (`when:`), and MCP `query`. It reads like GitHub and Linear search.

```text
is:open label:tui,perf -label:wontfix priority:>=high assignee:me updated:<7d "detail pane"
```

## Grammar

```text
query   = term *( SP term )                 ; terms are ANDed
term    = [ "-" ] ( field ":" values | word | phrase )
values  = value *( "," value )              ; ORed within one field
value   = [ op ] ( bare | quoted )          ; op is <, <=, >, >= for ordered fields
word    = bare                              ; free-text match
phrase  = DQUOTE *( char / "\\" DQUOTE ) DQUOTE
```

A leading `-` negates the term. Field names, `is:`/`has:` values, statuses, and priorities are case-insensitive. Free text is matched case-insensitively against the ID and title by default, and against the whole body with `text:`.

## Fields

| Field | Values |
| --- | --- |
| `id` | ID or numeric shorthand; `id:BL-1,BL-2` |
| `status` | Status key or name |
| `category` | `backlog`, `todo`, `active`, `done`, `canceled` |
| `priority` | `urgent`, `high`, `medium`, `low`, `none`; ordered, so `priority:>=high` works |
| `assignee` | Handle, `me`, or `none` |
| `label`, `type`, `milestone`, `source` | Exact value, or `none` |
| `parent` | ID, or `none` |
| `blocked-by`, `blocks` | ID |
| `created`, `updated`, `due` | `YYYY-MM-DD` or a relative age (`7d`, `12h`, `2w`), ordered; `due:<today` |
| `text` | Word or phrase matched against the title and body (FTS when cached) |
| `field.NAME` | Raw frontmatter or provider field (format.md) |
| `claimed` | `me`, `others`, or `none` (requires Worklease) |
| `is` | `open` (backlog, todo, or active), `closed`, `ready`, `blocked`, `waiting`, `unknown`, `mine`, `claimed`, `archived`, `pending` (unsynced local edit) |
| `has` | `parent`, `children`, `blockers`, `assignee`, `due`, `criteria`, `claim` |
| `changed` | Field name, or `readiness`. Hook `if:` only. True when the event changed that field. |
| `hook`, `action` | Name. Hook `if:` only. Matches the hook or action that produced a `hook.finished` or `action.finished` event. |
| `outcome` | `ok`, `failed`, `timeout`, `refused`. Hook `if:` only, for `*.finished` events. |

`is:ready` and related readiness values use design.md §6. With no `is:` term, a view or `bl ls` adds `is:open`. `is:archived` loads `archive/` on demand.

## Sort and group

Sort and group are separate view parameters. The filter bar also accepts them inline as `sort:` and `group:` terms:

| Parameter | Syntax | Notes |
| --- | --- | --- |
| `sort` | `field[,field…]` | Prefix `-` to reverse. Keys: `next` (design.md §6 order), `priority`, `rank`, `updated`, `created`, `due`, `id`, `title`, `status`. Ties always break by `next`, then `id`. |
| `group` | One key | `status`, `category`, `priority`, `assignee`, `label`, `milestone`, `type`, `parent`, `source`, `none`. An item with several labels appears in each label group. Groups follow workflow order for status and category, catalog order for declared milestones (design.md §5), and value order otherwise. |

## Errors

A malformed query returns a positioned error, for example `unknown field "lable" at 7; did you mean "label"?`. The TUI keeps the last valid result while you type. An unknown value for a known field, such as a status that doesn't exist, is an error in the CLI and an inline warning in the TUI.

## Completion

The TUI completes field names first, then values from the loaded snapshot and config: statuses, labels, assignees, milestones (with their names), IDs, and titles. Declared, unarchived labels and milestones come first. Free-text matches highlight in the list.
