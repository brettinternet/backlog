# Native format v1

**Status:** Frozen v1. Any incompatible change needs a new `version` in `backlog.yaml` and a migration in `bl check --fix`.

The native format is plain Markdown with YAML frontmatter, and it is designed for Git:

- One file per item, named by ID.
- Minimal diffs on edit.
- Nothing in a filename or directory changes when a field changes.
- The files read well without `bl`.

## Layout

```text
backlog.yaml             shared project config (design.md §5)
backlog.local.yaml       optional private overrides; either config marks a bl project
backlog/                 source `path` (default `backlog`)
  BL-1.md
  BL-2.md
  archive/               archived items; skipped by default loads
    BL-0.md
```

- The filename is exactly `<id>.md`. A file whose name doesn't match its `id` is a `bl check` error.
- Every other file or directory under `backlog/` is ignored, except `archive/`. Names starting with `.` are reserved for `bl`.
- Archiving moves the file to `archive/` and changes nothing else. Unarchiving moves it back. No other operation moves a file.

## Item file

```markdown
---
id: BL-12
title: Debounce detail rendering while the selection moves
status: todo
type: feature
priority: high
assignees: ["@brett"]
labels: [tui, perf]
milestone: m-2
parent: BL-3
blocked_by: [BL-10, BL-11]
related: [BL-40]
rank: a3
due: 2026-11-01
refs: [docs/tui.md, "https://github.com/charmbracelet/bubbletea/issues/1"]
files: [internal/tui/detail/**]
created: 2026-10-21T17:06:00Z
updated: 2026-10-22T09:15:42Z
---

Holding `j` re-renders Markdown for every row passed. Render the detail only
after the selection is stable for one frame, and reuse cached renders.

## Acceptance criteria

- [ ] `go test ./internal/tui -run TestDetailDebounce` passes
- [x] Held-key benchmark p95 ≤ 16 ms at 10k
  Evidence: `BL_TUI_SAMPLES=20 go test ./internal/tui -run TestHeldKey` p95 9.8 ms.

## Definition of done

- [ ] `task ci` passes on the final commit

## Plan

1. Add a stable-selection timer message.

## Notes

The 40 Hz held-key run needed a warm cache to reach budget.

## Summary

Detail rendering now waits for a stable selection.

## Comments

### 2026-10-22T09:10:00Z @brett

Keep the header fields synchronous; only the body waits.
```

### Encoding

- UTF-8.
- Reading accepts LF and CRLF. Editing preserves the line ending the file already uses. New files use LF and end with a newline.
- The file starts with a `---` line, then a block-style YAML mapping (one key per line), then a closing `---` line, then the body.
- Frontmatter uses LF or CRLF line terminators, not bare CR or Unicode line separators. Frontmatter keys are literal strings, not merge keys. Duplicate keys and aliases for known fields are rejected. Unknown values may use nested YAML; existing documents always retain their original representation.
- The codec rejects malformed frontmatter, missing required keys, invalid known-field types, IDs, dates, timestamps, priorities, and duplicate sections. Status membership, filename checks, graph validation, and filesystem writes belong to the source/check layer, not the codec.

### Frontmatter keys

| Key | Type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | ID | yes | Equals the filename stem |
| `title` | string | yes | One line, non-empty |
| `status` | string | yes | A status `key` from project config. The config maps it to a display name and a category. |
| `type` | string | no | Free text. Config may list suggestions. |
| `priority` | enum | no | `urgent`, `high`, `medium`, or `low`. Absent means unset. |
| `assignees` | list of strings | no | `@handle` or email. `me` resolves through user config `me`. |
| `labels` | list of strings | no | Free text. Config may list suggestions. |
| `milestone` | string | no | A milestone key. Config may give it a name (design.md §5). |
| `parent` | ID | no | Hierarchy. Cycles are errors. |
| `blocked_by` | list of IDs | no | Hard prerequisites (design.md §6). Cycles are errors. |
| `related` | list of IDs | no | Informational |
| `duplicate_of` | ID | no | Informational |
| `rank` | string | no | Manual order key, compared bytewise. `bl` generates fractional keys, so moving one item rewrites one line. |
| `due` | date | no | `YYYY-MM-DD` |
| `refs` | list of strings | no | URLs or repository-relative paths |
| `files` | list of strings | no | Declared file scope: repository-relative paths or globs |
| `created` | timestamp | yes | RFC 3339 UTC to the second. Set on create. |
| `updated` | timestamp | yes | RFC 3339 UTC to the second. Set on every write. |

