# Quickstart

One native source and five tasks. BL-3 waits on BL-2, which waits on the
finished BL-1. BL-5 is a draft.

```sh
bl ls
bl next --explain -n 0
```

```text
BL-2  todo   high    Add sign-in page
BL-3  todo   medium  Add password reset
BL-4  todo   low     Write onboarding docs
BL-5  draft          Explore single sign-on

BL-2  todo  high  Add sign-in page
BL-4  todo  low   Write onboarding docs

BL-1  ineligible  high · unblocks 2 (BL-2, BL-3) · status: done
BL-2  ready       high · unblocks 1 (BL-3)
BL-3  blocked     medium · unblocks 0 · blocker: BL-2
BL-4  ready       low · unblocks 0
BL-5  ineligible  unset · unblocks 0 · status: backlog
```

Finish BL-2 and BL-3 becomes ready:

```sh
bl start BL-2
bl ac BL-2 check 1 --evidence 'Manual sign-in works'
bl ac BL-2 check 2
bl done BL-2
bl next
```

```text
BL-3  todo  medium  Add password reset
```

Each task is a Markdown file. [`backlog/BL-2.md`](backlog/BL-2.md) after the
commands above:

```markdown
---
id: BL-2
title: Add sign-in page
status: done
priority: high
labels: [auth]
blocked_by: [BL-1]
created: 2026-10-01T09:05:00Z
updated: 2026-10-23T19:21:19Z
---

Email and password sign-in.

## Acceptance criteria

- [x] Valid credentials open the dashboard
  Evidence: Manual sign-in works
- [x] Invalid credentials show an error
```

Start your own with `bl init`; it writes a `backlog.yaml` like this one.
