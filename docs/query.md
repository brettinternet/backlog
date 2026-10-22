# Query language

**Status:** Frozen.

One syntax serves the TUI filter bar, CLI arguments (`bl ls QUERY`, `bl next QUERY`), saved views, hooks (`if:`), actions (`when:`), and MCP `query`. It reads like GitHub and Linear search.

```text
is:open label:tui,perf -label:wontfix priority:>=high assignee:me updated:<7d "detail pane"
```

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

A leading `-` negates the term. Empty queries match all items. Whitespace separates terms outside quotes; quoted and bare segments can be concatenated. A backslash escapes the next character, inside or outside quotes (including quotes, backslashes, whitespace, and commas). A trailing backslash or unterminated quote is an error. Empty values are errors. A quoted or escaped field prefix is literal free text. Commas delimit alternatives only in field values, not free text.

Field names, `is:`/`has:` values, categories, statuses, priorities, claim values, and outcomes are case-insensitive. Other exact values (including labels, handles, and IDs) are case-sensitive. `none` is a case-insensitive reserved value for an unset scalar or empty list where listed below. Raw field suffixes match frontmatter keys case-insensitively, like other field names.

Free text is a case-insensitive **literal substring** of the ID or title. `text:` is a case-insensitive literal substring of the title or body: description, criteria, DoD, plan, notes, summary, and comment bodies, joined with newlines. Neither mode tokenizes, stems, interprets wildcards, nor uses a search-engine query grammar. A cached FTS implementation must preserve these exact results.

## Fields

| Field | Values |
| --- | --- |
| `id` | Exact ID or numeric shorthand; `id:BL-1,BL-2`. Numeric shorthand matches the last hyphen-separated component, ignoring zero padding. `source:ID` disambiguates a ref. |
| `status` | Status key or name |
| `category` | `backlog`, `todo`, `active`, `done`, `canceled` |
| `priority` | `urgent`, `high`, `medium`, `low`, `none`; ordered, so `priority:>=high` works |
| `assignee` | Handle, `me`, or `none` |
| `label`, `type`, `milestone`, `source` | Exact value, or `none` |
| `parent` | ID, or `none` |
| `blocked-by`, `blocks` | ID |
| `created`, `updated`, `due` | `YYYY-MM-DD`, `today`, or nonnegative integer relative age (`7d`, `12h`, `2w`), ordered; `due:<today`. Absolute dates compare calendar dates in the injected clock's location. Relative ages compare elapsed age: `updated:<7d` means newer than seven days ago; `updated:>7d` means older. Hours/days/weeks are fixed durations. Without an operator, equality is used. Missing timestamps and invalid/unset item due dates do not match. |
| `text` | Literal word or phrase matched against the title and body (equivalent results when cached) |
| `field.NAME` | Exact string representation of a raw frontmatter/provider scalar, or membership in a raw list (format.md). `none` matches a missing or null field. No ordered operators. |
| `claimed` | `me`, `others`, or `none` (requires coordination) |
| `is` | `open` (backlog, todo, or active), `closed`, `ready`, `blocked`, `waiting`, `unknown`, `mine`, `claimed`, `archived`, `pending` (unsynced local edit) |
| `has` | `parent`, `children`, `blockers`, `assignee`, `due`, `criteria`, `claim` |
| `changed` | Field name, or `readiness`. Hook `if:` only. True when the event changed that field. |
| `hook`, `action` | Name. Hook `if:` only. Matches the hook or action that produced a `hook.finished` or `action.finished` event. |
| `outcome` | `ok`, `failed`, `timeout`, `refused`. Hook `if:` only, for `*.finished` events. |

`is:ready` and related readiness values use the engine's design.md §6 assessment, not a second readiness calculation. `is:mine` matches any configured caller handle against assignees. Claims, archive state, pending state, and readiness are injected snapshot overlays. Missing readiness is unknown; missing claim state is not known-free. Claim predicates require coordination to be enabled. `has:blockers` means the item has dependency references, regardless of their readiness; `has:criteria` refers to acceptance criteria, not DoD.

