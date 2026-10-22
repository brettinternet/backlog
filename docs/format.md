# Native format v1

**Status:** Frozen v1. Any incompatible change needs a new `version` in `.config/backlog.yaml` and a migration in `bl check --fix`.

The native format is plain Markdown with YAML frontmatter, and it is designed for Git:

- One file per item, named by ID.
- Minimal diffs on edit.
- Nothing in a filename or directory changes when a field changes.
- The files read well without `bl`.

## Layout

```text
.config/backlog.yaml     project config (design.md §5); its presence marks a bl project
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

Other keys are kept byte-for-byte, shown raw in detail, and queryable as `field.NAME:VALUE` (query.md). An ID reference may name another source as `source:ID`. `bl` preserves it, and readiness treats it as `unknown` unless the reference resolves in the loaded workspace.

### IDs

- Grammar: `^[A-Z][A-Z0-9]*-[0-9]+(\.[0-9]+)*$`.
- Comparison is natural: prefix, then each numeric segment as an integer. `BL-9 < BL-10`, and `BL-007` equals `BL-7` for lookup.
- New IDs are `PREFIX-N`, with no padding and no dotted segments (design.md D6). `bl import` keeps imported IDs exactly, including padding and dots.
- The allocator holds `<git-common-dir>/backlog/lock` (flock) and stores a counter at `<git-common-dir>/backlog/next/<source-id>`. The next number is the greater of the counter and one more than the highest number among active and archived items. Outside Git, the lock and counter live under XDG state, keyed by project ID.

### Native source I/O

The native source loads only regular files with native ID filenames directly in
its source directory. It ignores other files, nested directories, and symlinks.
Archive loading is opt-in; `Read` can find active or archived items. Malformed
items and filename/status mismatches produce per-file findings and partial
coverage without hiding successfully parsed items. Loads use at most GOMAXPROCS
parsers. The configured source path is resolved against the control root before
opening the source; Git discovery never runs a subprocess.

Every existing-item mutation requires its current content hash. Missing or stale
versions return reason `conflict` before writing. ID changes require renumbering,
not field edits. Creation allocates and durably reserves an ID before publishing
the item; failed creates may leave gaps. The allocator also checks active and
archived filenames on each reservation, including imported padded or dotted IDs
(the first numeric component advances the sequential counter). Outside Git,
state is under `$XDG_STATE_HOME/backlog/projects/<SHA-256 of project ID>/`.

Item writes and counter updates use a same-directory temporary file, fsync,
rename, and directory fsync, under the shared lock. Existing file permissions
are retained. Failures before rename leave the original intact and remove owned
temporary files. A directory-fsync error after rename is reported, but the new
bytes are already visible and durability is uncertain; callers must reread
rather than retry blindly. Archive/unarchive require the same version guard,
refuse occupied targets, and fsync both directories without rewriting bytes or
changing the version. The source currently supports local Unix flock semantics;
filesystem watching is a separate optional capability.

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

- An edit rewrites only the lines that represent changed values. Every other byte stays identical, including key order, comments, quoting, and list style. Golden tests enforce this.
- New keys and new files use the canonical key order from the table above, then unknown keys.
- New lists use flow style (`[a, b]`). Empty lists and unset optional values are removed, not written as empty.
- Strings in new fields/files are written plain when YAML allows it, and quoted otherwise (the YAML emitter may use single quotes). Existing scalar quote style and list flow/block style are retained on edits where applicable. List add/remove operations keep the quoting of existing elements.
- Every write updates `updated` and is atomic (design.md D8). The pure codec takes an explicit clock and returns bytes; the native source owns the lock, fsync, and rename.
- `Document.Bytes()` losslessly serializes an existing document. `Serialize(Item)` creates a canonical new file: it emits known keys in table order, unknown keys in lexical order, and nonempty sections in the order above. Unstructured text among criteria or before the first comment header is preserved by document edits, not reconstructed from an Item.
- Applying a mutation returns a representation-exact inverse bound to the resulting content hash. Applying that inverse refuses a version mismatch and restores the original bytes except for `updated`; undo also returns a redo inverse. Unknown keys and unedited sections are never reserialized.

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
| `noncanonical` | info | Normalize line endings and the trailing newline |

Exit status is 0 when there are no errors and 5 otherwise, so CI can run `bl check`.
