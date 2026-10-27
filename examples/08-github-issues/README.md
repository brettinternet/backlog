# GitHub Issues

One repository's issues. Blockers and sub-issues come from GitHub.

| File | Goes in |
| --- | --- |
| [`backlog.yaml`](backlog.yaml) | The repository root; commit it |
| [`config.yaml`](config.yaml) | `~/.config/backlog/config.yaml`; never commit it |

Change `acme/app` and `alice` to yours. The account runs `gh auth token`, so
there's no token to store. It needs the `repo` scope for private repositories
and writes:

```sh
gh auth refresh --hostname github.com --scopes repo
bl trust
bl ls
```

```text
128  open  Checkout fails with an empty cart
131  open  Add dark mode
135  open  Upgrade the payment SDK
```

Edits change one field per command:

```sh
bl new 'Retry failed uploads' +issues '#bug' @alice --body 'Steps…'
bl set issues:128 title='Checkout fails on an empty cart'
bl set issues:128 labels+=checkout
bl set issues:128 assignees=alice
bl comment issues:128 'Fixed in #140'
bl done issues:128
```

`bl done` closes the issue as completed. `bl set issues:128 status=open`
reopens it. Every open issue is `open` and ready; to give issues statuses, map
labels with [`statusLabels`](../../docs/github.md#label-statuses).

## Other credentials

A token from the environment instead of `gh`:

```yaml
accounts:
  github:
    - {name: personal, host: github.com, user: alice, env: GITHUB_TOKEN}
```

Or log in with a read-only GitHub App ([details](../../docs/github.md#oauth-login)):

```yaml
accounts:
  github:
    - {name: personal, host: github.com, user: alice, oauth: true}
```

```sh
bl auth login github personal
bl auth login github personal --write
```
