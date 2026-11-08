"""Assign every mapped test file to exactly one Stage 1 group and emit the ledger tables.

Ownership = group with the most distinct source modules imported by the test file.
Ties go to the earlier group in groups.GROUPS. hermes_constants.py and agent/secret_scope.py are imported by
a huge share of the suite, so they do not count towards ownership of the `config` group."""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
from groups import GROUPS  # noqa: E402
import map_tests  # noqa: E402,F401  (re-runs mapping into `imports`, cheap)

UBIQUITOUS = {"hermes_constants", "agent.secret_scope"}

owner: dict[str, tuple[int, str]] = {}
for g in GROUPS:
    names = {map_tests.dotted(f) for f in map_tests.srcs(g)} - UBIQUITOUS
    for t in map_tests.tests:
        n = len(map_tests.imports[t] & names)
        if n and (t not in owner or n > owner[t][0]):
            owner[t] = (n, g)

by_group: dict[str, list[str]] = {g: [] for g in GROUPS}
for t, (_, g) in owner.items():
    by_group[g].append(t)

json.dump(by_group, open(os.path.join(os.path.dirname(__file__), "ledger.json"), "w"), indent=1)
print({g: len(v) for g, v in by_group.items()}, "total", len(owner))
