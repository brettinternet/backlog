# OAuth

| Provider | Login | Status |
| --- | --- | --- |
| Linear | Authorization code, S256 PKCE, loopback callback | Available ([setup](linear.md#oauth-login)) |
| GitHub (github.com) | Device flow with separate read and write GitHub Apps | Available ([setup](github.md#oauth-login)) |
| Jira Cloud | — | Excluded: 3LO needs a client secret. Use a helper or API token. |
| External adapters | — | The adapter owns authentication ([protocol](external-adapter-protocol.md#compatibility-obligations)) |
| Native, Backlog.md, Beads | — | Local; no auth |

OAuth is opt-in per account, in user config only:

```yaml
accounts:
  linear:
    - name: work
      user: VIEWER_UUID
      oauth: true
```

`oauth: true` excludes `credential` and `env`, and never falls back to them.
Other providers keep using a helper or environment variable. There's no
user-configurable endpoint, client, or secret; an unsupported provider fails
with `oauth-provider-unavailable` before contacting anything.

## Lifecycle

```sh
bl auth login linear work          # read
bl auth login linear work --write  # read, then a separate write grant
bl auth logout linear work
bl auth status linear work         # offline: grant, scopes, expiry
```

- **Login** needs a terminal. It always gets read access first and verifies it;
  `--write` then asks for write separately (on GitHub, by authorizing a second
  App). MCP, hooks, actions, and claims never start a login.
- **Storage:** tokens and granted scopes live in owner-only XDG state (0700
  directory, 0600 records written atomically and fsynced; on Windows, a
  protected DACL granting only the current user). Never in config,
  cache, argv, logs, events, MCP output, hooks, or actions.
- **Refresh** starts a minute before expiry, under a per-account cross-process
  lock. The rotated pair is stored durably before the lock is released, then the
  immutable user ID (and source binding) is rechecked. A mismatch disables the
  affected sources. Capabilities are rediscovered after consent changes.
- **Logout** deletes local tokens and the account's cached rows, revokes
  remotely where a public client can (Linear), and otherwise tells you to revoke
  in provider settings (GitHub).

## Errors

| Reason | Meaning |
| --- | --- |
| `authentication-required` | No grant yet; run `bl auth login` |
| `token-expired` | The access token expired and couldn't refresh; log in again |
| `grant-revoked` | The grant was revoked; log in again |
| `insufficient-scope` | The grant lacks permission; use `--write` or fix App permissions |
| `principal-mismatch` | The token belongs to someone else; the source is disabled until fixed |
| `sso-required` | The organization requires SSO or SAML authorization for the app |
| `rate-limited` | Retry after the reported time |
| `provider-unavailable` | The provider is down, or its response couldn't be classified |
| `oauth-provider-unavailable` | No OAuth flow exists for this provider |
| `oauth-not-opted-in` | The account lacks `oauth: true` |
| `credential-store-unavailable` | The token store can't be used; check its permissions and free space |
| `interactive-login-required` | Login needs a terminal |

Messages never include provider response bodies or tokens. Workspace and team
binding is checked separately from token validity. A broader earlier grant is
never accepted as read-only consent.

## Registrations

**Linear:** public client ID `6eaa52fb21a79f49ba564d31ee0bab91`, callback
`http://127.0.0.1:47653/oauth/callback`, approved by the owner. Login requests
`read`, then `write` after explicit consent. Granted scopes come from the token
response (including the older array form). No actor/bot auth, client
credentials, webhooks, or custom redirects.

**GitHub:** `bl-cli-read` (`Iv23lixJ0srsgnPbExrP`) and `bl-cli`
(`Iv23liwvINakcZA9rAst`), approved by the owner. Two Apps are needed because
GitHub can't downscope a write App's user token. Both enable device flow and
expiring user tokens, with OAuth-during-installation off. Neither uses
Contents, enterprise permissions, webhooks, a callback server, a client secret,
or a private key. A private registration may limit who can install it. The
stored `read`/`write` label selects the App on refresh; it isn't a GitHub scope.

Fake authorization servers test both flows. No live user login has been
recorded.

## Provider evidence

Checked against official documentation on **2026-10-23** (BL-59). Recheck
expiry, refresh, and scope behavior when integrating a provider or an Enterprise
version.

| Candidate | Secret-free native flow | Read first; writes only after opt-in | Lifetime and refresh | Revocation | Principal verification |
| --- | --- | --- | --- | --- | --- |
| Linear | **Yes:** authorization code with PKCE; no secret at exchange or refresh for PKCE tokens [L1]. Use S256, random state, `actor=user`. | `read`; then `comments:create` for comments only, or `write` for issue updates. No `admin` [L1]. | Access 24 h; refresh returns a new pair. A lost refresh response can be replayed for 30 min. Refresh-token lifetime isn't documented. | `POST https://api.linear.app/oauth/revoke` with the token in the form body [L1]. | GraphQL `viewer { id }` [L2], plus organization and team binding, by UUID. |
| GitHub OAuth app | **Yes,** device flow without a secret [G1], but scopes are coarse. | No scope reads public data; `read:project` reads Projects. `repo` and `public_repo` include writes [G2], so no private read-only scope exists. | Expiring tokens: 8 h, refresh 6 months; rotation invalidates the old pair; scopes can't change on refresh [G1]. | Needs client ID **and secret** [G3]. Users revoke in settings [G4]. | `GET /user` after each token [G1]; bind the stable user ID. |
| GitHub App | **Yes,** implemented (BL-61): device-issued user tokens and refresh need no secret [G5, G6]. | Permissions come from the App registration, not scopes [G5]. Read App for reads; separate write App after opt-in. Live installation grants, user role, and `viewerCanUpdate` gate writes. | 8 h, refresh 6 months; refresh replaces the pair [G6]. Handle non-expiring tokens too. | Same secret requirement as OAuth apps [G3]; users revoke manually. | Verify the user with the new token, bind ID and login, and rediscover installation permissions; access is the intersection of App and user [G5]. |
| Jira Cloud 3LO | **No:** exchange and refresh require `client_secret` [J1]. No embedded secret or proxy. | If ever eligible: `read:jira-work`, `read:jira-user`; `write:jira-work` for writes [J2, J3]. | `expires_in`; `offline_access` enables rotating refresh; 90-day inactivity expiry [J1]. | Users revoke [J1]; no known secret-free API. | `accessible-resources` for the site, then `GET /rest/api/3/myself` and compare `accountId` [J1, J3]. |
| External adapters | **No** host method in protocol v1; the adapter authenticates [E1]. | Provider-specific. Never pass tokens through argv, config, environments, or frames. | Adapter-owned | Adapter-owned | Adapter-owned. Needs a concrete provider and a new protocol version first. |

## Jira and external adapter boundary

Checked **2026-10-23** (BL-63); runtime and protocol v1 unchanged.

| Provider | Use now | Gate for native OAuth |
| --- | --- | --- |
| Jira | [API-token helper or explicit environment account](jira.md#setup), without `oauth: true` | [3LO][J1] requires `client_secret` for both code exchange and refresh. Needs documented secret-free equivalents and an owner-approved public client; no embedded secret or proxy. |
| External | Adapter-owned private store or helper | No eligible consumer identified. Needs a concrete provider/consumer, then a new protocol major, schema and conformance fixtures. An optional capability cannot add auth RPCs. |

External v1 has no login, refresh, logout or credential-delivery RPC. Never put
credentials in `run` arguments or frames; the host strips provider credentials
from the adapter environment. New native flows must satisfy
[the integration checks below](#adding-a-provider).

[J1]: https://developer.atlassian.com/cloud/jira/platform/oauth-2-3lo-apps/

## Adding a provider

- Register an owner-approved public client. Loopback flows need S256, state
  checks, a bounded listener, and explicit consent; device flows must honor the
  polling interval, `slow_down`, denial, and code expiry.
- Reuse the account config, resolver, redaction, principal checks, token store,
  and per-account refresh lock. Persist each rotated pair before releasing the
  lock; provider grace periods don't replace serialization.
- Discover granted scopes, request writes only after opt-in, and rediscover
  capabilities afterward. Map failures to the codes above.
- Revoke remotely only through a public-client endpoint; never acquire a secret
  just to revoke.
- Test against fake servers: state and PKCE checks, login, rotation, concurrent
  refresh, denial, revocation, principal mismatch, unattended refusal, and
  canary-token leaks into config, argv, hooks, actions, logs, events, MCP, and
  cache.

## Sources

- **L1:** [Linear OAuth 2.0](https://linear.app/developers/oauth-2-0-authentication): PKCE code exchange, secret-free PKCE refresh, scopes, expiry, rotation grace period and revocation.
- **L2:** [Linear GraphQL getting started](https://linear.app/developers/graphql): OAuth Bearer authorization versus raw personal API key, and viewer identity query.
- **G1:** [Authorizing GitHub OAuth apps](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps): device flow, expiring tokens, refresh requirements and user verification.
- **G2:** [GitHub OAuth scopes](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps): public read, Projects read and coarse repository write access.
- **G3:** [GitHub OAuth application REST endpoints](https://docs.github.com/en/rest/apps/oauth-applications): Basic client ID/secret authentication for deleting tokens and grants.
- **G4:** [Reviewing authorized OAuth apps](https://docs.github.com/en/apps/oauth-apps/using-oauth-apps/reviewing-your-authorized-oauth-apps): user-managed revocation.
- **G5:** [Generating a GitHub App user access token](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app): device flow and registration-based permissions, not OAuth resource scopes.
- **G6:** [Refreshing GitHub App user access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens): token lifetimes, rotation and device-flow secret exception.
- **J1:** [Atlassian Jira Cloud 3LO](https://developer.atlassian.com/cloud/jira/platform/oauth-2-3lo-apps/): required secret, accessible sites, rotating refresh and user grant revocation.
- **J2:** [Jira OAuth scopes](https://developer.atlassian.com/cloud/jira/platform/scopes-for-oauth-2-3LO-and-forge-apps/): classic and granular permissions; scopes do not override user permissions.
- **J3:** [Jira current user API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-myself/#api-rest-api-3-myself-get): current `accountId` and required scopes.
- **E1:** [External adapter protocol v1](external-adapter-protocol.md#compatibility-obligations) and [schema](external-adapter/v1/schema.json): adapter-managed private authentication, no host credential method.