Other keys are kept byte-for-byte, shown raw in detail, and queryable as `field.NAME:VALUE` (query.md).

References are local by default, and a local reference must be a native ID. `source:ID` names another source in the same project, using that source's own ID: `api:API-1`, `issues:128`, `jira:10001`. In a [workspace](design.md#5-projects-configuration-and-trust), `source:ID` resolves only when one loaded item matches; to name another project, qualify the source with its escaped project ID:

```yaml
blocked_by: [BL-3, docs:DOC-1, "github.com%2Facme%2Fapp/tasks:BL-1"]
```

Missing, unloaded, or ambiguous targets are `unknown`. Opening a workspace never rewrites files.

### IDs

- Grammar: `^[A-Z][A-Z0-9]*-[0-9]+(\.[0-9]+)*$`.
- Comparison is natural: prefix, then each numeric segment as an integer. `BL-9 < BL-10`, and `BL-007` equals `BL-7` for lookup.
- New IDs are `PREFIX-N`, never padded or dotted (design.md D6). Import keeps IDs exactly, including padding and dots.
- The allocator locks `<git-common-dir>/backlog/lock` (flock) and keeps a counter in `<git-common-dir>/backlog/next/<source-id>`. The next number is the larger of the counter and one more than the highest existing number, active or archived (for imported dotted IDs, the first number counts). Outside Git, both live in `$XDG_STATE_HOME/backlog/projects/<SHA-256 of project ID>/`.
- Creation reserves the ID durably before writing the item, so a failed create can leave a gap.

### Loading and writing files

- Only regular files named `<ID>.md` directly in the source directory load. Other files, nested directories, and symlinks are ignored. Archives load on request; `Read` finds both.
- A malformed or misnamed file, an unknown status, or an unreadable file (`unreadable`) produces a finding and partial coverage; other items still load.
- The source path resolves against the control root. Parsing uses up to `GOMAXPROCS` workers.
- Every write to an existing item needs its current content hash; a missing or stale one is a `conflict`. Changing an ID needs `bl renumber`.
- Writes go to a temp file in the same directory, then fsync, rename, and directory fsync, all under the lock. File permissions are kept. A failure before the rename leaves the original intact. A directory-fsync failure after the rename is reported: the new bytes are visible but may not be durable, so reread rather than retry.
- Archive and unarchive take the same version check, refuse an occupied target, and fsync both directories without changing bytes.
- Locking uses local Unix flock.

### Body sections

- **Description.** Everything before the first recognized section heading. Leading and trailing blank lines are trimmed when reading. The writer puts exactly one blank line after the frontmatter.
- **Recognized headings.** These level-2 headings are recognized, matched case-insensitively with surrounding whitespace ignored:
  - `Acceptance criteria`
  - `Definition of done`
  - `Plan`
  - `Notes`
  - `Summary`
  - `Comments`

  Each may appear at most once, in any order. A duplicate is a `duplicate-section` parse error, not a merged or silently discarded section. Headings start at column 1 with `##` and a space; only the name's surrounding whitespace is ignored. The writer inserts a missing section before the next existing section in canonical order, or at the end.
