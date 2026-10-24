#!/usr/bin/env python3
"""Visible-resource lifecycle fixture for control-root integration tests."""
import json
import os
import pathlib
import sys

assert pathlib.Path.cwd().resolve() == pathlib.Path(os.environ["BL_FAKE_ROOT"]).resolve()
for name in ("WORKLEASE_SESSION_ID", "WORKLEASE_HANDLE", "WORKLEASE_RUN_ID"):
    assert name not in os.environ
argv = sys.argv[1:]
assert argv[0] == "--json"
if argv[1:] == ["profile", "show"]:
    print(json.dumps(dict(schemaVersion=2, ok=True, builtin=True, profile=dict(builtin=True, name="local"))))
    sys.exit(0)
assert argv[1] == "runs" or argv[-2:] == ["--session", "loop"]
path = pathlib.Path(os.environ["BL_FAKE_STATE"])
claim = json.loads(path.read_text()) if path.exists() else None
operation = argv[1]
response = dict(schemaVersion=2, ok=True)
if operation == "handle":
    if claim is None or not claim["active"]:
        response.update(ok=False, error=dict(reason="handle-malformed", message="handle is missing"))
    else:
        response.update(claimId=claim["claimId"], authorityId="authority", selectedAuthorityId="authority", authorityMatchesSelection=True, selectorType="contextual", selectorProvenance="resolved", selectorSession="loop", recordedState="ready", resources=claim["resources"])
elif operation == "runs":
    response["runs"] = []
elif operation == "acquire":
    assert claim is None or not claim["active"]
    claim = dict(claimId="claim", resources=[argv[argv.index("--resource") + 1]], authorityId="authority", sessionId="loop", agentId="agent@host", expiresAt="2030-01-01T00:00:00Z", active=True, workKey="test BL-012")
    path.write_text(json.dumps(claim))
    response.update(claim)
elif operation in ("status", "verify"):
    response["claim"] = claim
elif operation == "heartbeat":
    assert claim["active"]
    response["receipt"] = dict(result=dict(expiresAt=claim["expiresAt"]))
elif operation == "release":
    assert claim["active"]
    expected = argv[argv.index("--if-claim-id") + 1]
    if os.environ.get("BL_FAKE_REPLACE_ON_RELEASE"):
        claim.update(claimId="replacement", resources=["other-resource"], workKey="test BL-013")
    if claim["claimId"] != expected:
        response.update(ok=False, error=dict(reason="claim-mismatch", message="claim identity changed"))
    else:
        claim["active"] = False
    path.write_text(json.dumps(claim))
else:
    raise AssertionError(operation)
print(json.dumps(response))
sys.exit(0 if response["ok"] else 2)
