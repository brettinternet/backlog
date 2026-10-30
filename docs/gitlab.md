# GitLab

A GitLab forge lists a project's merge requests for [linked
changes](changes.md). bl has no GitLab source, so a GitLab account serves only
the forge.

## Setup

User config (`~/.config/backlog/config.yaml`), for gitlab.com or a
self-managed host:

```yaml
accounts:
  gitlab:
    - name: work
      host: gitlab.com
      user: YOUR_USERNAME
      credential: [glab, config, get, token, --host, gitlab.com]
      # or: env: BL_WORK_GITLAB_TOKEN
```

The token needs the `read_api` scope: a personal, group, or project access
token. bl checks it belongs to `user` before reading anything, and keeps it
only in memory, redacted from the cache, events, and logs. GitLab accounts
have no OAuth login.

With exactly one GitLab account for its host, a GitLab `origin` is the forge
without configuration. Otherwise name it in `backlog.yaml` and run `bl trust`:

```yaml
changes:
  forges:
    - type: gitlab
      host: gitlab.example.com   # default gitlab.com
      repo: group/subgroup/app
      account: work              # default the only GitLab account for the host
```

## Merge requests

Each refresh reads every open merge request and every other one updated since
the last, with one GraphQL query per page through the account's request
queue, and keeps them as a GitHub forge keeps pull requests
([Forge refresh](changes.md#forge-refresh)).

| Change | From GitLab |
| --- | --- |
| State | `opened` is open, or draft when marked draft; `merged`; `closed` |
| Checks | The head pipeline: success is passing; failed or canceled failing; created, pending, running, manual, or scheduled pending; none otherwise |
| Review | Changes requested by any reviewer; approved once GitLab reads it approved; requested while approvals are missing |
| `+/−` | The merge request's diff stats |
| Landed | Its merge commit, its squash commit, or for a fast-forward merge its head commit |

A merge request links as a pull request does, by source branch, an item ID in
its title, or a closing keyword in its description, and shows as `!N`. Its
change view reads the merge request's diffs and commits, and its
Conversation the description, pipeline jobs, reviewers, and discussions: a
resolvable discussion is a thread, other notes are comments, and system notes
are left out. A job allowed to fail reads as skipped. GitLab sends
`RateLimit-Reset` as a Unix time; the request queue waits until then.
