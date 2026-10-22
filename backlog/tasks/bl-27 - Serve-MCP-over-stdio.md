---
id: BL-27
title: Serve MCP over stdio
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
labels:
  - mcp
  - agents
milestone: m-2
dependencies:
  - BL-8
  - BL-13
references:
  - docs/design.md
modified_files:
  - internal/mcp
  - internal/cli
  - docs/mcp.md
  - go.mod
  - go.sum
priority: medium
type: feature
ordinal: 27000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

`bl mcp` serves the §7 tools over stdio with `github.com/modelcontextprotocol/go-sdk`: `next`, `query`, `show`, `create`, `update`, `append`, and `criteria`. BL-31 adds `action`, and BL-32 adds `claim` and `release`.

- Tools share types and validation with the CLI and map errors to the same reasons.
- `bl instructions` is exposed as a resource.
- docs/mcp.md shows setup for pi and Claude Code.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 An in-process MCP client test exercises each tool's success path and one reason-coded error
- [ ] #2 A parity test fails when a CLI write option has no MCP equivalent
- [ ] #3 A subprocess test runs initialize, tools/list, and tools/call over stdio
- [ ] #4 docs/mcp.md documents every tool and its setup
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
