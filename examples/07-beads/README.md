# Beads

Read and update a local [Beads](https://github.com/steveyegge/beads) checkout
through `bd` 1.3.0. `path: .` is the repository root; Beads sources need
`bl trust`.

Make a Beads database with three issues:

```sh
bd init --non-interactive --skip-hooks --skip-agents --prefix app
bd create 'Set up CI' --id app-1 -p 1
bd create 'Add sign-in page' --id app-2 -p 1
bd create 'Add password reset' --id app-3 -p 2
bd dep add app-3 app-2
bd close app-1
bl trust
```

`bl` computes readiness from the `blocks` edges:

```sh
bl next --explain -n 0
```

```text
app-2  open  high  Add sign-in page

app-2  ready       high · unblocks 1 (app-3)
app-1  ineligible  high · unblocks 0 · status: done
app-3  blocked     medium · unblocks 0 · blocker: app-2
```

Writes go through `bd` and land in the Beads database:

```sh
bl start app-2
bl set app-2 priority=urgent assignees=ana
bl comment app-2 'Form done' --author ana
bd show app-2
```

```text
◐ app-2 · Add sign-in page   [P0 · IN_PROGRESS]
Created by: brett · Assignee: ana · Type: task
…
COMMENTS
  2026-10-23 19:26 ana
      Form done
```

Unknown Beads statuses count as `backlog` and are never ready, so declare
every status you use. Each write is a local Dolt commit; `bl` never pushes.
