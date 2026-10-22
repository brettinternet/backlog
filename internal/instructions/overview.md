# Agent overview

Read `bl instructions workflow` before changing items. Use the installed binary's
guidance, not commands from a newer design or another tracker.

Inspect work with `bl ls --json`, `bl show ID --json`, and
`bl next --explain --json`. Use numeric ID shorthand only when unambiguous;
use source-qualified IDs otherwise. Read the full item before starting.
Pass the last returned item version to writes that depend on that read.

This release supports native files, Backlog.md import, MCP, and optional
coordination. With coordination configured, status transitions use claim gates;
next remains read-only. Follow the project's coordination policy. Unknown or
unverifiable ownership refuses side effects; do not bypass this with a new session.

Read the workflow, query, format, and json topics as needed.
