# BL-38 live verification

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
