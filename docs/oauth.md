# OAuth qualification

## Authentication today

`bl` does not implement interactive OAuth login, logout or token storage yet.
Use user-configured [credential helpers](remote.md), [GitHub setup](github.md)
and [Linear personal API keys](linear.md). Linear's current adapter sends raw
API-key authorization; putting an OAuth token in that helper does not enable
OAuth. Jira Cloud remains pending BL-40's approved probe and BL-41's adapter.
External adapters manage their own private authentication, not host tokens.

This is BL-59's documentation-based qualification for BL-53, checked **2026-10-23**.
It is not evidence of live authorization, registered applications or production
support. D17 remains unchanged: credentials held by `bl` are memory-only. The
flows below are implementation candidates, not commands or config you can use.

## Provider evidence matrix

| Candidate | Secret-free native flow | Read first; writes only after opt-in | Lifetime and refresh | Revocation | Principal verification |
| --- | --- | --- | --- | --- | --- |
| Linear | **Eligible in documentation:** authorization code with PKCE; secret optional at code exchange and refresh for PKCE tokens [L1]. Use S256 and random state, `actor=user`, not client credentials. | `read` initially; add `comments:create` for comment-only writes or `write` for supported issue updates. No `admin` [L1]. | Access token 24 hours; refresh returns a replacement pair. Lost refresh response can replay the original request for 30 minutes. Refresh-token absolute/inactivity lifetime is not stated in L1; do not invent one. | `POST https://api.linear.app/oauth/revoke`, token in form body, optional `token_type_hint`; documented 200/400/401 outcomes [L1]. | GraphQL `viewer { id }` [L2]; also validate organization/team binding as the existing adapter does. Compare immutable UUIDs, never names. |
| GitHub OAuth app | **Eligible flow, limited read-only scope:** device authorization, explicitly enabled on app registration, no secret for token exchange or device-issued refresh [G1]. | No scope reads public information; `read:project` reads Projects. `repo` (private repositories) and `public_repo` include writes: neither is a read-only scope [G2]. Do not silently request them for read-only setup. | Expiring access tokens: 8 hours, refresh 6 months without use; `offline_access` opts a sign-in into expiry. Rotation invalidates the old pair; scopes cannot change through refresh. Non-expiring/older Enterprise responses may omit refresh tokens [G1]. | Application token/grant deletion requires client ID **and secret** [G3]; a public client cannot use it. Delete local tokens and direct the user to GitHub's authorized-app settings to revoke access [G4]; never report local deletion as remote revocation. | `GET /user` after each token acquisition [G1]; compare configured login and bind stable user ID for stored credentials. Existing adapter verifies GraphQL `viewer.login`. |
| GitHub App | **Eligible device flow, permission-upgrade design still gated:** device-issued user tokens and refresh require no secret [G5, G6]. | Fine-grained permissions belong to app registration, not OAuth scopes [G5]. A read-only installation can use Issues read and required Projects read permissions; writes require installation permission approval, not a `scope=repo` workaround. Exact registration/upgrade strategy must be qualified in BL-61. | Expiring user tokens: 8 hours, refresh 6 months; refresh replaces and invalidates the old pair [G6]. Handle actual expiry fields and disabled expiration; `offline_access` is not a resource permission. | Token/grant deletion has the same confidential app-auth requirement [G3]. User revocation remains necessary without an eligible secret-free revocation endpoint. | Verify authenticated user with the new token, bind immutable ID and login, and rediscover installation/repository permissions; user access is the intersection of app and user permissions [G5]. |
| Jira Cloud 3LO | **Not eligible under the current evidence:** documented code exchange and refresh require `client_secret` [J1]. No secret-free PKCE/device alternative is established by these sources. Do not embed a secret or add a proxy service. | If a native flow becomes eligible: classic `read:jira-work` plus `read:jira-user` for issues/current user; `write:jira-work` only for enabled writes. Check each endpoint's scopes, or choose its granular alternative [J2, J3]. | Use returned `expires_in` (J1 does not promise a fixed access lifetime); `offline_access` enables rotating refresh. Documented refresh inactivity expiry 90 days, reuse leeway 10 minutes [J1]. These do not make a secret-requiring refresh eligible. | Users can revoke their grant [J1]. A secret-free programmatic revocation endpoint is **unknown** here; do not promise one. | Discover allowed site/cloud ID with `accessible-resources` [J1], then `GET /rest/api/3/myself`, compare immutable `accountId` and configured site [J3]. Site discovery alone is not user authentication. |
| External adapters | **Not a host OAuth candidate in protocol v1:** no login or credential-delivery method; the adapter owns authentication [E1]. | Provider-specific; no generic scope contract. Never forward tokens through argv, config, action environments or protocol frames. | Provider/adapter-owned, no host refresh/rotation contract. | Provider/adapter-owned, no host revocation contract. | Adapter-owned, no host principal-verification contract. A concrete provider and separately versioned protocol evidence are required before extending host auth. |