Event predicates require explicit hook parsing mode. With no event context they are false (before negation). `hook:` and `action:` only match their corresponding finished event types; `outcome:` only matches a hook/action finished event. `changed:` matches changed field names case-insensitively, including `readiness`.

With no `is:` term, a view or `bl ls` adds `is:open`. `is:archived` loads `archive/` on demand. These are caller responsibilities: the pure parser adds no terms and performs no I/O.

## Sort and group

Sort and group are separate view parameters. The filter bar also accepts them inline as `sort:` and `group:` terms:

| Parameter | Syntax | Notes |
| --- | --- | --- |
| `sort` | `field[,field…]` | Prefix `-` to reverse. Keys: `next` (injected complete engine design.md §6 order), `priority` (urgent first), `rank` (ascending, unset last), `updated` (newest first), `created` (oldest first), `due` (earliest first, unset last), `id` (natural numeric order), `title` (case-insensitive ascending), `status` (workflow order). Ties always break by `next`, then natural `id`, then source identity. Reversal applies to the named key only. Missing next ranks follow known ranks, then break by ID. |
| `group` | One key | `status`, `category`, `priority`, `assignee`, `label`, `milestone`, `type`, `parent`, `source`, `none`. An item with several labels or assignees appears in each corresponding group. Groups follow configured status order, category order (`backlog`, `todo`, `active`, `done`, `canceled`), priority order (`urgent`, `high`, `medium`, `low`, then unset), catalog order for declared milestones including archived ones (design.md §5), and case-sensitive value order otherwise. Undeclared statuses/milestones follow declared ones in value order. Parent group keys are source-qualified. An empty group value represents an unset field. Input sort order is preserved within groups. |

`sort:` and `group:` are each allowed at most once and cannot be negated. `group:` takes exactly one key. They are projection parameters, not match predicates; omitted sort uses `next`, and omitted group is `none`.

## Errors

A malformed query returns a positioned error with a close-name suggestion when available, for example parsing `x      lable:tui` returns `unknown field "lable" at 7; did you mean "label"?`. Positions are zero-based UTF-8 byte offsets into the original input; terms and values retain their positions in the AST. The TUI keeps the last valid result while you type.

An unknown value for a known field, such as a status that doesn't exist, is an error in the CLI and an inline warning in the TUI. Syntax validates fixed vocabularies immediately; separate snapshot validation checks status keys/names, labels, types, milestones, sources, and assignees against config and values in use. Unknown IDs/raw fields/free text are legal and simply may not match. Ordered operators are allowed only on priority and dates.

## Completion

The TUI completes field names first (including `field.NAME:` for raw keys in use, lowercased), then values from the loaded snapshot and config: statuses, labels, assignees, milestones (with their names), IDs, and titles. Declared labels and unarchived milestones come first, then deduplicated values in use in case-sensitive value order. Labels are plain strings in the current config and have no archive flag. Milestone candidates carry their display names; an archived catalog entry is not offered solely because it is declared (but remains discoverable if in use). Status candidates carry names too. Candidates are semantic values, not insertion text; the caller quotes/escapes punctuation as needed. Hook-only field names are offered only in hook mode. Free-text matches highlight in the list (rendering belongs to the TUI).

## Shared quick-add parser

`internal/query.QuickAdd` implements [tui.md quick-add](tui.md#quick-add) without defaults, ID allocation, or I/O. Quotes and escapes use the same decoding as query values. A token whose first character is quoted or escaped becomes literal title text; quotes inside a metadata token allow spaces in its value (`#"two words"`). Title words are joined with single spaces. A nonempty title is required.

Priority prefixes are case-insensitive and must uniquely identify urgent/high/medium/low. `@me` resolves to the caller-supplied handle and errors if none was supplied. Other handles retain `@`. List tokens accumulate without duplicates; repeated scalar tokens use the last value. Parent and blocker tokens accept `source:ID`. Empty metadata values, invalid priorities, unfinished quotes, and unfinished escapes produce positioned errors. The caller validates source-specific field values and reference identities.
