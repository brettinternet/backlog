---
id: BL-2
title: Define the item model and the native format codec with minimal-diff edits
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 01:45'
labels:
  - model
  - format
milestone: m-0
dependencies:
  - BL-1
references:
  - docs/format.md
  - docs/design.md
modified_files:
  - internal/model
  - internal/format
  - docs/format.md
  - go.mod
  - go.sum
priority: high
type: feature
ordinal: 2000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

The native format is the primary backend and the only one `bl` fully owns (D4). Git-friendly, minimal-diff writes are the core promise (D7, D8). Every source and command uses the shared model (§4.2, D10, D11).

## What

- `internal/model`: Item, Ref, StatusCategory, Priority, typed edges, Capabilities, and Mutation. A Mutation sets, adds, removes, or unsets a field; replaces a section; appends a note or comment; or checks, unchecks, adds, or removes a criterion. Every Mutation has an Inverse.
- `internal/format`: parse a file into a document that keeps the original bytes, expose the Item view, apply a Mutation as a minimal byte edit, and serialize new items canonically. Implement everything in docs/format.md: sections, code fences, criteria numbering, comments, CRLF, and unknown keys kept byte-for-byte.
- Golden tests in `internal/format/testdata` and fuzz targets for parse and for parse, edit, parse.
- Freeze docs/format.md: set its status to Frozen v1 and fold in any decision made while implementing.

## Notes

A YAML AST that keeps comments (for example `github.com/goccy/go-yaml`) can locate keys, but writes must be line-level splices so untouched bytes never change. Validation of statuses against config belongs to BL-9; this task parses and edits only.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Golden tests show each edit kind (scalar set, list add and remove, unset, section replace, note append, comment append, criterion check, add, and remove) changes only the expected lines; unknown keys, YAML comments, quoting, list style, and CRLF stay byte-identical
- [ ] #2 `go test -run '^$' -fuzz FuzzParse -fuzztime 60s ./internal/format` and the round-trip fuzz target run without failures, and a canonical file parses and serializes byte-identically
- [ ] #3 Headings inside code fences, duplicate sections, non-task-list lines in criteria, and malformed comment headers behave as docs/format.md states, each covered by a golden case
- [ ] #4 A table test proves that applying any Mutation and then its Inverse restores the original bytes apart from `updated`
- [ ] #5 docs/format.md says Frozen v1 and matches the implementation
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 task ci passes on the final commit
- [ ] #2 Every checked acceptance criterion has an AC#N evidence line in Implementation Notes naming the command and its result
- [ ] #3 Substantial changes had an independent verifier pass that returned PASS for every acceptance criterion
- [ ] #4 The diff stays within the task's modified-file list, or Implementation Notes justify the deviation
- [ ] #5 No test was deleted, skipped, or weakened
- [ ] #6 User-visible behavior changes are reflected in README.md or docs/ in the same change
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Read all of docs/format.md and design.md §4.2. Implement the shared model and document parser retaining original bytes and section/key spans; do not reserialize untouched YAML.
2. Implement canonical creation, minimal splices, section/comment/criterion edits, and inverses capturing original representation, not just normalized values. Treat updated as the sole permitted inverse difference.
3. Extend golden fixtures for each edit kind, CRLF, unknown YAML, fences, malformed input, and duplicate sections. Add parser/round-trip fuzz targets; run task test and each fuzz target separately for 60s (use an exact anchored target name). Freeze docs/format.md only after reconciling the fixtures.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
