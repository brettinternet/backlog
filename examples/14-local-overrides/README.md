# Local overrides

The team commits `backlog.yaml`. Ana keeps her own settings out of it.

| Layer | File | Ana's change |
| --- | --- | --- |
| Shared | [`backlog.yaml`](backlog.yaml) | — |
| Private | [`backlog.local.yaml`](backlog.local.yaml) | Adds a Mine view |
| User, this project only | `projects.github.com/acme/shop` in [`config.yaml`](config.yaml) | Skips `frontend` work |

Later layers win. Mappings merge, lists replace, so `backlog.local.yaml`
repeats the shared Next view to keep it. User overrides are keyed by
`project.id`, which defaults to the `origin` remote.

```sh
echo /backlog.local.yaml >> .gitignore
bl next -n 0
bl ls --view Mine
bl config show | grep -E '^(views|ready\.excludeLabels):'
```

```text
SHOP-1  todo  high  Add the refunds endpoint

SHOP-1  todo  high  Add the refunds endpoint

ready.excludeLabels: user-project (~/.config/backlog/config.yaml:4)
views: local (backlog.local.yaml:2)
```

Without Ana's override, `bl next` also shows SHOP-2, labelled `frontend`.
SHOP-3 is labelled `blocked`, which the shared config excludes.

`bl doctor` warns when `backlog.local.yaml` is tracked or not ignored. It uses
the project schema; accounts and credentials stay in user config. Hooks,
actions, and network sources in it still need `bl trust`.
