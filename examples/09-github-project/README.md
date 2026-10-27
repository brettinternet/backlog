# GitHub Projects v2

Issues from `acme/app` with their status from project board #12. Builds on
[GitHub Issues](../08-github-issues).

Find the project and its Status field:

```sh
gh auth refresh --hostname github.com --scopes repo,project
gh project view 12 --owner acme --format json --jq .id
gh project field-list 12 --owner acme --format json --jq '.fields[] | select(.name == "Status")'
```

```text
PVT_kwDOBk3x2c4AqZ7m
{"id":"PVTSSF_lADOBk3x2c4AqZ7mzgYt3kA","name":"Status","options":[{"id":"f75ad846","name":"Todo"},{"id":"47fc9ee4","name":"In Progress"},{"id":"5a1c2e90","name":"In Review"},{"id":"98236657","name":"Done"}],"type":"ProjectV2SingleSelectField"}
```

Copy the IDs into `githubProject`, map each option to a category, and declare
each option as a status. Then:

```sh
bl trust
bl ls --group status
```

```text
Todo (2)
131  Todo  Add dark mode
135  Todo  Upgrade the payment SDK

In Progress (1)
128  In Progress  Checkout fails with an empty cart
```

Statuses show the board's option names; `status=` and `--json` use the IDs.

`bl start` moves the card to In Progress. `bl done` closes the issue:

```sh
bl start issues:131
bl set issues:131 status=5a1c2e90
bl done issues:131
```

A closed issue is done whatever its card says. An open issue that isn't on the
board, or has an unmapped option, is `backlog` and never ready. Without
`allowWrites: true`, status changes to the board are refused.
