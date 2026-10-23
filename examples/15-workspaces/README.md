# Workspaces

Two repositories ranked as one list. [`site`](site) waits on [`app`](app):
`SITE-1` has `blocked_by: [tasks:APP-1]`.

[`config.yaml`](config.yaml) goes in `~/.config/backlog/config.yaml`, and lists
the repositories:

```yaml
workspaces:
  - {name: launch, projects: [~/src/app, ~/src/site]}
```

```sh
mkdir -p ~/src
cp -R app site ~/src/
git -C ~/src/app init -q
git -C ~/src/site init -q
bl next --workspace launch --explain -n 0
```

```text
app tasks:APP-1    todo  high    Add the pricing API
site tasks:SITE-2  todo  medium  Update the footer links
app tasks:APP-2    todo  low     Fix the slow search

github.com%2Facme%2Fapp/tasks:APP-1    ready    high · unblocks 1 (SITE-1)
github.com%2Facme%2Fsite/tasks:SITE-1  blocked  medium · unblocks 0 · blocker: APP-1
github.com%2Facme%2Fsite/tasks:SITE-2  ready    medium · unblocks 0
github.com%2Facme%2Fapp/tasks:APP-2    ready    low · unblocks 0
```

Both use the source ID `tasks`; the project ID keeps them apart. `tasks:APP-1`
resolves because only one loaded item matches it.

Workspace lists are read-only. Write from the item's own repository:

```sh
cd ~/src/app
bl done APP-1
bl next --workspace launch -n 0
```

```text
APP-1  done  high  Add the pricing API
site tasks:SITE-1  todo  medium  Publish the pricing page
site tasks:SITE-2  todo  medium  Update the footer links
app tasks:APP-2    todo  low     Fix the slow search
```

In the TUI, `W` toggles the workspace. Set `defaultWorkspace: launch` to open
it from outside any repository.
