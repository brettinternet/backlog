#!/usr/bin/env python3
"""Locked authority/handles fixture: separate CLI and MCP processes share state."""
import fcntl
import json
import os
import pathlib
import sys
import time

root = pathlib.Path(os.environ["BL_FAKE_ROOT"])
assert pathlib.Path.cwd().resolve() == root.resolve()
assert not any(k in os.environ for k in ("WORKLEASE_SESSION_ID", "WORKLEASE_HANDLE", "WORKLEASE_RUN_ID"))
argv = sys.argv[1:]
assert argv[0] == "--json"
op = argv[1]
session = argv[argv.index("--session") + 1] if "--session" in argv else None
state_path = pathlib.Path(os.environ["BL_FAKE_STATE"])
lock = open(str(state_path) + ".lock", "a+")

def load():
    return json.loads(state_path.read_text()) if state_path.exists() else dict(claims={}, arrivals=[], calls=[])

def save(state):
    state_path.write_text(json.dumps(state))

# Force both callers to reach acquire against their first snapshot before one
# can write active status. Poll the observable arrival ledger with a deadline.
if op == "acquire" and "--resource" in argv and os.environ.get("BL_FAKE_BARRIER") == "1":
    fcntl.flock(lock, fcntl.LOCK_EX)
    state = load()
    if session not in state["arrivals"]:
        state["arrivals"].append(session)
    save(state)
    fcntl.flock(lock, fcntl.LOCK_UN)
    deadline = time.monotonic() + 10
    while True:
        fcntl.flock(lock, fcntl.LOCK_EX)
        ready = len(load()["arrivals"]) == 2
        fcntl.flock(lock, fcntl.LOCK_UN)
        if ready:
            break
        assert time.monotonic() < deadline, "second caller never arrived"

fcntl.flock(lock, fcntl.LOCK_EX)
state = load()
state["calls"].append(dict(op=op, session=session, argv=argv))
claim = state["claims"].get(session)
response = dict(schemaVersion=2, ok=True)

def unknown():
    response.update(ok=False, error=dict(reason="authority-unavailable", message="reply lost; retain this session", details=dict(commitState="unknown")))

if op == "handle":
    if claim is None:
        response.update(ok=False, error=dict(reason="handle-malformed", message="handle is missing"))
    else:
        response.update(claim)
        pending = claim.get("pending", False)
        response.update(authorityId="authority", selectedAuthorityId="authority", authorityMatchesSelection=True, selectorType="contextual", selectorProvenance="resolved", selectorSession=session, recordedState="pending" if pending else "ready", handlePath="/retained/" + session, pendingRequestPresent=pending, pendingKind="acquire", pendingOperationId="original-operation")
elif op == "runs":
    response["runs"] = []
elif op in ("status", "verify"):
    assert claim is not None and not claim.get("pending", False)
    response["claim"] = claim
elif op == "acquire":
    if "--handle" in argv:
        assert argv == ["--json", "acquire", "--handle", "/retained/" + session, "--session", session], "reconstructed recovery request"
        assert claim is not None and claim["pending"]
        if os.environ.get("BL_FAKE_REPLAY_UNKNOWN") == "1":
            unknown()
        else:
            claim["pending"] = False
            response.update(claim)
    else:
        assert claim is None, "second acquisition while holding a claim"
        assert argv[argv.index("--wait") + 1] == "0s"
        resource = argv[argv.index("--resource") + 1]
        holder = next((c for c in state["claims"].values() if resource in c["resources"]), None)
        if holder:
            response.update(ok=False, error=dict(reason="already-claimed", message="held elsewhere", details=dict(holder=holder)))
        else:
            key = argv[argv.index("--work-key") + 1]
            item = root / "items" / (key.split(":")[1] + ".md")
            assert "status: todo" in item.read_text(), "status changed before claim"
            claim = dict(claimId="claim-" + session, resources=[resource], workKey=key, sessionId=session, agentId="agent", authorityId="authority", active=True, expiresAt="2030-01-01T00:00:00Z", pending=os.environ.get("BL_FAKE_PENDING") == "1")
            state["claims"][session] = claim
            if claim["pending"]:
                unknown()
            else:
                response.update(claim)
elif op == "release":
    assert claim and argv[argv.index("--if-claim-id") + 1] == claim["claimId"]
    del state["claims"][session]
elif op == "heartbeat":
    response["receipt"] = dict(result=dict(expiresAt=claim["expiresAt"]))
else:
    raise AssertionError(op)
save(state)
print(json.dumps(response))
sys.exit(0 if response["ok"] else 2)
