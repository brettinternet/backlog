# MCP

Give an agent the backlog as tools. Claude Code reads
[`.mcp.json`](.mcp.json) from the project root and starts `bl mcp` there:

```json
{
  "mcpServers": {
    "backlog": {
      "command": "bl",
      "args": ["mcp"]
    }
  }
}
```

Or add it from the command line. Use an absolute path when the client's `PATH`
lacks `bl`:

```text
$ claude mcp add --transport stdio backlog -- "$(command -v bl)" mcp
$ pi mcp add backlog --cwd "$PWD" -- "$(command -v bl)" mcp
```

An agent finishing BL-2 calls:

```json
{"name":"next","arguments":{}}
{"name":"update","arguments":{"id":"BL-2","fields":{"status":"in-progress"}}}
{"name":"criteria","arguments":{"id":"BL-2","operation":"check","index":1,"evidence":"Manual sign-in works"}}
{"name":"criteria","arguments":{"id":"BL-2","operation":"check","index":2}}
{"name":"update","arguments":{"id":"BL-2","fields":{"status":"done"}}}
```

Results use the same names as `bl show --json`. Each carries the item's
`version`:

```json
{"item":{"source":"tasks","id":"BL-2","title":"Add sign-in page","status":"in-progress","category":"active","priority":"high","version":"0058ac49…"}}
```

Pass it back as `version` to refuse a write when someone else changed the item
first:

```json
{"content":[{"type":"text","text":"{\"message\":\"expected version does not match current content\",\"reason\":\"conflict\"}"}],"isError":true}
```

Check the server without a client by piping frames to it:

```sh
{
  echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"sh","version":"1"}}}'
  echo '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  echo '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"next","arguments":{}}}'
  sleep 2
} | bl mcp
```

```text
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{…},"protocolVersion":"2025-06-18","serverInfo":{"name":"bl","version":"…"}}}
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"items\":[…]}"}],"structuredContent":{"items":[…]}}}
```

The `bl://instructions` resource tells the agent how to use the tools. All
tools and arguments are in [docs/mcp.md](../../docs/mcp.md).
