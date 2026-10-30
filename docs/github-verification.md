# GitHub live verification

## BL-38 live verification

Authorized target: `brettinternet/backlog`; helper: `gh auth token` for
`brettinternet`, refreshed with `project` scope. No token values were recorded.
Only synthetic artifacts created for this task were mutated. Existing projects
#3 and #4 and all their items were untouched.

Disposable project created by `gh project create --owner brettinternet --title
bl-provider-test-BL38-20261023 --format json`:

- Number: **7**, ID: `PVT_kwHOAT55tM4BoU9u`
- Status field: `PVTSSF_lAHOAT55tM4BoU9uzhm1lzM`
- Todo option: `f75ad846`; active option: `47fc9ee4`

Successful command:

```sh
BL_PROVIDER_TESTS=1 \
BL_GITHUB_TEST_REPO=brettinternet/backlog BL_GITHUB_TEST_USER=brettinternet \
BL_GITHUB_TEST_PROJECT_NUMBER=7 BL_GITHUB_TEST_PROJECT_ID=PVT_kwHOAT55tM4BoU9u \
BL_GITHUB_TEST_FIELD_ID=PVTSSF_lAHOAT55tM4BoU9uzhm1lzM \
BL_GITHUB_TEST_TODO_OPTION=f75ad846 BL_GITHUB_TEST_ACTIVE_OPTION=47fc9ee4 \
BL_GITHUB_TEST_LABEL=bug \
task cli:test PKG=./internal/source/github -- -run TestLiveRoundTrip -count=1 -v
```

Result: **PASS**, 34.15 seconds. Observed project Todo → active, label `bug`,
assignee `brettinternet`, appended comment and completed issue close through the
adapter. IDs and cleanup logs:

| Artifact | Created ID | Cleanup |
| --- | --- | --- |
| Issue #5 | `I_kwDOVS6Ow88AAAABZRpLxQ` | `deleteIssue` succeeded |
| Project entry | `PVTI_lAHOAT55tM4BoU9uzhC42JU` | `gh project item-delete` succeeded |
| Comment | `6356464185` | REST DELETE succeeded |

An earlier test run exposed eventual consistency immediately after the initial
project field edit. The test now polls observable read-back under a bounded
context, without sleeps or mutation retries. Its failed-run artifacts were also
cleaned: issue #4 `I_kwDOVS6Ow88AAAABZRo4BQ`, project entry
`PVTI_lAHOAT55tM4BoU9uzhC414E`.

Final independent cleanup checks:

- `gh project item-list 7 --owner brettinternet --format json` returned
  `{"items":[],"totalCount":0}`.
- GraphQL lookups of both issue IDs returned `null` with `NOT_FOUND`.
- `gh project delete 7 --owner brettinternet` succeeded.
- GraphQL lookup of `PVT_kwHOAT55tM4BoU9u` returned `null` with `NOT_FOUND`.

The project, issues, comments and project entries no longer exist. The null-node
verification commands intentionally return gh exit status 1 for `NOT_FOUND`;
these are positive cleanup evidence, not failed cleanup operations.

## BL-119 live verification

Same target and helper. Disposable project `bl-provider-test-BL119-20261027`:
number **8**, ID `PVT_kwHOAT55tM4BpBM0`, status field
`PVTSSF_lAHOAT55tM4BpBM0zhneqDY`, options as above, label `bug`.

Result: **PASS**, 78.10 seconds. Adds title, description, `labels-=`/`+=` and
`assignees-=`/`+=` to the BL-38 writes. Issue #7 `I_kwDOVS6Ow88AAAABaZYC-w`,
project entry `PVTI_lAHOAT55tM4BpBM0zhD_wss` and comment `6451834800` were
deleted by the test.