## Implementation gates

BL-60 follows this qualification and implements the shared seam. BL-61 (GitHub),
BL-62 (Linear), and BL-63 (Jira/external boundaries) depend on BL-59 and BL-60.
BL-53 remains open until all its acceptance criteria are satisfied; completing
this research does not deliver OAuth login.

Before shipping any eligible flow:

- Register an owner-approved public application and obtain its public client ID.
  Qualify redirect registration and loopback callback support for Linear; require
  S256, state validation, bounded listener lifetime and explicit interactive consent.
  GitHub device authorization must honor polling interval, `slow_down`, denial and
  code expiry. Neither path may start from MCP, hooks, actions or unattended claims.
- Resolve GitHub's read-only/private-repository and permission-upgrade boundary in
  BL-61. Classic OAuth cannot satisfy private read-only access with `repo`. A
  GitHub App is a candidate, not proof that installation/Projects permissions and
  read-to-write consent work end to end. Do not weaken least privilege to ship it.
- Extend existing account configuration, resolver, redaction and principal checks.
  Linear needs explicit Bearer-token handling distinct from its raw personal API
  key path. Keep non-opt-in helper/environment accounts unchanged. Verify principal
  and binding after login and **every refresh**; mismatch disables those sources.
- Update D17 in the same change that introduces persistence. Store access/refresh
  tokens only in an OS keychain or owner-private store, never disposable cache.
  Serialize refresh across CLI, TUI and MCP processes by credential identity,
  durably retain each rotated token before releasing the lock, and re-read under
  the lock. The existing resolver's process-local lock is not sufficient. Provider
  grace periods are recovery aids, not substitutes for serialization.
- Discover actual granted scopes/permissions, request writes only after explicit
  opt-in, then rediscover capabilities. Distinguish expiry, revocation, insufficient
  scope, SSO/SAML policy, rate limits and downtime in structured diagnostics.
- Logout deletes stored credentials and purges that principal's cached rows.
  Revoke remotely where a qualified public-client endpoint exists; otherwise
  clearly report the manual revocation step. Do not acquire a client secret just
  to call a confidential revocation API.
- Exercise fake authorization servers for state/PKCE validation, login, rotation,
  concurrent refresh, denial, revocation, principal mismatch, unattended refusal,
  and canary-token leaks across config, argv, hooks/actions, logs/events, MCP and
  cache. Provider documentation does not replace executable security evidence.

Jira stays helper/API-token based when its adapter ships, unless official evidence
establishes a secret-free native code exchange **and refresh**. BL-40/BL-41 remain
separate dependencies for Jira API behavior. External host OAuth waits for a
concrete eligible consumer and a compatible, versioned authentication contract;
protocol v1 must not gain invented auth methods. Native, Backlog.md and Beads are
local and need no OAuth.

## Official sources

Links and claims were checked on 2026-10-23. Expiry, refresh and scope behavior
must be rechecked when integrating a provider or qualifying an Enterprise version.

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
