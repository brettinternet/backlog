# Git and events

`git: {autoCommit: true}` makes each command that changes items commit those
files, and nothing else. It runs only after `bl trust`.

```sh
git add -A
git commit -qm 'Add backlog'
bl trust
bl start BL-2
bl ac BL-2 check 1 2
bl done BL-2
git log --oneline
```

```text
2053d2d docs(backlog): done BL-2 Add sign-in page
304838f docs(backlog): ac BL-2 Add sign-in page
1c59bf4 docs(backlog): start BL-2 Add sign-in page
4c41b06 Add backlog
```

`bl` never pushes. It skips the commit, keeping the edit, when the file was
already dirty or a merge or rebase is in progress.

Every change is also logged. Finishing BL-2 made BL-3 ready:

```sh
bl events
bl events 'changed:readiness is:ready'
```

```text
01M5N5BPS7WWE2G1VCVWKQ3XDR item.updated tasks:BL-2
01M5N5BPSFY46XSFYH7WAA9FNT item.readiness tasks:BL-2
01M5N5BPZAGCKVRFNRCRF07S3P item.updated tasks:BL-2
01M5N5BQ4DSW6D19R8HEK75V04 item.updated tasks:BL-2
01M5N5BQ4N4ES6E3M13E9V9GRC item.readiness tasks:BL-3

01M5N5BQ4N4ES6E3M13E9V9GRC item.readiness tasks:BL-3
```

`--follow` streams new events, one JSON line each, including edits made by hand
or by another process:

```text
$ bl events --follow --json 'changed:readiness is:ready'
{"schemaVersion":1,"ok":true,"command":"events","data":{"event":{"id":"01M5N5BQ4N4ES6E3M13E9V9GRC","type":"item.readiness","ref":"tasks:BL-3","changes":{"readiness":{"from":"blocked","to":"ready","reason":[{"code":"candidate"}]}},"item":{…},"origin":"cli",…}}}
```

The fields are in [docs/events.md](../../docs/events.md).
