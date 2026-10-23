# Backlog.md source

`bl` reads and edits Backlog.md 1.52 files in place, without the Backlog.md CLI.
To convert to the native format instead, [import](import.md).

```yaml
# backlog.yaml
version: 1
sources:
  - {id: tasks, type: backlog-md, path: backlog}
```

`path` is the backlog directory or the repository root; `backlog/config.yml`
and root `backlog.config.yml` (with a relative `backlog_directory`) both work.
Without this config, a detected Backlog.md project opens read-only.

## Mapping

| Backlog.md | `bl` |
| --- | --- |
| Status name | Raw status. To Do, In Progress, Done, and Canceled get their categories; `draft` is `backlog`. Project `statuses` override. |
| `assignee` | `assignees` |
| `dependencies` | Blockers |
| `parent_task_id` | Parent |
| `references`, `documentation` | `refs` |
| `modified_files` | `files` |
| Timestamps | UTC |
| `ordinal` | Read and preserved, not editable |
| Generated `#N` criterion indexes | Hidden; regenerated on write |

Reads include tasks, completed tasks, and drafts; archives load on request.
Unknown frontmatter and untouched Markdown stay as they are.

## Edits

Supported: fields, sections, notes and comments, acceptance criteria and DoD,
create, and archive. Writes check the version under the repository write lock
and replace files atomically, so other programs' changes are never overwritten.

```sh
bl set TASK-12 status='In Progress' label+=bug
bl edit TASK-12 --section plan
```

- Section editing works (`description`, `criteria`, `dod`, `plan`, `notes`,
  `summary`); whole-file `bl edit` is native-only.
- Priorities are `high`, `medium`, `low`, or unset; no `urgent`.
- `rank` isn't editable, and an explicit rank on create is rejected.
- Writing criteria keeps the HTML markers Backlog.md expects.
- New items use the configured prefix and padding, named `<id> - <Title>.md`.

## Drafts

In Backlog.md a draft is a folder, not a status. Setting a task's status to
`draft` moves it to `drafts/` under a new draft ID (keeping its stored status).
Setting a draft to another status moves it to `tasks/` under a new task ID with
that status. The result reports the new ID; use it from then on.

Unlike Backlog.md 1.52's promote and demote, `bl` also rewrites references to
the old ID in other tasks and drafts.

## Catalogs

Milestone names, descriptions, and archived state come from `milestones/` and
`archive/milestones/`; label suggestions come from `config.yml`. A milestone
named by title resolves to its unique ID; undeclared values stay valid. Catalogs
are read-only: edit Backlog.md's files to change them. Docs, decisions, and
milestone administration aren't offered.

## Tests

`BL_PROVIDER_TESTS=1` runs compatibility checks against Backlog.md 1.52.0 on
isolated fixtures. Production never needs that executable.
