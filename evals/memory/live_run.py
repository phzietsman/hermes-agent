"""Run the live scenarios against one checkout and write a Report.

    python -m evals.memory.live_run --arm baseline --checkout <path> --python <py> --out report.json

The checkout decides which Hermes is under test (main worktree = baseline, this branch = loopfix).
Credentials are inherited from the environment (ANTHROPIC_API_KEY, TYPESAFE_API_KEY), never written.
"""

from __future__ import annotations

import argparse
import os
import subprocess
import tempfile
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from .live_scenario import SIZES
from .models import ArmName, Report, ScenarioResult

MODEL = "claude-haiku-5-5"
NUMBER = {"recall": "1_recall", "supersession": "2_supersession", "abstention": "3_abstention", "write_landing": "6_write_landing"}


def run_one(python: str, checkout: Path, name: str, seed: int, root: Path) -> ScenarioResult:
    home = root / name / "hermes"
    (root / name / "home").mkdir(parents=True)
    home.mkdir(parents=True)
    env = {**os.environ, "HERMES_HOME": str(home), "HOME": str(root / name / "home")}
    proc = subprocess.run(
        [python, "-m", "evals.memory.live_scenario", "--scenario", name, "--home", str(home),
         "--model", MODEL, "--seed", str(seed)],
        cwd=checkout, env=env, capture_output=True, text=True, timeout=3600, check=False,
    )
    for line in proc.stdout.splitlines():
        if line.startswith("RESULT "):
            return ScenarioResult.model_validate_json(line[len("RESULT "):])
    raise RuntimeError(f"{name} failed (exit {proc.returncode}):\n{proc.stderr[-2000:]}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--arm", type=ArmName, required=True)
    ap.add_argument("--checkout", type=Path, required=True)
    ap.add_argument("--python", required=True)
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--seed", type=int, default=1234)
    ns = ap.parse_args()
    with tempfile.TemporaryDirectory(prefix="memeval-") as tmp, ThreadPoolExecutor(max_workers=len(SIZES)) as pool:
        futures = {n: pool.submit(run_one, ns.python, ns.checkout, n, ns.seed, Path(tmp)) for n in SIZES}
        results = [f.result().model_copy(update={"scenario": NUMBER[n]}) for n, f in futures.items()]
    report = Report(arm=ns.arm, seed=ns.seed, harness_only=False, results=results)
    ns.out.write_text(report.model_dump_json(indent=2) + "\n", encoding="utf-8")
    print({r.scenario: f"{r.passed}/{r.total}" for r in results})
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
