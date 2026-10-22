---
id: BL-44
title: Ship release builds
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 05:01'
labels:
  - release
  - tooling
milestone: m-4
dependencies:
  - BL-26
references:
  - >-
    https://github.com/brettinternet/hum/blob/main/.github/workflows/release.yaml
modified_files:
  - .github/workflows/release.yaml
  - install.sh
  - scripts
  - .taskfiles
  - docs/development.md
priority: medium
type: feature
ordinal: 44000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## What

Implement this repository's standalone release pipeline:

- Triggered by a tag. Fails closed unless the tag commit's CI run passed.
- git-cliff release notes.
- CGO_ENABLED=0 builds for darwin and linux on arm64 and amd64, with version metadata and checksums.
- An install script that verifies checksums.
- task release:snapshot runs a local, nonpublishing dry run.

Hum's release workflow and installer are optional reference material to copy and adapt, not required inputs. All resulting workflows, scripts, tests, and fixtures must live in this repository. Do not import Hum, Noon, Worklease, Tell, or another reference project as a Go module, checkout another repository in CI, call another project's release pipeline, or require a sibling checkout, release, credentials, or artifacts to build/test this project.

Pushing the first tag and optionally updating a Homebrew tap are separate owner steps. Document them as a checklist; neither is required to validate the local pipeline.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 `task release:snapshot` builds every target with version metadata and checksums
- [ ] #2 The release workflow refuses to publish unless the tag commit's CI passed
- [ ] #3 The install script verifies checksums, and its test passes
- [ ] #4 docs/development.md has the owner's release checklist
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
1. Implement the tag-CI gate, git-cliff notes, static four-target matrix, version metadata, checksums, and installer locally. Existing projects may be consulted as optional reference code; their availability must not gate this task.
2. Add task release:snapshot for local nonpublishing builds and local installer fixtures. No external project checkouts, module imports, workflows, or artifacts are required for build or tests.
3. Verify installer checksum rejection with corrupted local artifacts, and test the release gate against missing/failed/wrong-SHA CI evidence. Run task ci and document owner-only publishing and optional tap steps.
4. Record acceptance evidence and independent verification. Do not push tags, publish releases, edit other repositories or global skills, or change a tap without separate explicit authorization.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Refined per owner: other projects are optional reference code only. This task delivers a standalone release pipeline and has no migration, data import, sibling-checkout, cross-repository CI, or runtime/module dependency obligation.
<!-- SECTION:NOTES:END -->
