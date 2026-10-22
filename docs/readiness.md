# Readiness engine

`internal/engine.Evaluate` computes readiness, ranking, and explanations without
I/O. CLI, query, TUI, and coordination integrations consume this projection;
their commands are delivered separately.

Pass loaded items and the effective `project.ReadyConfig` (including defaults
from the config loader), identity aliases, archive/freshness maps, and optional
claim states. Unqualified dependency and parent references are source-local.
Padded numeric IDs resolve naturally; ambiguous IDs remain unresolved. A present
cross-source target resolves normally. Missing and stale targets are unknown,
not satisfied. Source-wide partial coverage does not invalidate a known fresh
target: mark stale cached targets individually.

Every item has structured reasons, including excluded items. Readiness precedence
is ineligible (archive, status, container), blocked (known blocker, excluded
label), waiting (assignment, other claim), unknown (unresolved/stale blocker or
unknown claim), then ready. All applicable reasons are retained. With coordination
enabled, an absent claim entry is unknown; explicitly supply `none` for a known
free item. With coordination disabled, claims have no effect.

Open means neither done nor canceled. Archived children do not make a container
open; an unresolved explicitly listed child conservatively excludes a container.
Unblock impact counts distinct open, non-archived transitive dependents, excluding
the item itself. Traversal stops at closed/archived dependents. Cycles are reported
as sorted strongly connected components (including self-cycles), independently
of readiness; downstream victims are not reported as cycle members.

Results include all items in the design.md §6 order and a ready-only candidate
list. Natural-ID ties fall back to source and verbatim ID so imported padding
never makes ordering depend on input order. Impact explanations keep only the
first two naturally sorted references plus a remaining count:

```text
high · rank a3 · unblocks 4 (BL-9, BL-12, +2)
```

Unranked items omit the rank segment; unset priority displays `unset`. Excluded
items append their readiness and reason codes. Structured references retain
source identity even when the compact text displays only the ID.

The evaluator uses adjacency lists and reusable traversal marks, not a dense
transitive-closure matrix. Storage is linear in items and edges; runtime depends
on reachable open dependents (quadratic on a long chain), plus sorting. Run it on
projection workers, never the Bubble Tea update loop. `task bench` includes a
50k-item, exactly 100k-edge shared-prerequisite graph and reports timing and
allocations without asserting host-dependent latency.
