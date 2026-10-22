# Query

Quote queries against shell expansion. Terms are ANDed; use | for OR,
parentheses for grouping, and a leading - for negation. Quote values containing
spaces inside the query.

```sh
bl ls 'is:open priority:>=high' --sort priority,id --group status -n 20 --json
bl next 'label:cli' --explain --json
bl ls -- -priority:low
```

Use status, category, priority, assignee, label, type, milestone, source,
parent, blocked-by, blocks, created, updated, due, text, and field.NAME
predicates. Use is:ready or is:blocked for engine readiness; has:criteria
checks for acceptance criteria. Claim predicates require coordination and are
not available in this release.

Lists default to is:open unless an is: term is present. Use is:closed for closed
items and is:archived to load archives. Next always ranks ready candidates;
its query narrows candidates, not their dependency context. Do not supply sort
or group directives to next.

Use >= or <= on priorities and dates. Relative ages such as updated:<7d mean
newer than seven days ago; due:<today means overdue. Use none for unset values.
Malformed or unknown vocabulary is a usage error, not an empty result.
