# Agent overview

Read `bl instructions workflow` before changing items. Use the installed binary's
guidance, not commands from a newer design or another tracker.

Inspect work with `bl ls --json`, `bl show ID --json`, and
`bl next --explain --json`. Use numeric ID shorthand only when unambiguous;
use source-qualified IDs otherwise. Read the full item before starting.
Pass the last returned item version to writes that depend on that read.

This release supports native files and Backlog.md import. It does not yet
provide claims, MCP, hooks, or remote adapters. Status changes do not claim work.
Follow the project's coordination policy; do not run competing agents without
an external claim protocol.

Read the workflow, query, format, and json topics as needed.
