# Backlog.md import

`bl import backlog-md` reads Backlog.md files directly; it does not run or shell out to the Backlog.md CLI. The source may be a Backlog.md directory, a repository root with `backlog/config.yml`, or a repository root with `backlog.config.yml`. The root config's relative `backlog_directory` selects the source directory (`backlog` by default).

The destination is a project directory, not a `backlog/` subdirectory. It must be absent or empty, including for `--dry-run`. A successful import writes `.config/backlog.yaml`, active items to `backlog/<ID>.md`, and archived items to `backlog/archive/<ID>.md`. Dry-run performs the same source parsing, mapping, and validation and reports the planned paths and counts, without creating or changing the destination. A non-empty destination is refused.

## Item mapping

| Backlog.md field or content | Native output |
| --- | --- |
| `id` | `id`, upper-cased while preserving zero padding and dotted suffixes (`task-012.3` becomes `TASK-012.3`). Drafts outside the native prefix get new IDs, described below. |
| `title`, `type`, `priority`, `labels`, `milestone`, `due` | Same-named native fields; source status names become the generated status keys in `.config/backlog.yaml` |
| `assignee` | `assignees` list |
| `dependencies` | `blocked_by`; IDs and `parent_task_id` references are upper-cased and rewritten to match imported IDs, including archived items |
| `parent_task_id` | `parent` |
| `ordinal` | Spaced, lexically ordered `rank` keys. Ordinals sort numerically; equal or missing ordinals use source-path order. The gaps leave room for later rank insertions. |
| `references`, `documentation` | Appended to `refs`, preserving both lists and their order |
| `modified_files` | `files` |
| `created_date`, `updated_date` | `created`, `updated` in UTC at whole-second precision. Offset-bearing timestamps are converted to UTC; timestamps without a timezone are interpreted as UTC. If a date is absent, the importer uses the other date or the source file modification time and reports the decision. Invalid dates are refused. |
| Any other task frontmatter key | Retained as a native unknown field with its value |
| Description, Acceptance Criteria, Definition of Done, Plan / Implementation Plan, Notes / Implementation Notes, Summary / Final Summary | Corresponding native body sections. Structural HTML markers such as `<!-- AC:BEGIN -->` and `<!-- SECTION:DESCRIPTION:END -->` are removed. In Acceptance Criteria and Definition of Done task-list entries, one leading generated `#N ` index is also removed; remaining text, order, and checked state are retained (`#1 #12 regression` becomes `#12 regression`). Fenced code and other sections are unchanged. |
| Backlog.md Comments block | Native timestamped comments. `author` and `created` are converted from each legacy entry; unstructured comment text is retained under an `unknown` author. |
| Other level-two body sections | Retained under Description with their heading and contents. |

Tasks in `completed/` receive the `done` status. Files in `archive/tasks/` and `archive/drafts/` are written under `backlog/archive/`. Drafts receive the `draft` status, whose category is `backlog`; archived drafts retain that status. A native ID never changes when a draft is promoted, so drafts whose prefix isn't the native prefix (Backlog.md's `DRAFT-1`) are numbered after the highest ID in that prefix, active and archived, in draft ID order, as `backlog draft promote` would: with `TASK-20` as the highest task, `DRAFT-1` becomes `TASK-21`. References to a draft follow its new ID, and the draft's file report shows it. IDs that collide after upper-casing or native padded-ID lookup normalization are refused rather than silently merged. Each output item is reparsed before anything is written; if source text would change native structure, the import is refused and names the file. For example, a comment line shaped like a native comment header (`### 2026-01-02T03:04:00Z name`) would otherwise split the comment and attribute its text to another author. Edit the source text, for instance by indenting that line, and import again.

## Project configuration and milestones

The importer writes a native `tasks` source at `backlog`, a `draft` status first, then a status entry for each source status and any required standard status, the source project's name, all configured labels, and `defaults.dod` from Backlog.md's `definition_of_done`. Backlog.md's `task_prefix` is upper-cased and written as the native source prefix (for example, `task` becomes `TASK`); if absent, the importer infers it from the first non-archived, non-draft task ID, then any archived task ID, or uses `TASK` when there are only drafts or no items. `ready.statuses` contains generated To Do-category keys. Configured `blocked` and `deferred` labels are copied to `ready.excludeLabels`. The source config's `default_status` has no native project-wide equivalent and is reported as skipped.

Every `milestones/*.md` and `archive/milestones/*.md` file becomes a project milestone: frontmatter `id` to `key`, `title` to `name`, Description section to `description`, and archived-file location to `archived: true`. A task milestone matching an imported key or unique title is rewritten to that key. An unresolved or ambiguous value is kept as written and reported.

## Report and skipped material

The JSON-serializable report contains per-source-file, per-field original and output values; configuration mappings; output paths and counts; dry-run state; mapping decisions (including generated criterion-index removal); skipped paths/settings; and unresolved milestones. Task and milestone files are listed separately, while their output path points into the native project. Paths in `outputPaths` are absolute; per-file `outputPath` values are relative to the destination.

The importer does not import Backlog.md documentation, decisions, locks, or unsupported archive partitions. It reports these paths and source configuration settings without native equivalents under `skipped`. Unsupported files inside task or milestone partitions (including symlinks and non-Markdown files) are also reported. It never edits the source directory.
