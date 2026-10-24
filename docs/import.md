# Backlog.md import

```text
bl import backlog-md [PATH] [--to DIR | --in-place] [--dry-run] [--json]
```

Reads Backlog.md files directly (never the Backlog.md CLI) and writes a native
project, either into a new directory, leaving the source unchanged, or in place
of the Backlog.md files (below).

| Input | Accepted |
| --- | --- |
| PATH | A Backlog.md directory, or a repository root with `backlog/config.yml` or `backlog.config.yml` (honoring a relative `backlog_directory`) |
| `--to` | A project directory that is empty or absent, even for `--dry-run`. Refusing one that holds the Backlog.md folder suggests `--in-place`. |
| `--in-place` | Instead of `--to`: write into the source's project root and remove the imported Backlog.md files |

Output:

```text
DIR/backlog.yaml
DIR/backlog/<ID>.md
DIR/backlog/archive/<ID>.md
```

`--dry-run` parses, maps, and validates everything and reports the planned paths
and counts without writing. To rehearse a full cutover, see
[below](#standalone-cutover-rehearsal).

## In-place cutover

```text
bl import backlog-md --in-place --dry-run   # review the report
bl import backlog-md --in-place
bl check
git status                                  # review, then commit
```

`--in-place` writes `backlog.yaml` at the checkout root and the items into the
Backlog.md folder, which becomes the native source path (`backlog` or
`docs/backlog`). It then removes every imported task, draft, archive, and
milestone file and `config.yml`. Docs, decisions, and other files stay. Empty
partition folders are removed.

It refuses, before writing anything, when:

- the project isn't a Git checkout, or the Backlog.md folder or config has
  uncommitted changes, so every removed file stays recoverable with `git`;
- a file to remove isn't in the index: ignored, behind a symlinked folder, or
  in a nested repository;
- no task or milestone files were found;
- `backlog.yaml` or `backlog.local.yaml` exists;
- a planned output file exists.

Output files are created without replacing anything, and are removed again if
any write fails. Source files are removed only after every output is written.
Stop every Backlog.md writer first, as in step 1 of the rehearsal.

## Item mapping

| Backlog.md | Native |
| --- | --- |
| `id` | `id`, upper-cased, keeping padding and dots (`task-012.3` → `TASK-012.3`). Drafts and slug IDs may be renumbered (below). |
| `title`, `type`, `labels`, `milestone` | Same names. Status names become generated status keys. |
| `priority` | Same name. `p0` and `critical` become `urgent`, `p1` `high`, `p2` and `normal` `medium`, `p3` and `later` `low` (noted). Other values fail with the accepted list. |
| `due_date` (or `due`) | `due` |
| `assignee` | `assignees` |
| `dependencies` | `blocked_by`, rewritten to imported IDs (including archived). Cycles are broken or refused (below). |
| `parent_task_id` | `parent` |
| `ordinal` | Spaced `rank` keys in numeric ordinal order; ties and missing ordinals follow source path |
| `references`, `documentation` | `refs`, both lists in order, so the distinction between them is lost. A path to an imported item becomes its output path. An imported item's ID moves to `related`, following renumbering (noted). A value that is neither a URL nor a path, such as a commit hash, stays in an unknown `references` field and is reported in `unresolvedReferences`. |
| `modified_files` | `files` |
| `created_date`, `updated_date` | `created`, `updated` in UTC to the second. No time zone means UTC. An empty value counts as missing. A missing `updated` uses `created`. A missing `created` uses the file's first Git commit, unless that is after `updated`; otherwise `updated`, else the file's mtime (noted). Invalid dates fail. |
| Other frontmatter | Kept as unknown fields |
| Description, Acceptance Criteria, Definition of Done, (Implementation) Plan, (Implementation) Notes, (Final) Summary | Matching native sections |
| Comments block | Native comments; unstructured text gets author `unknown` |
| Other `##` sections | Appended to Description with their heading |

Backlog.md markers like `<!-- AC:BEGIN -->` are removed, as is one generated
`#N ` index per criterion:

```text
- [ ] #1 #12 regression      →      - [ ] #12 regression
```

Text, order, and checked state are kept; fenced code is untouched.

### Status, archives, and drafts

- Tasks in `completed/` get `done`.
- `archive/tasks/` and `archive/drafts/` go to `backlog/archive/`.
- Drafts get the `draft` status (category `backlog`), archived or not.
- Native IDs don't change on promotion, so drafts outside the task prefix are
  renumbered after the highest task ID, in draft order, as
  `backlog draft promote` would. With `TASK-20` the highest, `DRAFT-1` becomes
  `TASK-21`. Archived drafts often reuse a live draft's ID; each gets its own.
- IDs that aren't `PREFIX-N` (slugs like `pw-some-slug-2`) are renumbered after
  the drafts.

A renumbered item keeps its old ID in `legacy_id`, and the report lists each in
`renumbered`. References follow. A dependency on an ID several items had
resolves to the only unarchived holder, else fails.

### References and links

Text in every section and comment is rewritten for the new layout:

- A standalone mention of a renumbered ID becomes the new ID. `DRAFT-1.2`,
  `draft-1-notes`, and `drafts/draft-1.md` are left alone.
- A relative Markdown link to an imported item points at its output file;
  other relative links are rebased so they still name the same file.

```text
tasks/task-1.md: [spec](../docs/spec.md) [peer](task-2%20-%20Peer.md#plan)
backlog/TASK-1.md: [spec](docs/spec.md) [peer](TASK-2.md#plan)
```

Rebasing assumes the native project replaces the source in place, as
`--in-place` does. Fenced code is untouched. A link into a Backlog.md folder
that isn't an imported item, or a mention of an ID several items had, is left
as written and reported in `unresolvedReferences`.

### Symlinks and cycles

A symlink in a task or milestone folder is read as the Markdown file it names
(noted); its links resolve from the target's directory. In-place cutover
removes the link, not the target. A broken link or a non-Markdown target fails.

Native `check` rejects dependency cycles. In a cycle whose members are all
closed (done, canceled, or archived), each member drops its dependencies on
equal or higher IDs, and the report notes each dropped edge. A cycle with an
open item fails; remove one dependency in the source.

### Refusals

- IDs that collide after upper-casing or padding normalization.
- Dependency cycles with an open item, ambiguous dependencies, broken symlinks,
  and unknown priorities (above).
- Text that would change native structure. Every output item is reparsed first.
  For example, a comment line shaped like a native header
  (`### 2026-01-02T03:04:00Z name`) would split the comment; indent it in the
  source and import again. The error names the file.

## Project config

| Generated | From |
| --- | --- |
| Source | `{id: tasks, type: native, path: backlog}` |
| Prefix | `task_prefix` upper-cased; else the first active task ID, then any archived task ID, else `TASK` |
| Statuses | `draft` first, then each source status, plus any required standard status |
| `ready.statuses`, `ready.satisfiedBy` | The default categories, `[todo]` and `[done, canceled]` |
| `ready.excludeLabels` | `blocked` and `deferred`, if configured as labels |
| Project name, `labels` | Copied |
| `defaults.dod` | `definition_of_done` |
| `default_status` | Skipped (no native equivalent) and reported |

Every `milestones/*.md` and `archive/milestones/*.md` becomes a milestone: `id` →
`key`, `title` → `name`, the Description section → `description`, and the
archive location → `archived: true`. A task milestone matching a key or a unique
title becomes that key; an unresolved or ambiguous one is kept as written and
reported.

## Report

Without `--json`, the report prints the counts, each source file and its
output, notes, skipped content, unresolved milestones and references, and
for `--in-place` the count of removed files:

```text
Would import 4 item(s) (1 draft(s), 0 archived) and 1 milestone(s) into backlog-import
  drafts/draft-1 - Explore-single-sign-on.md → backlog/TASK-4.md
  milestones/m-1 - Beta.md → backlog.yaml m-1
  tasks/task-1 - Set-up-CI.md → backlog/TASK-1.md
note Removed one leading #N index per criterion in 1 file(s); text, order, and checked state are kept.
skipped backlog/config.yml default_status: Backlog.md default status has no native project-wide default
```

Notes cover what this import guessed or changed: an inferred prefix, a created
date's source, a comment without a timestamp, an ambiguous milestone title,
criterion-index removal, renumbering, priority aliases, statuses the config
doesn't list, followed symlinks, and dropped cycle edges. The rules above that
always apply aren't repeated.

The JSON report, in the envelope's `data`, adds each field's original and output
value per source file, config mappings, output paths, `renumbered`
(`sourcePath`, `from`, `to`), `unresolvedReferences` (`sourcePath`, `value`,
`reason`), and for `--in-place` the project-relative `removed` paths. Notes are
in `decisions`. `outputPaths` are absolute; each file's `outputPath` is relative to
the destination.

Skipped and reported: documentation, decisions, locks, unsupported archive
folders, non-Markdown files in task and milestone folders, and config settings
without a native equivalent.

## Standalone cutover rehearsal

`task docs:test` runs `TestMigrationRehearsal`, which executes the four `sh`
blocks below, in order, against the synthetic Backlog.md project in
`internal/doctest/testdata/migration`. The harness provides an empty
`BL_REHEARSAL_DIR`, a built `bl`, isolated HOME and XDG with identity
`@example`, and a fake Worklease. No real project, credential, or service is
involved. The blocks are written for that harness, not for a real project.

### 1. Freeze, back up, and import

Before a real cutover, which needs its own owner approval:

1. Stop **every** writer: agents, supervisors, CLI and MCP servers, editor
   integrations, hooks, scheduled jobs, and CI jobs that write tasks.
2. Reconcile and release outstanding claims through the old workflow.
3. Record the source revision and inventory tasks, drafts, archives,
   milestones, labels, and config.
4. Copy the whole source, including the docs and decisions import skips, and
   verify the copy.

A status change is not a lock, and old and new writers never run together.

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

Review every mapping, decision, skipped item, and unresolved milestone. Compare
inventories, sections, criteria and their checks, comments, dates, custom
fields, assignees, references, files, relationships, labels, and milestones
(including archived ones). Expect the documented ID, status, index, and rank
changes, not byte identity. The test checks these values, including the
archived task, before any mutation. Keep the backup and reports outside the
destination.

### 2. Switch and run one native loop

Only after the comparison passes, point local instructions, launchers, and CI at
the native project. Use `bl instructions workflow` and
`bl next --claim --start --session SESSION`. Every contender must share the
project ID, authority, control root, and the exact resource from
`bl show --json`; never derive another Worklease key. Repositories with a
wrapper (like this one) use it instead of bare `bl`. Don't restart a legacy
supervisor.

**Unmigrated Backlog.md projects keep their existing Backlog.md workflow.** A
legacy config alone doesn't authorize importing or switching. Read the
project's declared provider and instructions first. With mixed sources, route
by the item's source, not its ID spelling. Don't apply this repository's native
override globally or edit shared skills. In the rehearsal, `legacy` and later
`restored` stand for that route.

```sh
printf '\ncoordination:\n  worklease: {}\n' >> native/backlog.yaml
bl -C native instructions workflow
bl -C native next --claim --start --session rehearsal-single --json > selected.json
bl -C native heartbeat --session rehearsal-single --json
version=$(bl -C native show TASK-1 --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["item"]["version"])')
bl -C native ac TASK-1 check 1 --evidence 'Synthetic comparison passed in TestMigrationRehearsal' --if-version "$version" --json
bl -C native note TASK-1 'Synthetic native loop verified; retain reports for rollback.' --json
bl -C native done TASK-1 --session rehearsal-single --json > done.json
bl -C native show TASK-1 --json > after-loop.json
```

The [agent loop rules](guide.md#agent-loop) apply: one stable session per loop,
heartbeat before half TTL, check both outcomes, recover uncertainty with the same
session, and finish only after verification.

### 3. Two independent loops

With TASK-1 done, TASK-2 and the now-unblocked TASK-3 are ready. The fake's
barrier makes both loops contend for the first candidate, proving selection
skips contention instead of treating status as a claim:

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

### 4. Stop and roll back

For a real rollback:

1. Stop native writers and reconcile or release their claims.
2. Keep the native project and its event and evidence reports.
3. Compare every change since cutover and translate that work to the old
   provider. Restoring the backup alone loses it.
4. Restore the backup to a separate location and validate it with the old
   provider.
5. Switch instructions and launchers back, then start only the old writers.

Never run both providers to resolve divergence. The rehearsal keeps the native
project, with its three completions and evidence, beside a byte-identical
restored source. Nothing is reverse-imported:

```sh
cp -R legacy-backup restored
diff -r legacy-backup restored
diff -r legacy legacy-backup
bl -C native check --json
```

The test checks that no fake claims remain before the restore and that native
progress survives it. The harness deletes the temporary projects.
