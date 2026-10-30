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
  refused. Once the change moves on, the TUI carries the tour to the new head
  while ref is still its ancestor: unchanged lines move, and steps on lines
  edited or deleted since show outdated with their old lines. After rewritten
  history, or while a step's file has uncommitted edits, it shows the tour as
  stale. Write a new tour for the new head when steps are outdated or stale.
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
bl tour ID - --json < change.tour
```

- MCP: the tour tool with id and file, or, when your client doesn't share
  bl's filesystem (a sandbox, a container, another machine), id and the tour
  object itself as tour: `{"id": "ID", "tour": {"ref": "…", "steps": […]}}`.
- From an action or hook: write `{"tour": "PATH"}` to the file in BL_RESULT.

A refusal names the reason: invalid JSON or a bad step, a criterion the item
lacks, or a ref that heads no change linked to the item, listing their heads.
Each step is checked against Git objects, never the worktree: its file must
exist at ref (with `"side": "old"`, before the change, where a renamed file
may use either path) and its line, or its range's last line, must be within
the file. One refusal lists every bad step with its number, file, lines, and
the file's line count, so fix them all before storing again. A head your checkout doesn't hold, such as an
unfetched pull request, can't be checked and is stored as is.

Read back what is stored, and whether it is stale, with `bl tour ID --json`
(MCP: the tour tool without file or tour). Each entry is the stored tour plus change,
ref, and stale; an item without one returns `"tours": []`. `bl changes ID
--json` marks each change with a stored tour as `tour: {ref, steps, stale}`,
so automation can skip changes whose tour is current.

Storing again replaces a change's tour. To drop a wrong one, run
`bl tour ID --remove [CHANGE] --json` (MCP: the tour tool with remove and
change); CHANGE is needed only when several linked changes have tours.

Tours are opt-in. A project's `tours.mode` in backlog.yaml is `off` (the
default), `suggest`, or `require`, and `tours.stages` (worktree, branch, pr)
narrows which open changes need one. Under suggest, `bl instructions workflow`
asks for a tour of each such change and the done response's `tourRequest`
names any still without one; under require, done refuses until each has a
tour for its current head, naming the change and the command to store it. A
tour carried forward to the head with no step outdated counts as current.
