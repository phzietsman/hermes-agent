"""Map each group to the test files that import its modules. Writes mapping.json.

Parses each test file once with ast and collects every dotted module name it imports
(including `from pkg import mod` forms and string targets of the form "pkg.mod.attr"
used with monkeypatch/mock.patch)."""
import ast
import glob
import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(__file__))
from groups import GROUPS  # noqa: E402

ROOT = "/Users/paulz/sourcery/phzietsman/hermes-agent"
os.chdir(ROOT)

STR_TARGET = re.compile(r"^[A-Za-z_][\w]*(\.[A-Za-z_]\w*)+$")


def srcs(group: str) -> list[str]:
    out: list[str] = []
    for pat in GROUPS[group]:
        out += [p for p in glob.glob(pat) if p.endswith(".py") and os.path.basename(p) != "__init__.py"]
    return sorted(set(out))


def dotted(path: str) -> str:
    return path[:-3].replace("/", ".")


def imported(path: str) -> set[str]:
    try:
        tree = ast.parse(open(path, encoding="utf-8", errors="replace").read())
    except SyntaxError:
        return set()
    mods: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            mods.update(a.name for a in node.names)
        elif isinstance(node, ast.ImportFrom) and node.module and node.level == 0:
            mods.add(node.module)
            mods.update(f"{node.module}.{a.name}" for a in node.names)
        elif isinstance(node, ast.Constant) and isinstance(node.value, str) and STR_TARGET.match(node.value):
            parts = node.value.split(".")
            mods.update(".".join(parts[:i]) for i in range(2, len(parts) + 1))
    return mods


tests = sorted(glob.glob("tests/**/test_*.py", recursive=True))
imports = {t: imported(t) for t in tests}

mapping: dict[str, dict] = {}
for g in GROUPS:
    files = srcs(g)
    names = {dotted(f) for f in files}
    hit = [t for t in tests if imports[t] & names]
    mapping[g] = {"sources": files, "tests": hit}
    loc = sum(len(open(f, encoding="utf-8").read().splitlines()) for f in files)
    print(f"{g:14s} src={len(files):3d} ({loc:6d} loc)  tests={len(hit):4d}", flush=True)

json.dump(mapping, open(os.path.join(os.path.dirname(__file__), "mapping.json"), "w"), indent=1)
