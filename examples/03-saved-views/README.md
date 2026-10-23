# Saved views

Named queries in `backlog.yaml`. Each one is a TUI tab and a `bl ls --view`.
[`config.yaml`](config.yaml) is user config: it makes `me` mean `@ana`.

```sh
bl ls --view Mine
```

```text
WEB-1  todo         urgent  Crash on empty cart
WEB-3  in-progress  medium  Dark mode
```

```sh
bl ls --view Bugs
```

```text
none (1)
WEB-4  todo  high    Slow product list

@ana (1)
WEB-1  todo  urgent  Crash on empty cart

@ben (1)
WEB-2  todo  low     Typo on pricing page
```

```sh
bl ls --view Overdue
```

```text
WEB-1  todo  urgent  Crash on empty cart
WEB-4  todo  high    Slow product list
```

Extra terms narrow a view:

```sh
bl ls --view Bugs 'priority:>=high'
```

```text
none (1)
WEB-4  todo  high    Slow product list

@ana (1)
WEB-1  todo  urgent  Crash on empty cart
```

`Team board` opens as columns in the TUI (`layout: board`). Press `ctrl-s` in
the TUI to save the current filter as a view. See the
[query language](../../docs/query.md).
