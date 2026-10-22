#!/usr/bin/env python3
"""Deterministic stable-CLI boundary fixture; never reads Worklease state."""
import json
import os
import pathlib
import sys

plan_path = pathlib.Path(os.environ["BL_FAKE_PLAN"])
plan = json.loads(plan_path.read_text())
index_path = plan_path.with_suffix(".index")
index = int(index_path.read_text()) if index_path.exists() else 0
try:
    step = plan["steps"][index]
    assert sys.argv[1:] == step["argv"], (sys.argv[1:], step["argv"])
    assert pathlib.Path.cwd().resolve() == pathlib.Path(plan["root"]).resolve()
    for selector in ("WORKLEASE_SESSION_ID", "WORKLEASE_HANDLE", "WORKLEASE_RUN_ID"):
        assert selector not in os.environ, selector
except (AssertionError, IndexError) as error:
    plan_path.with_suffix(".failure").write_text(str(error))
    sys.exit(1)
index_path.write_text(str(index + 1))
print(json.dumps(step["response"]))
sys.exit(0 if step["response"]["ok"] else 2)
