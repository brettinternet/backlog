---
id: BL-2
title: Define the item model and the native format codec with minimal-diff edits
status: done
type: feature
priority: high
assignees: ["@brett"]
labels: [model, format, reviewed]
milestone: m-0
blocked_by: [BL-1]
rank: a0a0a0a0a0a0a0a0a
refs: [docs/format.md, docs/design.md]
files: [internal/model, internal/format, docs/format.md, go.mod, go.sum]
created: 2026-10-22T00:50:00Z
updated: 2026-10-30T05:43:22Z
---

## Why

The native format is the primary backend and the only one `bl` fully owns (D4). Git-friendly, minimal-diff writes are the core promise (D7, D8). Every source and command uses the shared model (§4.2, D10, D11).

## What

- `internal/model`: Item, Ref, StatusCategory, Priority, typed edges, Capabilities, and Mutation. A Mutation sets, adds, removes, or unsets a field; replaces a section; appends a note or comment; or checks, unchecks, adds, or removes a criterion. Every Mutation has an Inverse.
- `internal/format`: parse a file into a document that keeps the original bytes, expose the Item view, apply a Mutation as a minimal byte edit, and serialize new items canonically. Implement everything in docs/format.md: sections, code fences, criteria numbering, comments, CRLF, and unknown keys kept byte-for-byte.
- Golden tests in `internal/format/testdata` and fuzz targets for parse and for parse, edit, parse.
- Freeze docs/format.md: set its status to Frozen v1 and fold in any decision made while implementing.

## Acceptance criteria

- [x] #1 Golden tests show each edit kind (scalar set, list add and remove, unset, section replace, note append, comment append, criterion check, add, and remove) changes only the expected lines; unknown keys, YAML comments, quoting, list style, and CRLF stay byte-identical
- [x] #2 `go test -run '^$' -fuzz FuzzParse -fuzztime 60s ./internal/format` and the round-trip fuzz target run without failures, and a canonical file parses and serializes byte-identically
- [x] #3 Headings inside code fences, duplicate sections, non-task-list lines in criteria, and malformed comment headers behave as docs/format.md states, each covered by a golden case
- [x] #4 A table test proves that applying any Mutation and then its Inverse restores the original bytes apart from `updated`
- [x] #5 docs/format.md says Frozen v1 and matches the implementation

## Definition of done

- [x] #1 task ci passes on the final commit
- [x] #2 Every checked acceptance criterion has an AC#N evidence line in Implementation Notes naming the command and its result
- [x] #3 Substantial changes had an independent verifier pass that returned PASS for every acceptance criterion
- [x] #4 The diff stays within the task's modified-file list, or Implementation Notes justify the deviation
- [x] #5 No test was deleted, skipped, or weakened
- [x] #6 User-visible behavior changes are reflected in README.md or docs/ in the same change

## Plan

1. Read all of docs/format.md and design.md §4.2. Implement the shared model and document parser retaining original bytes and section/key spans; do not reserialize untouched YAML.
2. Implement canonical creation, minimal splices, section/comment/criterion edits, and inverses capturing original representation, not just normalized values. Treat updated as the sole permitted inverse difference.
3. Extend golden fixtures for each edit kind, CRLF, unknown YAML, fences, malformed input, and duplicate sections. Add parser/round-trip fuzz targets; run task test and each fuzz target separately for 60s (use an exact anchored target name). Freeze docs/format.md only after reconciling the fixtures.
4. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and hand off implementation delivery before making dependent work eligible. Do not push or publish without explicit authorization.

5. Deliver the verified implementation by local merge to main and remove the session-owned worktree; commit task metadata separately in the primary checkout.

## Notes

A YAML AST that keeps comments (for example `github.com/goccy/go-yaml`) can locate keys, but writes must be line-level splices so untouched bytes never change. Validation of statuses against config belongs to BL-9; this task parses and edits only.

Implemented shared model, retained-byte native document parsing, canonical creation, all mutation kinds, and version-bound byte-exact inverses. Golden fixtures cover LF/CRLF edits, fenced headings/entries/comment headers, malformed comment headers, duplicate sections, and canonical stability. FuzzRoundTrip exposed bare-CR YAML line offsets; fixed by requiring LF/CRLF in frontmatter and retained testdata/fuzz/FuzzRoundTrip/b5db5b12057972a4. task ci passes pre-commit. Independent verifier acceptance pass in progress. Reused existing go.yaml.in/yaml/v3 dependency; no module changes.

AC#1: task cli:test PKG=./internal/format -- -run TestEditsAndInverse passed; all 17 mutation cases match reviewed LF and CRLF goldens, retaining unknown YAML, comments, quoting, and list style.
AC#2: mise exec go -- go test -run '^$' -fuzz '^FuzzParse$' -fuzztime 60s ./internal/format PASS (6,270,736 executions final run); the same command with ^FuzzRoundTrip$ PASS on merged main (1,541,737 executions). task test also passed TestCanonical byte-stable parse/serialize.
AC#3: task test passed TestBodyGrammar and its body.txt/duplicate.txt goldens: fenced headings/entries/comment headers ignored, duplicate sections rejected, non-entry lines retained, malformed comment headers remain in comment body or preserved preamble.
AC#4: task test passed TestEditsAndInverse: every operation plus Restore/redo restores exact original bytes when the original updated clock is supplied; TestConflictAndValidation rejects stale inverses. Both LF and CRLF covered.
AC#5: task ci passed with docs/format.md Frozen v1; independent verifier b2502fde-ba4a-4f91-9579-d9e5f5cb1802 returned PASS for all five criteria and no blocking defect after checking codec/spec behavior.
Delivery: implementation commit 36ff6a9; merge commit cfb2d23 incorporates concurrent main documentation changes without losing their milestone/label or version-guard guidance. Fast-forwarded primary main to cfb2d23 and task ci passed there (security, checks, tests, race, smoke). task check:staged passed before commits. No tests removed, skipped, or weakened. All implementation paths are in the declared scope; backlog metadata is maintained separately through the CLI.
Cleanup: verified session creation receipt (session 01a16177-9a4e-729e-88c1-f468a8f76d5c), Worktrunk listing, and shell-only pane wHP:p1. wt remove bl-2-native-codec --foreground --format=json removed the checkout and merged branch. herdr workspace list confirmed matching workspace wHP disappeared; git worktree list shows only primary. No retained checkout/workspace, blockers, or next resumable step.

Review (6fcd2be): fixed codec writes that silently moved content: section/note/comment/serialized text adding, hiding (unclosed fence), or forging sections/comments is now rejected; empty section replacement removes the heading; new sections at EOF and empty-description files no longer leave stray blank lines; added criteria must be single-line. Regression cases in TestConflictAndValidation; format.md Writing updated. task ci PASS.

## Summary

Implemented the shared item model and native v1 codec with minimal edits, canonical creation, and version-bound exact undo. Golden tests, both 60-second fuzz targets, independent acceptance verification, and task ci pass. Delivered through 36ff6a9 and cfb2d23 on main; removed the session-owned worktree, branch, and Herdr workspace.
