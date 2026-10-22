---
id: BL-007.2
title: 'Original title' # keep title comment
status: todo
# keep this YAML comment
labels: ['two'] # keep labels comment
assignees:
  - '@one'
  # keep intervening comment
  - '@two'
priority: high
custom: {nested: [a, b]} # untouched unknown field
created: 2026-10-21T17:06:00Z
updated: 2026-10-22T10:00:00Z
---

Description with **Markdown**.

```markdown
## Notes
```

## Acceptance criteria

A non-task-list line stays here.
- [ ] First criterion
  continuation
* [X] Second criterion

~~~md
## Plan
- [ ] Not a criterion
~~~

## Definition of done

- [ ] CI passes

## Plan

Old plan.

## Notes

Old note.

## Comments

### malformed header

Unowned preamble is retained.

### 2026-10-22T09:10:00Z @brett

Original comment.

### not-a-timestamp @someone

Part of the original comment.

```md
### 2026-10-22T09:11:00Z @fake
```
