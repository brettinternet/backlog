---
id: BL-44
title: Ship release builds
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
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

Port hum's release pipeline (`.github/workflows/release.yaml`, `install.sh`, `scripts/install_test.sh`):

- Triggered by a tag. Fails closed unless the tag commit's CI run passed.
- git-cliff release notes.
- `CGO_ENABLED=0` builds for darwin and linux on arm64 and amd64, with checksums.
- An install script that verifies the checksums.
- `task release:snapshot` runs a local dry run.

Pushing the first tag and updating the Homebrew tap (`brettinternet/homebrew-tap`) are owner steps. Document them as a checklist.
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