A first run (issue #6 `I_kwDOVS6Ow88AAAABaZXWpw`, cleaned up by the test) failed
with `conflict`: the composite version kept moving after a write's value
appeared. The test now waits for two equal consecutive reads before the next
write. Afterward the project was empty and deleted, and GraphQL lookups of both
issues and the project returned `NOT_FOUND`.

## BL-120 live verification

Same target and helper. Disposable project `bl-provider-test-BL120-20261027`:
number **9**, ID `PVT_kwHOAT55tM4BpBZ0`, status field
`PVTSSF_lAHOAT55tM4BpBZ0zhne2Ng`, options as above, label `bug`.

A probe script created issues #8 `I_kwDOVS6Ow88AAAABaZyu7w` and #9
`I_kwDOVS6Ow88AAAABaZyvmQ`, added both to the project, and after each mutation
polled both issues' and project items' `updatedAt` every 5 seconds for up to
70 seconds. Results are in [github.md](github.md#sync): a status option change
bumped only the item, a title edit bumped both, and `addBlockedBy`,
`removeBlockedBy`, `addSubIssue` and `removeSubIssue` bumped neither.
`items(query:"updated:>=TIMESTAMP")` matched nothing; `updated:>=DATE` matched.

`TestLiveRoundTrip` then passed in 84.65 seconds, including two change-window
loads from a reconciled cursor: one saw the project-only status change, the
other the renamed, relabelled, closed issue. Issue #10
`I_kwDOVS6Ow88AAAABaZ5U3w`, project entry `PVTI_lAHOAT55tM4BpBZ0zhEANpY` and
comment `6452554788` were deleted by the test. The probe issues and the project
were then deleted, and GraphQL lookups of all three returned `NOT_FOUND`.

## BL-118 live verification

Same target and helper. Disposable project `bl-provider-test-BL118-20261027`:
number **10**, ID `PVT_kwHOAT55tM4BpBf0`, status field
`PVTSSF_lAHOAT55tM4BpBf0zhne70g`, options as above, label `bug`.

Result: **PASS**, 104.34 seconds. Adds two creates through the adapter. Child
issue #20 `I_kwDOVS6Ow88AAAABaaE60Q` was created with label, assignee, parent
and blocker #19, and project status active. The project status moved its
version, so undo closed it as not planned. Plain issue #21
`I_kwDOVS6Ow88AAAABaaFHXA` was deleted by undo through `deleteIssue`. Cleanup
deleted #19, #20 and the project entry.

Earlier runs, cleaned up by the test: #12 and #15 failed because the create's
read-back lagged the project status (the test now polls), and #16 hit the
BL-119 version drift in `assignees+=`. #17 and #18 passed before the plain
delete step was added. Afterward the project was empty and deleted, and GraphQL
lookups of all eight issues and the project returned `NOT_FOUND`.

## BL-129 live verification

Same target and helper. A probe against disposable project 12
(`PVT_kwHOAT55tM4BpCUY`) and issue #2 polled both timestamps for up to 90
seconds after each edit:

| Edit | Issue `updatedAt` | Project item `updatedAt` |
| --- | --- | --- |
| Title | Bumped | Bumped |
| Body | Bumped | Bumped |
| Add or remove a label | Bumped | Bumped |
| Add or remove an assignee | Bumped | Bumped |
| Comment | Bumped | Unchanged |
| Close or reopen | Bumped | Bumped |

A project item window alone would miss comments. So a project-only change
window also reads `issues(filterBy:{since})` in each repository the last scan
saw, kept in the cursor; an issue added to the project bumps its item, and a
new repository joins the list then. Reconciliation enumerates the project.

`TestLiveProjectOnly` against disposable projects 13–15: run 1 hit a version
conflict on the title write because reads after the comment had not settled
(the test now spaces its settle reads), runs 2 and 3 passed (33 s, 44 s). Each
read `OWNER/REPO#N`, reconciled, saw a comment-only edit in the change window,
renamed the issue and set the project status. The test deleted issues #3–#5,
and the script deleted the probe's label and each project.

## BL-128 live verification

Same target. A probe against disposable project 16 (`PVT_kwHOAT55tM4BpCfG`)
added a draft with `addProjectV2DraftIssue`, retitled it with
`updateProjectV2DraftIssue`, and converted it into issue #6 with
`convertProjectV2DraftIssueItemToIssue`. The draft edit bumped the project
item's `updatedAt` (23:05:21 to 23:05:25), so the change window sees draft
edits. The converted item kept its ID, `PVTI_lAHOAT55tM4BpCfGzhECWqo`, so the
status follow-up targets the same item. The probe deleted issue #6 and the
project.

## BL-140 live verification

`TestLiveActivity` read a closed and reopened issue in the owner's repository
with the classic `gh` token (`repo`, `project`, `read:org`, and unrelated
scopes) and passed: three entries, oldest first (`closed as completed`,
`reopened`, `closed as completed`). A direct GraphQL read with the same token
returned `ProjectV2ItemStatusChangedEvent` entries (`Ready → In progress`)
for an issue on a project board. Nothing was written.
