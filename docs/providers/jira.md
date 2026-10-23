# Jira Cloud probe (BL-40)

Observed on 2026-10-23, 15:38–15:40 UTC, against the owner-approved
`https://brettinternet.atlassian.net` site. The owner authorized all test
operations. The sole discovered project was `KAN` (10000), a team-managed
software project. Only synthetic issues were changed. This is evidence for
BL-41, not an announcement that the Jira adapter is available. Jira Data Center
is excluded.

## Authentication

HTTP Basic authentication with the account email and API token succeeded on
`GET /rest/api/3/myself` (200). The token was obtained privately from macOS
Keychain service `jira-api`, using `security find-generic-password -s jira-api
-w` as an argv helper. It stayed in process memory, not argv, config, or files;
redirects were disabled. The response verified the accountId of the test user.
No OAuth flow or client secret is needed for these probes.

The token's exact scopes and minimum required scopes were **not measured**.
BL-41 must document scope requirements separately; successful access here is
not proof of a least-privilege scope set. Do not put the token in project config
or ask for it in chat. Reuse the account helper/redaction boundary in
[remote.md](../remote.md).

## Observations and unknowns

| Behavior | Observed result | Limit / adapter consequence |
| --- | --- | --- |
| Search pagination and order | `POST /rest/api/3/search/jql`, `maxResults: 1`, explicit `ORDER BY id ASC`, returned ascending numeric IDs through opaque `nextPageToken` values. The final nonempty page still had `isLast: false`; a third, empty page had `isLast: true`. | Follow tokens through the terminal page, not a guessed total or page length. Concurrent edits, token expiry, and snapshot isolation are unknown. |
| Link addition | `POST /rest/api/3/issueLink` with type `Blocks` returned 201. Both issues' `updated` timestamps advanced. | Fetch directional links, not only linked issue keys. One workflow/site does not prove all link mutations always advance timestamps. |
| Link removal | `DELETE /rest/api/3/issueLink/{id}` returned 204. Both timestamps advanced again, and both `issuelinks` arrays became empty. | Include links in version fingerprints; do not assume `updated` alone captures every relation change. |
| Project move | **Unknown**: no cross-project move was performed; discovery returned only one project. No second project or workflow was created or reconfigured. | Use the numeric issue ID, never the movable display key. BL-41 must exercise this policy with a move vector; this probe does not establish live move/alias behavior. |
| Deletion | Issue DELETE returned 204; subsequent authenticated numeric-ID GET returned 404 with `Issue does not exist or you do not have permission to see it.` Reconciled search returned an empty terminal result. | A 404 alone does not distinguish deletion from lost visibility. |
| Visibility / permission loss | Before deletion, anonymous GET of an existing issue returned the same 404 message, while anonymous search returned 200, `issues: []`, `isLast: true`. | Permission revocation for the *same authenticated principal* was not performed and remains **unknown**. Empty results prove only visible scope, not global deletion. |
| Transition discovery | `GET /rest/api/3/issue/{id}/transitions?expand=transitions.fields` returned To Do (11), In Progress (21), In Review (31), Done (41), each with `fields: {}` and `hasScreen: false`. | Discover per issue; these IDs are not portable workflow constants. Required-field workflows remain **unknown**. |
| Transition writes | POST with transition 0 returned 400 (`Transition id '0' is not valid for this issue.`); discovered transition 41 returned 204. A reread showed status Done / category `done`, resolution `null`, and advanced `updated`. | Preserve raw status, category, and nullable resolution separately. Unsuccessful resolutions and server CAS support were not measured. Never claim atomic version enforcement on this evidence. |
| Rate limits | Successful authenticated responses included `Ratelimit-Policy`, `Ratelimit`, `X-Ratelimit-Limit`, and `X-Ratelimit-Remaining`. Example search: `"jira-burst-based";q=100;w=1`, `"jira-burst-based";r=199;t=1`, limit 200, remaining 199. | 429 bodies, `Retry-After`, reset headers, and exhaustion behavior are **unknown**. No disruptive traffic was used to induce limits. Do not infer reset time from remaining count. |
| ADF comments | POST comment returned 201; GET returned 200 with a structurally identical ADF v1 document containing plain text, a strong mark, ampersand, and Unicode `café`. | Other node types, mentions, attachments, and normalization are **unknown**. |

