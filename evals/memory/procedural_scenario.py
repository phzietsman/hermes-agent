"""Scenario 5: a multi-step procedure taught once must be repeatable from a terse request.

Run 1 spells out a quirky "weekly rollup" recipe. Runs 2-4 (fresh sessions, new data) only say "run the weekly
rollup on <dir>", so reproducing the output needs the recipe from memory or a skill. Correctness is a
deterministic file comparison; cost is tool calls, errors, tokens and wall time per run.
"""

from __future__ import annotations

import json
import random
import time
from pathlib import Path
from typing import Any, Protocol

from .models import ScenarioResult

FILES = ("alpha", "bravo", "charlie", "delta", "echo", "foxtrot")
RECIPE = (
    "Here is how our weekly rollup works, please do it for {dir}. The directory holds one file per store, named "
    "sales_<store>.txt, each line 'item,qty'. Sum qty per file. Write {dir}/rollup.json (JSON, keys sorted, "
    "2-space indent) with 'files': a list of {{'name': <store>, 'total': <sum>}} in alphabetical order of store, "
    "'grand_total': the sum of all totals, and 'check': grand_total modulo 97. Then tell me the grand total."
)
TERSE = "Run the weekly rollup on {dir}."


class ProceduralArm(Protocol):
    def start_session(self) -> None: ...
    def run_task(self, text: str) -> dict[str, Any]: ...
    def skills_created(self) -> int: ...


def make_week(root: Path, rng: random.Random) -> dict[str, Any]:
    root.mkdir(parents=True, exist_ok=True)
    files: list[dict[str, Any]] = []
    for store in FILES:
        lines = [(f"item{rng.randint(1, 40)}", rng.randint(1, 50)) for _ in range(rng.randint(3, 5))]
        (root / f"sales_{store}.txt").write_text("".join(f"{i},{q}\n" for i, q in lines), encoding="utf-8")
        files.append({"name": store, "total": sum(q for _, q in lines)})
    grand = sum(f["total"] for f in files)
    return {"check": grand % 97, "files": files, "grand_total": grand}


def correct(root: Path, expected: dict[str, Any]) -> bool:
    try:
        return bool(json.loads((root / "rollup.json").read_text(encoding="utf-8")) == expected)
    except (OSError, ValueError):
        return False


def procedural(arm: ProceduralArm, work: Path, seed: int, repeats: int = 3) -> list[ScenarioResult]:
    rng = random.Random(seed * 1000 + 5)
    weeks = [work / f"week{i + 1}" for i in range(repeats + 1)]
    expected = [make_week(w, rng) for w in weeks]
    first = arm.run_task(RECIPE.format(dir=weeks[0]))
    arm.start_session()  # session end: the memory provider flushes and any background review runs
    created = arm.skills_created()
    ok = 0
    sums: dict[str, int] = {}
    for w, exp in zip(weeks[1:], expected[1:]):
        arm.start_session()
        t0 = time.monotonic()
        m = arm.run_task(TERSE.format(dir=w))
        ok += correct(w, exp)
        for k, v in {**m, "ms": int((time.monotonic() - t0) * 1000)}.items():
            sums[k] = sums.get(k, 0) + int(v)
    arm.start_session()
    detail = {"run1_correct": int(correct(weeks[0], expected[0])), "run1_calls": int(first["calls"]),
              "run1_tokens": int(first["tokens"]), "skills_created": created,
              **{f"reuse_{k}": v for k, v in sums.items()}}
    return [ScenarioResult(scenario="5_procedural", passed=ok, total=repeats, seed=seed, detail=detail)]
