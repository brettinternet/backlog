# Import from Backlog.md

Convert a committed Backlog.md folder to native files in place. Preview first:

```sh
git add -A
git commit -qm 'Add Backlog.md'
bl import backlog-md --in-place --dry-run
bl import backlog-md --in-place
```

```text
Imported 4 item(s) (1 draft(s), 0 archived) and 1 milestone(s) into .
  drafts/draft-1 - Explore-single-sign-on.md → backlog/TASK-4.md
  milestones/m-1 - Beta.md → backlog.yaml m-1
  tasks/task-1 - Set-up-CI.md → backlog/TASK-1.md
  tasks/task-2 - Add-sign-in-page.md → backlog/TASK-2.md
  tasks/task-3 - Add-password-reset.md → backlog/TASK-3.md
note Removed one leading #N index per criterion in 1 file(s); text, order, and checked state are kept.
note Renumbered 1 item(s) whose IDs were drafts outside TASK, not PREFIX-N, or reused; each keeps its old ID as legacy_id.
skipped backlog/config.yml default_status: Backlog.md default status has no native project-wide default
removed 6 Backlog.md file(s), including config; docs and decisions stay
```

It refuses while the Backlog.md files have uncommitted changes, so every removed
file stays recoverable with `git`. Without `--in-place`, the input is untouched
and the output goes to `backlog-import/`. Add `--json` for every field mapping.

Statuses, labels, and milestones become `backlog.yaml`:

```yaml
version: 1
project:
  name: Shop
sources:
  - id: tasks
    type: native
    path: backlog
    prefix: TASK
statuses:
  - key: draft
    name: Draft
    category: backlog
  - key: todo
    name: To Do
    category: todo
  - key: in-progress
    name: In Progress
    category: active
  - key: done
    name: Done
    category: done
workflow:
  start: in-progress
  done: done
milestones:
  - key: m-1
    name: Beta
    description: Sign-in for invited users.
labels:
  - auth
  - docs
ready:
  statuses:
    - todo
  satisfiedBy:
    - done
    - canceled
  skipContainers: true
```

[`task-2 - Add-sign-in-page.md`](backlog/tasks/task-2%20-%20Add-sign-in-page.md)
becomes `TASK-2.md`. Dependencies become `blocked_by`, and the acceptance
criteria lose their markers:

```markdown
---
id: TASK-2
title: Add sign-in page
status: todo
priority: high
labels: [auth]
milestone: m-1
blocked_by: [TASK-1]
rank: a9999999999999999
created: 2026-10-01T09:05:00Z
updated: 2026-10-01T09:05:00Z
---

Email and password sign-in.

## Acceptance criteria

- [ ] Valid credentials open the dashboard
- [ ] Invalid credentials show an error
```

The draft keeps status `draft` and becomes `TASK-4`, with `legacy_id: DRAFT-1`.
Check the result, then commit it:

```sh
bl check
bl ls --group milestone
```

```text
No findings.
m-1 (2)
TASK-2  todo   high    Add sign-in page
TASK-3  todo   medium  Add password reset

none (1)
TASK-4  draft          Explore single sign-on
```
