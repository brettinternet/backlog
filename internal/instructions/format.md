# Native format

Keep one UTF-8 Markdown file per item at the configured source path, normally
backlog/ID.md. Use YAML frontmatter with required id, title, status, created,
and updated keys. Use RFC 3339 UTC timestamps, configured status keys, and
urgent/high/medium/low priorities. Preserve unknown fields and untouched text.

Use these level-2 body headings once each: Acceptance criteria, Definition of
done, Plan, Notes, Summary, Comments. Description precedes the first heading.
Write criteria as column-1 Markdown checkboxes. Indent Evidence: under the
criterion it proves; criterion numbers are not stable identifiers.

Prefer version-guarded CLI writes to manual rewrites:

```sh
bl new "Example !h #cli :feature" --ac "Observable result" --json
bl check --json
```

Create work nobody should pick up yet with `bl new TITLE --status draft --json`
when the project configures a draft status. Drafts are open but never ready, so
next never returns them.

Writes are minimal-diff and atomic under a shared lock. Never overwrite a
conflict without rereading. Conflict markers fail parsing. Use
`bl check --fix --json` only for supported repairs: filename mismatch when the
target is free, line endings/final newline, updated-only conflicts, and
append-only comment conflicts. Resolve other conflicts manually.

Archive moves files to archive/ without rewriting them. Use
`bl renumber FROM TO` to change an ID and its references; do not just rename
the file. From linked worktrees, the default control root is the primary
checkout, so all agents see the same backlog.