- **Section extent.** A section runs to the next recognized heading or the end of the file. Any other heading inside a section, at any level, belongs to that section.
- **Code fences.** Headings, task-list entries, and comment headers inside fenced code blocks are never structural. Backtick and tilde fences use at least three identical characters, with up to three leading spaces. Closing fences use the same character, at least the opening length, and no non-whitespace suffix. An unclosed fence extends to EOF.
- **Criteria and definition of done.** Each column-1 GFM task-list item (`- [ ] text` or `- [x] text`, `*` and uppercase `X` accepted) is one entry, together with any following lines indented by two or more spaces (or a tab). Entries are numbered from 1 in file order. Numbers aren't stored, so they shift when an entry is added or removed above them. Callers that address entries by number guard the write with the item version (design.md §7). Lines that aren't task-list items are kept but are not entries.
- **Evidence.** An entry's continuation line that starts with `Evidence:` is that entry's evidence. `bl ac ID check N --evidence TEXT` writes or replaces it as one line, and unchecking removes it. Evidence sits under its own entry, so it moves and disappears with that entry and never refers to a number.
- **Comments.** Each comment starts with a line of the form `### <RFC3339 UTC timestamp> <author>` and runs until the next such line or the end of the section. Level-3 headings without that shape are part of a comment body. Text before the first valid header is retained but is not a comment. Comment headers start at column 1; timestamps use `YYYY-MM-DDTHH:MM:SSZ`, and authors are nonempty single-line strings. Comments are append-only through `bl`.
- **Plan, notes, and summary.** Free Markdown. `bl note` appends a paragraph to Notes, separated by one blank line.

### Writing

An edit rewrites only the lines whose values changed. Golden tests hold every other byte identical, including key order, comments, quoting, and list style:

```diff
 status: todo
-priority: medium
+priority: high
 labels: [tui, perf]
-updated: 2026-10-22T09:15:42Z
+updated: 2026-10-23T08:00:00Z
```

- Every write sets `updated` and is atomic (design.md D8).
- New keys and new files use the table's key order, then unknown keys (lexical in new files). Sections follow the order above.
- New lists use flow style (`[a, b]`) unless the line would pass 80 columns, then block style. Empty lists and unset values are removed. New output passes Prettier's defaults.
- New strings are plain when YAML allows, otherwise double-quoted. Edits keep each existing value's quoting and list style, including for list adds and removes.
- Clearing a section removes its heading.
- Text written into a section, note, comment, or new file can't add, remove, or hide a section (a pasted `## Summary`, an unclosed fence), and a comment can't contain a comment header. Such writes fail with `invalid-value`. Added criteria are one line.
- Each mutation returns an exact inverse bound to the new content hash. Applying it checks the version and restores the original bytes except `updated`.

In code, the pure codec takes a clock and returns bytes; the native source owns locking and I/O. `Document.Bytes()` reproduces an existing file exactly. `Serialize(Item)` writes a canonical new file, so text that the Item model doesn't hold (such as loose lines among criteria) survives only document edits.

## Validation

`bl check [--json] [--fix]` reports one finding per problem, each with a code, a severity, a path, and a line:

| Code | Severity | `--fix` |
| --- | --- | --- |
| `invalid-frontmatter`, `missing-required`, `invalid-value` | error | no |
| `filename-mismatch` | error | Rename the file to its ID when the target is free |
| `duplicate-id` | error | No. Use `bl renumber FROM TO`, which rewrites every reference. |
| `unknown-status` | error | no |
| `blocker-cycle`, `parent-cycle` | error | no |
| `duplicate-section` | error | no |
| `merge-conflict` | error | Resolve the two kinds of hunk that `bl` understands. When the sides differ only in `updated`, the later timestamp wins. When both sides only add whole comments, both are kept in timestamp order and exact duplicates dropped. Both marker styles (`merge` and `diff3`) are read. Other hunks are left for you, and the file stays an error until they're gone. |
| `dangling-ref` | warning | no |
| `invalid-ref` | warning | no |
| `noncanonical` | info | Normalize line endings and the trailing newline |

`invalid-ref` flags a `refs` entry that is neither a URL nor a path: one without a slash or a dot that names no file at the repository root, or an item ID. Item IDs belong in `related`; write `./NAME` for an extensionless file that doesn't exist yet.

Exit status is 0 when there are no errors and 5 otherwise, so CI can run `bl check`.
