# Agent overview

Read `bl instructions workflow` before changing items. Use the installed binary's
guidance, not commands from a newer design or another tracker.

Inspect work with `bl ls --json`, `bl show ID --json`, and
`bl next --explain --json`. Use numeric ID shorthand only when unambiguous;
use source-qualified IDs otherwise. Read the full item before starting.
Pass the last returned item version to writes that depend on that read.

Items come from native files, Backlog.md, Beads, GitHub, Linear, or external
adapters, and may be open GitHub pull requests no other item links (read-only;
they disappear once an item links them). `bl config show --json` lists the configured sources, and
`bl doctor --json` checks them. The same commands and JSON work for every
source, from the CLI or MCP. A write that fails with unknown-outcome may have
applied: reread the item before deciding anything, and never repeat it blindly.
Logging in (`bl auth login`) and trusting a project (`bl trust`) need a person:
stop and ask instead.

With coordination configured, use
`bl next --claim --start --session SESSION --json` to select and start work;
plain next remains read-only. Follow the project's coordination policy. Unknown
or unverifiable ownership refuses side effects; do not bypass this with a new
session.

Read the workflow, query, format, and json topics as needed, and tours to
walk a reviewer through a change.
