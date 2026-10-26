# Readiness and ranking

`internal/engine.Evaluate` decides which items are ready and in what order, with
a reason for every item. It does no I/O. The CLI, query language, TUI, and
coordination all use its result. [design.md §6](design.md#6-readiness-and-next)
defines the rules; this page covers the details.

```text
$ bl next --explain -n 0
BL-1  todo  high  Debounce detail rendering

BL-1  ready       high · unblocks 2 (BL-2, BL-3)
BL-2  ineligible  unset · unblocks 0 · status: active; blocker: BL-1
BL-3  blocked     unset · unblocks 0 · blocker: BL-1
```

## Inputs

Loaded items, the effective `ready` config (with defaults), ID aliases, archive
and freshness flags, and, with coordination, claim states.

- Bare blocker and parent references are local to the item's source. Padded
  numbers resolve naturally (`BL-7` finds `BL-007`); ambiguous IDs stay
  unresolved. Cross-source references resolve when the target is loaded.
- A missing or stale target is unknown, never satisfied. Mark stale items
  individually: a source-wide partial scan doesn't make a fresh target unknown.
- With coordination on, an item without a claim entry is unknown; pass `none`
  for a known-free item. With coordination off, claims are ignored.

## Readiness

The first matching state wins, but every applicable reason is kept:

| State | Reasons |
| --- | --- |
| `ineligible` | Archived, status not in `ready.statuses`, an open container, or an excluded label |
| `blocked` | A known unsatisfied blocker |
| `waiting` | Assigned only to others, or claimed by someone else |
| `unknown` | An unresolved or stale blocker, or an unknown claim |
| `ready` | None of the above |

Open means not done and not canceled. Archived children don't keep a parent
open, but an unresolved listed child does, conservatively.

## Ranking

All items are returned in the §6 order, plus the ready subset. Unblock impact
counts distinct open, unarchived items that transitively depend on this one,
stopping at closed or archived items. When natural ID order ties (as with
imported padding), source and verbatim ID decide, so order never depends on
input order.

Cycles are reported separately as sorted strongly connected components,
including self-cycles. Items downstream of a cycle aren't listed as members.

## Explanations

```text
high · rank a3 · unblocks 4 (BL-9, BL-12, +2)
```

The rank segment is omitted for unranked items; unset priority shows `unset`.
Impact lists the first two references in natural order, then a count. Excluded
items add their state and reason codes. Structured reasons keep the source even
when the text shows only the ID.

## Cost

Adjacency lists and reusable traversal marks, not a transitive-closure matrix.
Memory is linear in items and edges. Time grows with reachable open dependents
(quadratic on one long chain) plus sorting. It runs on projection workers, never
the Bubble Tea update loop. `task bench` reports a 50k-item, 100k-edge graph.
