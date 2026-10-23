---
# Project metadata stays byte-identical.
id: TASK-007
title: 'Original: title' # title comment
status: To Do
assignee:
  - '@alice' # keep owner
  # An independent list comment.
  - '@bob'
created_date: '2026-01-01 10:00'
updated_date: '2026-01-02 12:30'
due_date: '2026-12-01'
labels: [one, 'two'] # keep label comment
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
- [ ] #1 First criterion
- [x] #2 #12 is user text
~~~md
- [ ] #9 fenced example
~~~
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 Verified
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
Keep the plan.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Keep the notes.
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
<!-- COMMENTS:END -->
