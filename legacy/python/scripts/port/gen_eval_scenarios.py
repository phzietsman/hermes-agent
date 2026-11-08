"""Export the memory-eval scenario instances from the Python harness to JSON for the Go harness.

Python's random.Random sampling cannot be reproduced in Go, so the instances (facts, tell and ask turns, expected
answers, procedural weeks) are generated here, once, from the real Python scenario code and embedded in the Go
binary. Scenario wording is therefore the Python wording byte for byte.

usage: python scripts/port/gen_eval_scenarios.py internal/evals/data/scenarios.json
"""
import json
import pathlib
import random
import sys
import tempfile

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

from evals.memory import aside_scenarios as aside  # noqa: E402
from evals.memory import preference_scenario as pref  # noqa: E402
from evals.memory import procedural_scenario as proc  # noqa: E402
from evals.memory.scenarios import make_value  # noqa: E402

SEEDS = range(1, 7)
SIZES = {"recall": (4, 8, 10), "supersession": (4, 8, 10), "abstention": (4, 6, 10), "preference": (1, 2, 3)}


def fact(f):
    return {"key": f.key, "statement": f.statement, "question": f.question, "answer": f.answer}


def recall(seed, n):
    return {"facts": [fact(f) for _, f in aside._facts(seed, n)], "per_session": 2}


def supersession(seed, n):
    old, new = aside._facts(seed, n), aside._facts(seed, n, offset=7)
    updates = []
    for (t, f), (_, g) in zip(old, new):
        stmt = f"{t.task} Correction to what I said before: {t.aside.format(v=g.answer)} The old one is retired."
        updates.append({"key": t.key, "statement": stmt, "question": f.question, "answer": g.answer})
    return {"old": [fact(f) for _, f in old], "updates": updates}


def abstention(seed, n):
    return {"facts": [fact(f) for _, f in aside._facts(seed, n, offset=13)]}


def preference(seed, n):
    rng = random.Random(seed * 1000 + 4)
    chosen = pref.TEMPLATES[:n]
    values = [make_value(rng, "code") for _ in chosen]
    return {"items": [{"key": t.key, "statement": t.statement.format(v=v), "preference": t.preference.format(v=v), "ask": t.ask, "value": v}
                      for t, v in zip(chosen, values)], "filler": pref.FILLER}


def procedural(seed, repeats=3):
    rng = random.Random(seed * 1000 + 5)
    weeks = []
    with tempfile.TemporaryDirectory() as tmp:
        for i in range(repeats + 1):
            root = pathlib.Path(tmp) / f"week{i + 1}"
            expected = proc.make_week(root, rng)
            files = {p.name: p.read_text() for p in sorted(root.glob("sales_*.txt"))}
            weeks.append({"files": files, "expected": expected})
    return {"weeks": weeks, "recipe": proc.RECIPE, "terse": proc.TERSE}


def main(path):
    data = {}
    for seed in SEEDS:
        for n in SIZES["recall"]:
            data[f"recall/{seed}/{n}"] = recall(seed, n)
        for n in SIZES["supersession"]:
            data[f"supersession/{seed}/{n}"] = supersession(seed, n)
        for n in SIZES["abstention"]:
            data[f"abstention/{seed}/{n}"] = abstention(seed, n)
        for n in SIZES["preference"]:
            data[f"preference/{seed}/{n}"] = preference(seed, n)
        data[f"procedural/{seed}"] = procedural(seed)
    pathlib.Path(path).write_text(json.dumps(data, indent=1, ensure_ascii=False, sort_keys=True) + "\n")
    print(len(data), "instances")


if __name__ == "__main__":
    main(sys.argv[1])
