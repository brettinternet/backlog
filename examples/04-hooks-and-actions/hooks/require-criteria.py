"""Refuse to finish an item while it has unchecked acceptance criteria."""
import json
import sys

item = json.load(sys.stdin)["item"]
unchecked = [c["text"] for c in item["criteria"] or [] if not c["checked"]]
if unchecked:
    sys.exit(f"{len(unchecked)} unchecked criteria, first: {unchecked[0]}")
