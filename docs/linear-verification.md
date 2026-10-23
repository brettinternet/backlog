# BL-39 Linear verification

## Local checks

Passed:

```sh
task cli:check
task cli:test PKG='./internal/source/linear ./internal/source/github ./internal/source/remote ./internal/project ./internal/engine ./internal/worklease ./internal/cli' -- -count=1
task cli:test PKG='./internal/source/linear ./internal/source/remote' -- -race -count=1
gopls check internal/source/linear/linear.go internal/source/linear/write.go internal/source/linear/linear_test.go internal/source/linear/live_test.go internal/engine/read.go internal/project/settings.go internal/source/remote/scheduler.go internal/source/remote/scheduler_test.go internal/cli/doctor.go
git diff --check
task ci
```

The full CI gate passed after integration correction, including security, static
checks, documentation examples, all tests, race tests and CLI smoke checks.
The first gate caught a missing README link to this verification document; that
link was added before the passing run.

Independent verification passed all four acceptance criteria and repeated the
live round trip. It also found that synchronous engine reads rejected partial
Linear observations. The correction keeps partial coverage honest while allowing
fresh rows in list/next/GoTo; stale rows remain unready and stale or missing
blockers stay unknown. Public engine regressions exercise partial reads, fresh
claimed selection, held-claim resolution, archive filtering, workspace staleness,
and rejection of unknown coverage, error findings and duplicate identities.
Targeted independent re-verification returned **PASS** for that correction and
confirmed that all four acceptance-criterion PASS results remain applicable.

The fake HTTPS suite covers 251 issues across capped 250-item pages, recovery of
an issue actually omitted between moving cursor pages, and independent directional
relation reconciliation with unchanged `updatedAt`. It checks stale/missing version
conflicts, write capability and principal/organization gates, all five supported
writes, no replay after uncertain dispatch, incomplete pagination/relation metadata,
and organization/issue UUID-based claim identity. Shared remote tests cover raw
personal-key authorization and both request/complexity millisecond quota resets.
Existing GitHub tests continue passing.

## Authorized live round trip — PASS

Owner authorized disposable synthetic create/edit/link/comment/delete operations
in Linear team **Test**, using the private Keychain helper
`[security, find-generic-password, -s, linear-api, -w]`. No credentials were printed,
written to files, or included in configuration/evidence. The initial access attempt
failed before any provider operations; the owner made the Keychain entry available
before live creation resumed.

Final command:

```sh
BL_PROVIDER_TESTS=1 BL_LINEAR_TEST_TEAM=Test \
  task cli:test PKG=./internal/source/linear -- -run '^TestLiveRoundTrip$' -count=1 -v
```

Result: `PASS`, package completed in 15.166 seconds. The round trip verified state,
assignee (including clear), priority, labels (including clear), comment creation,
independent blocks reconciliation and rejection of the stale pre-relation version.
The final source uses current Linear scalar types `ID!` for label filter IDs and
`DateTimeOrDuration!` for the overlap boundary, confirmed against live schema.

Verified scope:

- Organization: `0bfebf80-70af-4eca-9e39-2029a01f5b77`
- Test team: `d19193f6-0501-485b-93af-65e829c2039d`

Final run artifacts and cleanup receipts:

| Artifact | ID | Cleanup |
| --- | --- | --- |
| Issue TEST-7 | `89c055bd-139b-4aa8-b780-2851dd6b17eb` | Delete success; detail verified absent/trashed |
| Issue TEST-8 | `3c132ba3-6c9d-4e3b-bdc0-025e2166e295` | Delete success; detail verified absent/trashed |
| Comment on TEST-7 | `360f05d0-650c-4958-bb67-cb030713318a` | Explicit commentDelete success |
| Blocks TEST-8 → TEST-7 | `8fcf0de1-6d52-477e-aaa5-babce8994141` | Explicit issueRelationDelete success |

Earlier schema-validation attempts also cleaned every created artifact:

| Run | Issues (delete success, verified absent/trashed) | Relation (explicit delete success) | Comment (removed with issue deletion) |
| --- | --- | --- | --- |
| Before overlap scalar correction | TEST-3 `cf4225f1-c3ac-400e-952e-2b3e2d0645fe`; TEST-4 `a5151cea-dcc7-4728-b245-f995642e6ed9` | `64a23180-73ec-41b5-86c3-95f1f8756960` | `6ef46444-4627-4a99-a101-5509264df780` |
| First complete PASS | TEST-5 `e56386f5-2143-439e-9065-0a2fbc9cd56e`; TEST-6 `3805a1a0-e69b-40f6-bf24-4a60350c55c7` | `632e9227-0928-4e50-a9d7-65695b3e71b3` | `45138f6c-47db-4847-b22a-a0b9703ef610` |

Independent verifier run: `PASS` in 18.34 seconds. TEST-9 and TEST-10 were
deleted and verified absent/trashed. Relation
`d1397eef-bf77-413e-b49e-69699d9f6100` and comment
`45d6a660-7340-4859-a123-f2b9b54d0d46` were explicitly deleted.

No existing issues, labels, states or team configuration were modified. No live
cleanup is outstanding. Linear's issue deletion may retain trashed records under
its normal retention policy; the test does not administer the trash or permanently
destroy provider history.

## Implementation scope

`internal/source/linear` contains the adapter and fake/live tests. Directly required
wiring extends `internal/engine/read.go` (trusted registry and shared cache),
`internal/project/settings.go` and the project schema (default-false `allowWrites`),
`internal/cli/doctor.go` (recognize the now-supported provider), and the shared remote
scheduler (personal-key raw auth and Linear millisecond quotas, with regression).
Shared engine read/GoTo/claimed-selection/workspace paths and the remote wrapper
also propagate stale/archive metadata while admitting valid partial observations;
engine regressions protect this integration and fail-closed malformed inputs.
Existing Worklease `linear` key derivation already matched; no coordinator code
change was needed. Its organization/UUID identity is exercised through the adapter.

D15 documents the same non-CAS limitation for Linear as GitHub. Full lightweight
issue enumeration plus the tagged 30-day overlap avoids nested 250×250 complexity;
every final issue receives a detail read and independent relation scans on the
worker. Conservative partial coverage never proves deletion. `allowWrites` is a
trust-bound opt-in, not proof of API-key scope; the provider remains authoritative.
