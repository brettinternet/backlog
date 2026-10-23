# Jira Cloud

Project `KAN` on `acme.atlassian.net`, with an API token from the environment.

| File | Goes in |
| --- | --- |
| [`backlog.yaml`](backlog.yaml) | The repository root; commit it |
| [`config.yaml`](config.yaml) | `~/.config/backlog/config.yaml`; never commit it |

Create an API token at id.atlassian.com → Security → API tokens, then find your
accountId:

```sh
export JIRA_API_TOKEN=ATATT3x...
curl -s -u "alice@acme.com:$JIRA_API_TOKEN" https://acme.atlassian.net/rest/api/3/myself | jq -r .accountId
```

```text
712020:3f1c9a2e-8b4d-4c6e-9a1f-2b3c4d5e6f7a
```

Put it in the account's `user`. Keep done issues in the `jql`, or their
dependents stay blocked.

```sh
bl trust
bl ls
```

```text
10001  To Do        Retry failed webhooks
10002  In Progress  Rotate the signing key
10007  To Do        Document the webhook payload
```

Items are addressed by numeric ID, which survives a move to another project.
`fields.key` holds `KAN-1`.

```sh
bl start jira:10001
bl set jira:10001 assignees=712020:3f1c9a2e-8b4d-4c6e-9a1f-2b3c4d5e6f7a --yes
bl comment jira:10001 'Reproduced on staging'
bl done jira:10001
```

Status changes follow the issue's transitions, so `status=` takes a destination
name like `In Progress`. Done with a resolution in `unsuccessfulResolutions`, or
Won't Do, counts as canceled.

## Scoped tokens

A scoped token goes through Atlassian's gateway. Add the site's cloud ID:

```sh
curl -s https://acme.atlassian.net/_edge/tenant_info
```

```yaml
sources:
  - id: jira
    type: jira
    site: https://acme.atlassian.net
    cloudId: 0f9a8b7c-6d5e-4f3a-9b2c-1d0e9f8a7b6c
    account: work
    jql: 'project = KAN ORDER BY id ASC'
```

Give it `read:jira-user` and `read:jira-work`, plus `write:jira-work` for writes.
Jira has no OAuth login; use `env` or a `credential` helper.
