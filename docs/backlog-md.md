# Backlog.md source

`bl` reads and edits Backlog.md 1.52 files directly. It never invokes the
Backlog.md CLI in production. To keep the original format instead of
[importing](import.md), configure a source:

```yaml
# .config/backlog.yaml
version: 1
sources:
  - {id: tasks, type: backlog-md, path: backlog}
```

The path can name the backlog directory or the repository root. Both
`backlog/config.yml` and root `backlog.config.yml` are supported, including a
relative `backlog_directory`. Without explicit source configuration, a detected
Backlog.md project opens read-only and prompts you to configure it. No write
capabilities are granted by detection.

## Mapping and edits

Raw status names are retained. Configured statuses are added to the project's
status list; familiar To Do, In Progress, Done and Canceled names receive their
corresponding categories. `draft` is a backlog-category status. Project status
entries take precedence over inferred categories.

The adapter maps `assignee` to assignees, `dependencies` to blockers,
`parent_task_id` to parent, `references` and `documentation` to refs,
`modified_files` to files, and legacy timestamps to UTC. Unknown frontmatter and
untouched Markdown remain in their original files. Reads include tasks,
completed tasks and drafts; archived tasks and drafts are available on demand.

Supported edits include fields, named body sections, appended notes and
comments, acceptance criteria and Definition of Done, creation, and archival.
Use `bl edit ID --section description` (or `criteria`, `dod`, `plan`, `notes`,
`summary`) for editor-based section changes. Whole-file editing is native-only.
Backlog.md priorities are `high`, `medium`, `low`, or unset; `urgent` is not
offered. Native lexical `rank` editing is unavailable; provider ordinals remain
readable and are preserved. Explicit ranks on creation are rejected, never
silently discarded.

The adapter checks the content version under the shared repository write lock
before publishing a mutation with an atomic file replacement. Changes made by
other programs are never silently overwritten.

Backlog.md's generated `#N` criterion indexes are omitted from the item model.
Writing criteria regenerates sequential indexes and keeps the HTML section
markers expected by Backlog.md. New items use the configured task prefix and
zero padding, with filenames `<id> - <Title>.md`.

## Draft identity

Draft is a folder, not `status: Draft` in task frontmatter. Setting a task's
status to `draft` moves it into `drafts/`, allocating the next draft ID while
retaining its previous stored status. Setting a draft to another status moves
it into `tasks/`, allocates the next task ID and writes the requested status.
The mutation result reports the new ID.

Unlike Backlog.md 1.52's demote/promote commands, `bl` rewrites references in
other task and draft files to the new ID. Use the returned ID for subsequent
operations; a previous version or ID is not a valid write target.

## Catalogs and limits

Milestone names, descriptions and archived state come from `milestones/` and
`archive/milestones/`. The configured label suggestions come from `config.yml`.
A milestone reference by title resolves to its unique catalog ID; undeclared
values remain valid. Catalogs are read-only: edit the provider's files or config
to change them. Docs, decisions, tracker settings and milestone administration
are not item operations and are not offered.

Compatibility checks are opt-in with `BL_PROVIDER_TESTS=1` and use Backlog.md
1.52.0 on isolated fixtures. Production does not require that executable.
