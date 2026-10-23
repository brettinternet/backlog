# Linear

One Linear team, with an API key from the environment.

| File | Goes in |
| --- | --- |
| [`backlog.yaml`](backlog.yaml) | The repository root; commit it |
| [`config.yaml`](config.yaml) | `~/.config/backlog/config.yaml`; never commit it |

Create a personal API key in Linear Settings → Security & access. Give it read
access, plus issue update and comment creation for writes. Then find the UUIDs:

```sh
export LINEAR_API_KEY=lin_api_...
q() { curl -s https://api.linear.app/graphql -H "Authorization: $LINEAR_API_KEY" \
  -H 'Content-Type: application/json' --data "{\"query\": \"$1\"}"; }
q '{ viewer { id organization { id } } teams { nodes { id key } } }'
q '{ team(id: \"TEAM_UUID\") { states { nodes { id name type } } } }'
```

```text
{"data":{"viewer":{"id":"5f6a7b8c-…","organization":{"id":"7c4f2a8e-…"}},"teams":{"nodes":[{"id":"3e9b1c7d-…","key":"ENG"}]}}}
{"data":{"team":{"states":{"nodes":[{"id":"1a2b3c4d-…","name":"Todo","type":"unstarted"}, …]}}}}
```

`viewer.id` is the account's `user`, the organization is `site`, and each state
becomes a status. State types map to categories:

| Linear type | Category |
| --- | --- |
| `backlog` | backlog |
| `unstarted` | todo |
| `started` | active |
| `completed` | done |
| `canceled`, `duplicate` | canceled |

```sh
bl trust
bl next
```

```text
ENG-431  1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d  Retry failed webhooks
```

Statuses are state UUIDs; `bl ls --json` has the names. Writes change one field
per command and need `allowWrites: true`:

```sh
bl start eng:ENG-431
bl set eng:ENG-431 priority=high
bl set eng:ENG-431 labels=bug,webhooks
bl comment eng:ENG-431 'Retries now back off'
bl done eng:ENG-431
```

## Other credentials

Read the key from the macOS Keychain instead:

```sh
security add-generic-password -s linear-api -a "$USER" -w
```

```yaml
accounts:
  linear:
    - name: work
      user: VIEWER_UUID
      credential: [security, find-generic-password, -s, linear-api, -w]
```

Or log in with OAuth ([details](../../docs/linear.md#oauth-login)) by replacing
`env` with `oauth: true`:

```sh
bl auth login linear work --write
```
