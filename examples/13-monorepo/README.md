# Monorepo

One queue per package, ranked together. `source:ID` crosses queues:
[`WEB-1`](web/backlog/WEB-1.md) waits on `api:API-1`.

```text
backlog.yaml
api/backlog/API-1.md
web/backlog/WEB-1.md   blocked_by: [api:API-1]
web/backlog/WEB-2.md
```

```sh
bl next --explain -n 0
bl ls source:api
```

```text
api:API-1  todo  medium  Add the orders endpoint
web:WEB-2  todo  low     Fix the footer on mobile

web:WEB-1  blocked  high · unblocks 0 · blocker: API-1
api:API-1  ready    medium · unblocks 1 (WEB-1)
web:WEB-2  ready    low · unblocks 0

api:API-1  todo  medium  Add the orders endpoint
```

New items need `--source`. Qualify blockers in another queue:

```sh
bl new 'Show tracking link <api:API-1' --source web
bl done API-1
bl next -n 0
```

```text
web:WEB-3  todo  Show tracking link
api:API-1  done  medium  Add the orders endpoint
web:WEB-1  todo  high  Show order history
web:WEB-2  todo  low   Fix the footer on mobile
web:WEB-3  todo        Show tracking link
```

An unqualified `<API-1` means `web:API-1`, which doesn't exist, so the blocker
is unknown and the item is never ready. `bl check` reports it.

Any source type can join the list; add a GitHub source from
[example 08](../08-github-issues) and its issues rank alongside these.
