# Import from Backlog.md

Convert a Backlog.md folder to native files. The input is untouched; the output
goes to a new directory, `backlog-import/` by default.

```sh
bl import backlog-md
```

```text
Imported 4 item(s) (1 draft(s), 0 archived) and 1 milestone(s) into backlog-import
  drafts/draft-1 - Explore-single-sign-on.md → backlog/TASK-4.md
  milestones/m-1 - Beta.md → backlog.yaml m-1
  tasks/task-1 - Set-up-CI.md → backlog/TASK-1.md
  tasks/task-2 - Add-sign-in-page.md → backlog/TASK-2.md
  tasks/task-3 - Add-password-reset.md → backlog/TASK-3.md
note Removed one leading #N index per criterion in 1 file(s); text, order, and checked state are kept.
skipped backlog/config.yml default_status: Backlog.md default status has no native project-wide default
```

Preview with `--dry-run`; add `--json` for every field mapping. Statuses,
labels, and milestones become `backlog.yaml`:

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

The draft keeps status `draft`. To switch over, move the old folder aside and
the new files into place:

```sh
mv backlog backlog-md
mv backlog-import/backlog backlog-import/backlog.yaml .
bl ls --group milestone
```

```text
m-1 (2)
TASK-2  todo   high    Add sign-in page
TASK-3  todo   medium  Add password reset

none (1)
TASK-4  draft          Explore single sign-on
```

Commit the move, then delete `backlog-md/` once nothing reads it.
