# Jira Cloud

Jira Cloud REST v3 is supported. **Jira Data Center is not supported.**
The [BL-40 probe](providers/jira.md) records the observed API behavior and its
limits; default tests use fake servers, not the owner's site.

## Setup

In user config (`$XDG_CONFIG_HOME/backlog/config.yaml`, normally
`~/.config/backlog/config.yaml`), select an explicit account and token helper:

```yaml
accounts:
  jira:
    - name: work
      host: https://example.atlassian.net
      user: immutable-atlassian-account-id
      email: you@example.com
      credential: [security, find-generic-password, -s, jira-api, -w]
```

`user` is the accountId returned by `GET /rest/api/3/myself`, not the email.
The helper prints one API token line. An explicit `env: JIRA_API_TOKEN` can
replace `credential`; ambient Jira credentials are never selected. The adapter
verifies the active principal on reads and immediately before writes. Tokens
and their Basic-auth encoding stay inside the shared memory/redaction boundary.

In root `backlog.yaml` (or private `backlog.local.yaml`, using the same schema
and requiring the same `bl trust` approval):

```yaml
sources:
  - id: jira
    type: jira
    site: https://example.atlassian.net
    account: work
    jql: 'project = KAN ORDER BY id ASC'
    hardLinkTypes: [Blocks]
    unsuccessfulResolutions: [Abandoned]
    allowWrites: false
```

Use `jql: 'filter = 12345 ORDER BY id ASC'` for a saved filter. JQL controls
search enumeration; explicit numeric-ID reads continue to work after an issue
moves outside that query. Hard link types accept exact type names or IDs;
`inwardIssue` is a blocker and `outwardIssue` is blocked by this issue. Other
link types are preserved as raw fields, not hard dependencies. Missing linked
issues remain unknown, not satisfied. Only configure types with this direction.

Run `bl trust --show` and approve the source with `bl trust` yourself. Changed
site, JQL, link mappings, or write settings require renewed trust.

### Token scopes

[Atlassian distinguishes scoped and unscoped API tokens](https://support.atlassian.com/atlassian-account/docs/manage-api-tokens-for-your-atlassian-account/):

- An unscoped token uses the configured site directly.
- For a scoped token, add `cloudId: YOUR-CLOUD-ID` to the source. Requests use
  `https://api.atlassian.com/ex/jira/YOUR-CLOUD-ID`; `serverInfo.baseUrl` must
  match `site`. Claim identity still uses the site, never the gateway URL.

The documented recommended classic scopes are `read:jira-user` for
[principal verification](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-myself/)
and `read:jira-work` for
[search](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-search/),
issue, link, transition and comment reads. Add `write:jira-work` only for
[transitions, assignment](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issues/)
and [comment writes](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-comments/).
Granular-scope tokens must include every scope listed for those endpoints;
this adapter does not discover or elevate grants. This is documented scope
guidance, not a live least-privilege qualification of the BL-40 token.

Jira's Browse Projects, issue security, Transition Issues, Assign Issues and
Add Comments permissions still apply. Tokens do not grant missing permissions.
No Jira OAuth login: 3LO exchange and refresh require a client secret.
Use the helper or `env` above, not `oauth: true`.
See [eligibility and reopening gates](oauth.md#jira-and-external-adapter-boundary).

## Reads and coverage

Use numeric IDs (`bl show jira:10001`), not movable keys such as `KAN-1`.
`fields.key` retains the display key; the URL follows it after a project move.
Claims use Worklease `generic` with the canonical HTTPS site origin and numeric
issue ID. Project, account, query and display-key changes never rekey a claim.
A site-domain migration needs explicit reconciliation; it is not an alias.

Search follows opaque `nextPageToken` values until `isLast: true`, including an
empty terminal page. Coverage is **partial**, with evidence: Jira returns only
visible indexed issues and offers no proven snapshot or deletion guarantee.
An estimated count never upgrades coverage. Unseen cached issues become stale;
a 404 cannot distinguish deletion from permission loss. Errors retain cached
rows but cannot make them ready or satisfy dependencies.

The raw status name is displayed. `fields.status` includes its ID and
`statusCategory`; `fields.resolution` preserves the nullable resolution. Jira
`new`, `indeterminate` and `done` map to todo, active and done. A done issue with
Won't Do, Won't Fix, Duplicate, Cannot Reproduce, Declined, Canceled or Cancelled
resolution maps to canceled (case-insensitive). `unsuccessfulResolutions` adds
site-specific names or IDs. A null resolution does not turn Done into canceled.

Requests share a serial host/account scheduler and bounded bodies. A 429 records
`Retry-After`; requests are refused until the deadline and a later refresh
retries. No mutation is automatically replayed.

## Writes

Enable `allowWrites: true` deliberately. Supported operations are one status
transition, one single-assignee replacement, or one plain-text ADF comment per
request. Multi-field atomic batches are refused before any write.

```sh
bl set jira:10001 'status=In Progress' --if-version VERSION
bl set jira:10001 assignees=ACCOUNT-ID --yes --if-version VERSION
bl set jira:10001 assignees= --yes --if-version VERSION
bl comment jira:10001 'Checked the reproduction' --author me --if-version VERSION
```

Status values are destination names from that issue's discovered transitions,
not global workflow IDs. Required-field transitions without defaults and
ambiguous destination names are not offered. The TUI status picker uses only
these discovered choices; assignment replacement is available through CLI
`--yes` or MCP `update` with `confirm: true`, not the additive TUI assignee picker.
The MCP `fields` form otherwise matches the CLI; one field is allowed per write.

Every write refreshes the principal, issue, directional links, full comments,
and discovered transitions, then compares the composite version. Observed
conflicts are rejected. Jira has no established server compare-and-swap here:
a concurrent edit can still race between preflight and dispatch. A rate-limited
or overloaded write was not applied and reports `rate-limited` or `overloaded`.
Any other failed mutation or failed post-write refresh reports `unknown-outcome`;
refresh before deciding whether to act again. Never blindly repeat a comment or transition.
