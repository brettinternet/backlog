# Tours

A tour walks a reviewer through one change in the TUI Diff tab: each step's
Markdown narrative sits above the line it explains. bl never writes tours; you
do, then hand bl the file.

Write CodeTour JSON for the change's head commit:

```json
{
  "title": "Debounce detail rendering",
  "ref": "0123456789abcdef0123456789abcdef01234567",
  "steps": [
    {"file": "internal/tui/detail.go", "line": 42, "title": "The debounce", "description": "Renders wait **80 ms** after the last keypress.", "criterion": 1},
    {"file": "internal/tui/detail.go", "selection": {"start": {"line": 60, "character": 1}, "end": {"line": 72, "character": 2}}, "description": "The render it waits on."},
    {"file": "internal/tui/detail.go", "line": 17, "side": "old", "description": "The old code rendered on every key."}
  ]
}
```

- ref: the full SHA of the change's head, from `git rev-parse HEAD` in its
  checkout or `head` in `bl changes ID --json`. A tour for any other commit is
  refused; once the change moves on or a step's file has uncommitted edits,
  the TUI shows the tour as stale, so commit and write a new one.
- steps (1 to 200, in reading order): file is a repository-relative path with
  `/` separators; line is 1-based in the file at ref, or before the change
  with `"side": "old"` (a deleted line or a renamed file's old path);
  description is Markdown, up to 16 KiB.
- A step may cover up to 500 lines with CodeTour's selection in place of, or
  beside, line, which must then equal selection.start.line. side applies to
  the whole range, and characters are ignored: the TUI marks every line of
  the range and puts the narrative above its first line.
- Optional: a top-level title and description, and per step a title and
  criterion, the 1-based index of the item's acceptance criterion the step
  satisfies, shown with its text and check state.
- Lines outside the diff are fine: the TUI shows them, and every line of a
  range, with three lines of context. Unknown keys are refused, so a
  misspelled key isn't silently lost.

The schema is docs/schemas/tour.json in bl's repository. Store the file with
any of:

```sh
bl tour ID change.tour --json
```

- MCP: the tour tool with id and file.
- From an action or hook: write `{"tour": "PATH"}` to the file in BL_RESULT.

A refusal names the reason: invalid JSON or a bad step, a criterion the item
lacks, or a ref that heads no change linked to the item, listing their heads.
