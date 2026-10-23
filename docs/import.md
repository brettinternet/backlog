# Backlog.md import

[Standalone cutover rehearsal](#standalone-cutover-rehearsal) covers backup,
comparison, agent routing, and rollback without importing a real project.

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

## Standalone cutover rehearsal

Run `task docs:test` from this repository. `TestMigrationRehearsal` executes the
shell blocks below against `internal/doctest/testdata/migration`, a synthetic
Backlog.md project. It supplies an empty temporary `BL_REHEARSAL_DIR`, a built
`bl`, isolated HOME/XDG directories with synthetic identity `@example`, and the existing
fake Worklease executable.
No sibling checkout, credentials, service, real-project data, global skill edits,
or real-provider opt-in is required. The fake is a test fixture, not a production
claim authority. These blocks run together in that harness, not in a real project.

### Freeze, back up, and import

Before a real, separately authorized cutover, stop **all** writers: agents,
supervisors, CLI/MCP servers, editor integrations, hooks, scheduled jobs, and CI
jobs that write tasks. Reconcile and release outstanding claims using their old
provider workflow. Record the source revision and inventory task/draft/archive
files, milestones, labels, and configuration. Copy the whole source, including
docs and decisions that import skips. Verify the backup before proceeding. Do
not use a status change as a lock or run old and new writers together.

The rehearsal starts with no writers or claims:

```sh
cp -R internal/doctest/testdata/migration "$BL_REHEARSAL_DIR/legacy"
cd "$BL_REHEARSAL_DIR"
cp -R legacy legacy-backup
diff -r legacy legacy-backup
bl import backlog-md legacy --to native --dry-run --json > preview.json
test ! -e native
bl import backlog-md legacy --to native --json > import.json
git -C native init --quiet
bl -C native check --json
bl -C native show TASK-1 --json > before-loop.json
bl -C native show TASK-3 --json > relationships.json
diff -r legacy legacy-backup
```

Inspect every report mapping, decision, skipped setting/path, and unresolved
milestone before approving cutover. Compare source and destination inventories,
body sections, criterion text/checks, comments, dates, custom metadata, assignees,
references, files, relationships, labels, and milestones (including archived
state). Expect the documented ID/status/index/rank transformations, not byte
identity. The test compares these synthetic values before any task mutation and
checks the archived task too. Keep the backup and reports outside the destination;
a non-empty destination is refused, including for dry-run.

### Switch the provider and complete a native loop

Only after comparison passes, change the project's local instructions, launcher,
and CI routing to the native project. Native projects use `bl instructions
workflow` and `bl next --claim --start --session SESSION`; all contenders use the
same project identity, authority, control root, and the exact resource from
`bl show --json`. Do not derive alternate Worklease keys. In a repository with a
native wrapper (like this one), use that wrapper instead of these bare `bl`
commands. Do not restart a legacy supervisor after cutover.

**Unmigrated Backlog.md projects retain their existing Backlog.md CLI/MCP and
coordination workflow.** Presence of a legacy config alone is not authorization
to import or switch providers. Resolve the project's declared provider and read
its instructions first; for mixed sources, route by the selected item's source,
not its ID spelling. Do not apply this repository's native override globally or
edit shared skills. The unchanged `legacy` fixture represents that route until
cutover, and `restored` represents it again after rollback.

The harness enables fake coordination only in the temporary native project:

```sh
printf '\ncoordination:\n  worklease: {}\n' >> native/.config/backlog.yaml
bl -C native instructions workflow
bl -C native next --claim --start --session rehearsal-single --json > selected.json
bl -C native heartbeat --session rehearsal-single --json
version=$(bl -C native show TASK-1 --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["item"]["version"])')
bl -C native ac TASK-1 check 1 --evidence 'Synthetic comparison passed in TestMigrationRehearsal' --if-version "$version" --json
bl -C native note TASK-1 'Synthetic native loop verified; retain reports for rollback.' --json
bl -C native done TASK-1 --session rehearsal-single --json > done.json
bl -C native show TASK-1 --json > after-loop.json
```

Use one stable, distinct session per loop; renew before half TTL. Inspect both
status and claim outcomes, including on failure. An uncertain acquisition must
recover with the **same** session, never a new session or key. A failed status
write may retain a claim; a failed release may leave the status written. A
verified supervised run owns renewal/release instead of its worker. Record
criterion evidence with the current version; finish only after required checks
and delivery. `done` writes then releases before returning.

### Two independent loops

With TASK-1 done, TASK-2 and the now-unblocked TASK-3 are ready. The fake's
barrier forces both processes to contend on the first ranked candidate, proving
that selection skips confirmed contention rather than treating status as a claim:

```sh
BL_FAKE_BARRIER=1 bl -C native next --claim --start --session rehearsal-a --json > a.json &
first_pid=$!
BL_FAKE_BARRIER=1 bl -C native next --claim --start --session rehearsal-b --json > b.json &
second_pid=$!
wait "$first_pid"
wait "$second_pid"
first_item=$(python3 -c 'import json; print(json.load(open("a.json"))["data"]["items"][0]["id"])')
second_item=$(python3 -c 'import json; print(json.load(open("b.json"))["data"]["items"][0]["id"])')
test "$first_item" != "$second_item"
bl -C native done "$first_item" --session rehearsal-a --json
bl -C native done "$second_item" --session rehearsal-b --json
```

### Stop and roll back

For a real rollback, quiesce native writers and reconcile/release their claims
first. Preserve the native project and event/evidence reports. Compare all changes
since cutover and explicitly translate any work to the old provider before
restarting it: copying the old backup alone loses post-cutover progress. Restore
the verified backup to a separate location, validate it with the old provider,
switch local instructions/launchers back, then enable only the old writers.
Never run both providers to resolve divergence.

The rehearsal deliberately retains the completed native project alongside the
byte-identical restored legacy source. Its three synthetic completions and
criterion evidence stay in `native`; they are **not** silently reverse-imported.
The restore models a rollback that quarantines native progress for reconciliation:

```sh
cp -R legacy-backup restored
diff -r legacy-backup restored
diff -r legacy legacy-backup
bl -C native check --json
```

The test verifies zero retained fake claims before restore and retained native
progress afterwards. Temporary projects are removed by the test harness. No real
migration is a release prerequisite; each needs separate owner authorization.
