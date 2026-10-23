#!/usr/bin/env python3
"""Read-only bl adapter: each `- [ ]` line in TODO.md is an item."""
import hashlib
import json
import re
import sys

LINE = re.compile(r"^- \[([ x])\] (.+)$")


def items(source):
    out = []
    with open("TODO.md") as f:
        for text in f:
            m = LINE.match(text.rstrip("\n"))
            if not m:
                continue
            done = m.group(1) == "x"
            out.append({
                "Ref": {"source": source, "id": f"T-{len(out) + 1}"},
                "Title": m.group(2),
                "Status": "done" if done else "todo",
                "Category": "done" if done else "todo",
                "Version": hashlib.sha256(text.encode()).hexdigest()[:12],
            })
    return out


def reply(id, result=None, error=None):
    msg = {"jsonrpc": "2.0", "id": id}
    if error:
        code = -32007 if error == "not-found" else -32601
        msg["error"] = {"code": code, "message": error, "data": {"diagnostic": error}}
    else:
        msg["result"] = result
    print(json.dumps(msg, separators=(",", ":")), flush=True)


source = ""
caps = ["list", "read"]
for frame in sys.stdin:
    req = json.loads(frame)
    method, params = req.get("method"), req.get("params", {})
    if "id" not in req:  # $/cancelRequest
        continue
    if method == "initialize":
        source = params["sourceId"]
        manifest = {"id": "todo-md", "version": "1.0.0", "capabilities": caps, "requiredFeatures": []}
        reply(req["id"], {"protocolVersion": 1, "manifest": manifest})
    elif method == "capabilities":
        reply(req["id"], {"capabilities": caps})
    elif method == "list":
        reply(req["id"], {"items": items(source)[:100], "nextCursor": "", "coverage": "complete"})
    elif method == "read":
        found = [i for i in items(source) if i["Ref"]["id"] == params["id"]]
        reply(req["id"], {"item": found[0]}) if found else reply(req["id"], error="not-found")
    else:
        reply(req["id"], error="unsupported-capability")
