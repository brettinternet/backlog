# Examples

Each directory is a project root. Copy one into a new repository to try it:

```sh
cp -R examples/01-quickstart /tmp/quickstart
cd /tmp/quickstart
git init -q
bl
```

A `config.yaml` belongs in `~/.config/backlog/config.yaml`; merge it into yours.

| Example | Backend | Shows |
| --- | --- | --- |
| [01-quickstart](01-quickstart) | Native | Blockers, `next`, acceptance criteria |
| [02-custom-workflow](02-custom-workflow) | Native | Own statuses, milestones, epics, readiness rules |
| [03-saved-views](03-saved-views) | Native | Saved queries, grouping, columns, a board |
| [04-hooks-and-actions](04-hooks-and-actions) | Native | A hook that blocks `done`, keyboard actions |
| [05-agent-loop](05-agent-loop) | Native | Worklease claims for parallel agents |
| [06-backlog-md](06-backlog-md) | Backlog.md | An existing Backlog.md folder |
| [07-beads](07-beads) | Beads | An existing `bd` database |
| [08-github-issues](08-github-issues) | GitHub | Issues and unlinked pull requests through `gh auth token` |
| [09-github-project](09-github-project) | GitHub | Status from a Projects v2 board |
| [10-linear](10-linear) | Linear | A team through an API key |
| [11-jira](11-jira) | Jira Cloud | A JQL query through an API token |
| [12-external-adapter](12-external-adapter) | External | A small Python adapter |
| [13-monorepo](13-monorepo) | Native | One queue per package, cross-queue blockers |
| [14-local-overrides](14-local-overrides) | Native | Private and per-user settings |
| [15-workspaces](15-workspaces) | Native | Several repositories in one list |
| [16-mcp](16-mcp) | Native | Tools for Claude Code and other agents |
| [17-import-backlog-md](17-import-backlog-md) | Backlog.md | Converting to native files |
| [18-git-and-events](18-git-and-events) | Native | Automatic commits, the event log |

Credentials stay in user config or the environment, never in `backlog.yaml`.
Hooks, actions, adapters, and network sources run only after `bl trust`.
