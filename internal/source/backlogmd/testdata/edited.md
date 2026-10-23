---
# Project metadata stays byte-identical.
id: TASK-007
title: 'Edited: title' # title comment
status: To Do
assignee:
  - '@alice' # keep owner
  # An independent list comment.
  - '@bob'
created_date: '2026-01-01 10:00'
updated_date: '2026-10-22 10:00'
due_date: '2026-12-01'
labels: [one, 'two', three] # keep label comment
milestone: Release
dependencies: [TASK-006]
references:
  - docs/design.md
documentation: ['https://example.test/spec']
modified_files: [internal/example.go]
priority: high
type: bug
ordinal: 100
custom:
  enabled: true # preserve nested metadata
  values: [x, 7]
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Keep this prose, including - [ ] #9 not-a-criterion.
<!-- SECTION:DESCRIPTION:END -->

## Migration Detail

Unknown Markdown remains untouched.

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 #12 is user text
~~~md
- [ ] #9 fenced example
~~~

- [ ] #2 Third criterion
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 Verified
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
New plan.

More detail.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Keep the notes.

New note.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Keep the summary.
<!-- SECTION:FINAL_SUMMARY:END -->

## Comments

<!-- COMMENTS:BEGIN -->
author: alice
created: 2026-01-02 03:04
---
Original comment.
---

author: @agent
created: 2026-10-22 10:00
---
New comment.
---
<!-- COMMENTS:END -->