### Search request and terminal evidence

The second run used this body, passing each returned token unchanged on the
next request:

```json
{
  "jql": "project = KAN AND labels = bl40-api-probe ORDER BY id ASC",
  "fields": ["summary", "updated"],
  "maxResults": 1,
  "reconcileIssues": [10002, 10003]
}
```

| Page | IDs | isLast | nextPageToken |
| --- | --- | --- | --- |
| 1 | 10002 | false | present |
| 2 | 10003 | false | present |
| 3 | none | true | absent |

All three responses were 200. No `total` was returned. The bounded probe used
`reconcileIssues` for its known fresh IDs; indexing latency without reconciliation
was not measured. This tiny reconciled query does not establish completeness
for arbitrary JQL or an estimated count. Follow the shared complete/partial/
unknown contract; an estimate is partial evidence, never proof of deletion.

### Link timestamp evidence

First run, `fields.updated` (site offset -0600):

| Issue | Before link | After link | After unlink |
| --- | --- | --- | --- |
| 10000 / KAN-1 | 09:39:39.500 | 09:39:41.523 | 09:39:42.929 |
| 10001 / KAN-2 | 09:39:40.386 | 09:39:41.547 | 09:39:42.945 |

With `inwardIssue: {id: "10000"}` and `outwardIssue: {id: "10001"}` in
the create-link body, issue 10000 exposed `outwardIssue: 10001` and issue
10001 exposed `inwardIssue: 10000`. The type declared outward `blocks` and
inward `is blocked by`. Thus KAN-1 blocked KAN-2. Dependency mapping must retain
both configured type and direction.

## Identity and versions for BL-41

- Stable source identity: the configured canonical HTTPS site origin, e.g.
  `https://brettinternet.atlassian.net` (lowercase host, no trailing slash).
  Do not include a project key, JQL, saved filter, account, or display name.
  Site-domain migration is not proven stable and requires explicit handling.
- Stable item key: the decimal numeric issue `id`, e.g. `10000`. `KAN-1` is
  display/navigation metadata only. Derive claims using Worklease's `generic`
  policy with that site as source and numeric ID as item, and expose the result
  from `bl show`; clients must not reconstruct it. A project move must not
  rekey the source or item.
- Version policy: use a deterministic composite fingerprint of the fresh
  adapter-visible content, including directional link IDs/targets, raw status,
  resolution, assignee, and comment state used by edits. `updated` is useful
  evidence, not a proven universal version or CAS token. Reread before writes;
  reject observed conflicts and do not retry uncertain mutations. Unless BL-41
  establishes server CAS, document the remaining preflight-to-write race.

## Cleanup inventory and verification

Executed with Python's standard-library HTTPS client and the private Keychain
helper (`python3 /tmp/backlog-jira-run.py`); no credential was printed. Two
bounded runs created the following inventory:

| Issue | Numeric ID | Created | DELETE | Subsequent authenticated GET |
| --- | --- | --- | --- | --- |
| KAN-1 | 10000 | 201 | 204 | 404 |
| KAN-2 | 10001 | 201 | 204 | 404 |
| KAN-3 | 10002 | 201 | 204 | 404 |
| KAN-4 | 10003 | 201 | 204 | 404 |

The first run ended at 15:39:48 UTC and the second at 15:40:20 UTC.
Each run's final `POST /rest/api/3/search/jql` for its two numeric IDs, with
`reconcileIssues` containing those IDs, returned 200, `issues: []`, and
`isLast: true`. Comments 10000 and 10001 were children of deleted issues;
links 10000 and 10001 had already been explicitly removed (204). No synthetic
issue remains, and no existing issue, project configuration, or permissions
were changed. Issue-number allocation is not rolled back by deletion.

The one-off probe scripts are not a supported CLI or an automated test fixture.
A future live rerun must be explicitly gated by `BL_PROVIDER_TESTS=1` and renewed
owner approval. Default BL-41 tests must use fake servers, covering the unknowns
without treating this site's workflow as universal.
