# Backlog.md

Read and edit an existing [Backlog.md](https://github.com/MrLesk/Backlog.md)
project in place. The tasks stay in Backlog.md's format, so its own CLI keeps
working. Statuses and labels come from [`backlog/config.yml`](backlog/config.yml).

```yaml
sources:
  - {id: tasks, type: backlog-md, path: backlog}
```

```sh
bl ls
bl next --explain -n 0
```

```text
TASK-2  To Do  high    Add sign-in page
TASK-3  To Do  medium  Add password reset
TASK-2  To Do  high  Add sign-in page

TASK-1  ineligible  high · unblocks 2 (TASK-2, TASK-3) · status: done
TASK-2  ready       high · unblocks 1 (TASK-3)
TASK-3  blocked     medium · unblocks 0 · blocker: TASK-2
```

```sh
bl set TASK-2 status='In Progress' label+=frontend
bl ac TASK-2 check 1
bl new 'Write onboarding docs #docs' --ac 'README covers setup'
```

The edited file keeps Backlog.md's markers:

```markdown
---
id: TASK-2
title: Add sign-in page
status: In Progress
assignee: []
created_date: '2026-10-01 09:05'
updated_date: '2026-10-23 19:24'
labels: [auth, frontend]
dependencies: [TASK-1]
priority: high
---
…
## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 Valid credentials open the dashboard
- [ ] #2 Invalid credentials show an error
<!-- AC:END -->
```

The new task is `backlog/tasks/task-4 - Write-onboarding-docs.md`.

Without `backlog.yaml`, `bl` still finds `backlog/config.yml` and opens it
read-only. To convert to native files instead, see [import](../../docs/import.md):

```sh
bl import backlog-md --dry-run
```
