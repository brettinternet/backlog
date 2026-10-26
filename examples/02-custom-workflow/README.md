# Custom workflow

Your own statuses, milestones, labels, and new-item defaults. Each status maps
to one of five categories (`backlog`, `todo`, `active`, `done`, `canceled`);
readiness reads the category.

```sh
bl ls --group milestone
```

```text
beta (3)
SHOP-2  ready   high    Cart page
SHOP-3  ready   high    Payment form
SHOP-1  ready           Checkout

v1 (2)
SHOP-4  doing   medium  Product search
SHOP-5  review  medium  Product images

none (1)
SHOP-7  idea            Gift cards
```

Only SHOP-2 is ready. SHOP-3 has an excluded label, and the epic SHOP-1 waits
for its children (`skipContainers`):

```sh
bl next --explain -n 0
```

```text
SHOP-2  ready  high  Cart page

SHOP-2  ready       high · unblocks 0
SHOP-3  ineligible  high · unblocks 0 · excluded-label: needs-design
SHOP-4  ineligible  medium · unblocks 0 · status: active
SHOP-5  ineligible  medium · unblocks 0 · status: active
SHOP-1  ineligible  unset · unblocks 0 · container: SHOP-2, SHOP-3
SHOP-6  ineligible  unset · unblocks 0 · status: canceled
SHOP-7  ineligible  unset · unblocks 0 · status: backlog
```

`bl start` and `bl done` use `workflow`. Any other status is a plain `set`:

```sh
bl start SHOP-2
bl set SHOP-2 status=review
bl done SHOP-2
```

New items get `defaults`:

```sh
bl new 'Order history #checkout ~v1'
bl show SHOP-8
```

```text
SHOP-8  Order history
status     ready
type       task
labels     checkout
milestone  v1
created    2026-10-23T19:21:51Z
updated    2026-10-23T19:21:51Z

## Definition of done
- [ ] Tests pass
- [ ] Changelog updated
```

Check off definition-of-done items with `--dod`:

```sh
bl ac SHOP-8 check 1 --dod --evidence 'go test ./... passed'
```
