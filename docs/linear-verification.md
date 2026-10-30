# BL-39 Linear verification

## Local checks

All passed, including the full `task ci` gate:

```sh
task cli:check
task cli:test PKG='./internal/source/linear ./internal/source/github ./internal/source/remote ./internal/project ./internal/engine ./internal/worklease ./internal/cli' -- -count=1
task cli:test PKG='./internal/source/linear ./internal/source/remote' -- -race -count=1
git diff --check
task ci
```

The fake HTTPS suite covers 251 issues over 250-item pages, an issue skipped
between moving cursor pages, and relation changes with unchanged `updatedAt`. It
also covers stale and missing version conflicts, write capability and
user/organization checks, all five writes, no replay after an uncertain write,
incomplete pagination or relation data, and UUID-based claim identity. Shared
remote tests cover raw personal-key authorization and millisecond request and
complexity quota resets.

Independent verification passed all four acceptance criteria and repeated the
live run. It found that engine reads rejected partial Linear data; after the fix,
fresh rows appear in `ls`, `next`, and go-to, stale rows stay unready, and stale
or missing blockers stay unknown. Engine tests cover partial reads, claimed
selection, held claims, archive filtering, workspace staleness, and rejection of
unknown coverage, error findings, and duplicate IDs. Re-verification passed.

## Live round trip: PASS

The owner authorized disposable issues, links, and comments in Linear team
**Test**, through the Keychain helper `[security, find-generic-password, -s,
linear-api, -w]`. No credentials were printed or stored.

```sh
BL_PROVIDER_TESTS=1 BL_LINEAR_TEST_TEAM=Test \
  task cli:test PKG=./internal/source/linear -- -run '^TestLiveRoundTrip$' -count=1 -v
```

**PASS** in 15.166 s. Verified state, assignee (set and clear), priority, labels
(set and clear), comment, blocks reconciliation, and rejection of the version
from before the relation. The live schema confirmed the `ID!` label filter and
`DateTimeOrDuration!` overlap types.

Organization `0bfebf80-70af-4eca-9e39-2029a01f5b77`, team
`d19193f6-0501-485b-93af-65e829c2039d`.

| Artifact | ID | Cleanup |
| --- | --- | --- |
| Issue TEST-7 | `89c055bd-139b-4aa8-b780-2851dd6b17eb` | Deleted; verified absent or trashed |
| Issue TEST-8 | `3c132ba3-6c9d-4e3b-bdc0-025e2166e295` | Deleted; verified absent or trashed |
| Comment on TEST-7 | `360f05d0-650c-4958-bb67-cb030713318a` | `commentDelete` |
| Blocks TEST-8 → TEST-7 | `8fcf0de1-6d52-477e-aaa5-babce8994141` | `issueRelationDelete` |

Earlier schema runs were cleaned up too (issues deleted and verified, relations
deleted explicitly, comments removed with their issues):

| Run | Issues | Relation | Comment |
| --- | --- | --- | --- |
| Before the overlap type fix | TEST-3 `cf4225f1-c3ac-400e-952e-2b3e2d0645fe`, TEST-4 `a5151cea-dcc7-4728-b245-f995642e6ed9` | `64a23180-73ec-41b5-86c3-95f1f8756960` | `6ef46444-4627-4a99-a101-5509264df780` |
| First full pass | TEST-5 `e56386f5-2143-439e-9065-0a2fbc9cd56e`, TEST-6 `3805a1a0-e69b-40f6-bf24-4a60350c55c7` | `632e9227-0928-4e50-a9d7-65695b3e71b3` | `45138f6c-47db-4847-b22a-a0b9703ef610` |
| Independent verifier (PASS, 18.34 s) | TEST-9, TEST-10 | `d1397eef-bf77-413e-b49e-69699d9f6100` | `45d6a660-7340-4859-a123-f2b9b54d0d46` (deleted explicitly) |

No existing issues, labels, states, or team settings changed, and no cleanup is
outstanding. Linear may keep deleted issues in its trash under normal retention.

## BL-140 history read: PASS

`TestLiveActivity` passed in 242 s against the Test team with the Keychain
key: it created TEST-58, moved it to In Progress after the wait, read
`state Backlog → In Progress`, and deleted it. Reading history right after
quick edits returned only the creation record with no changes, which is why the
test waits.

## Scope

The adapter and its tests live in `internal/source/linear`. Supporting changes:
the trusted registry and shared cache (`internal/engine/read.go`), default-false
`allowWrites` (`internal/project/settings.go` and the schema), `bl doctor`
recognizing Linear, and the remote scheduler (raw personal-key auth and
millisecond quotas). Engine reads, go-to, claimed selection, workspaces, and the
remote wrapper carry stale and archive metadata and accept valid partial data.
Worklease's existing `linear` key already matched.

As with GitHub, there's no compare-and-swap (D15). A light full enumeration plus
the 30-day overlap avoids nested 250×250 GraphQL complexity. Partial coverage
never proves deletion. `allowWrites` is an opt-in, not proof of the key's scope.
