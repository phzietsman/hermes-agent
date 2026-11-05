"""Run the live scenarios against one checkout for several seeds and write a Report.

    python -m evals.memory.live_run --arm baseline --checkout <path> --python <py> --seeds 1,2,3 --out r.json

The checkout decides which Hermes is under test (main worktree = baseline, this branch = loopfix).
Credentials are inherited from the environment (ANTHROPIC_API_KEY, TYPESAFE_API_KEY), never written.
"""

from __future__ import annotations

import argparse
import os
import statistics
import subprocess
import tempfile
from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from .live_scenario import SCENARIOS
from .models import ArmName, Report, ScenarioResult

MODEL = "claude-haiku-5-5"


def run_one(python: str, checkout: Path, name: str, seed: int, root: Path, sam: bool = False, sizes: str = "8,4,4,3") -> list[ScenarioResult]:
    base = root / f"{name}-{seed}"
    home = base / "hermes"
    (base / "home").mkdir(parents=True)
    home.mkdir(parents=True)
    env = {**os.environ, "HERMES_HOME": str(home), "HOME": str(base / "home")}
    proc = subprocess.run(
        [python, "-m", "evals.memory.live_scenario", "--scenario", name, "--home", str(home),
         "--model", MODEL, "--seed", str(seed), "--sizes", sizes, *(["--sam"] if sam else [])],
        cwd=checkout, env=env, capture_output=True, text=True, timeout=3600, check=False,
    )
    results = [ScenarioResult.model_validate_json(line[len("RESULT "):])
               for line in proc.stdout.splitlines() if line.startswith("RESULT ")]
    if not results:
        raise RuntimeError(f"{name} seed={seed} failed (exit {proc.returncode}):\n{proc.stderr[-2000:]}")
    return results


def summarize(results: list[ScenarioResult]) -> dict[str, str]:
    by: dict[str, list[ScenarioResult]] = defaultdict(list)
    for r in results:
        by[r.scenario].append(r)
    out: dict[str, str] = {}
    for name, rs in sorted(by.items()):
        rates = [r.rate for r in rs]
        per_seed = " ".join(f"{r.passed}/{r.total}" for r in sorted(rs, key=lambda x: x.seed))
        out[name] = f"mean {statistics.mean(rates):.0%} (min {min(rates):.0%}, max {max(rates):.0%})  [{per_seed}]"
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--arm", type=ArmName, required=True)
    ap.add_argument("--checkout", type=Path, required=True)
    ap.add_argument("--python", required=True)
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--seeds", default="1,2,3")
    ap.add_argument("--sizes", default="8,4,4,3", help="per seed: recall,supersession,abstention,preferences")
    ap.add_argument("--only", choices=SCENARIOS, default=None, help="run a single scenario")
    ap.add_argument("--workers", type=int, default=3)
    ns = ap.parse_args()
    seeds = [int(s) for s in ns.seeds.split(",")]
    with tempfile.TemporaryDirectory(prefix="memeval-") as tmp, ThreadPoolExecutor(max_workers=ns.workers) as pool:
        futures = [pool.submit(run_one, ns.python, ns.checkout, n, s, Path(tmp), ns.arm is ArmName.LOOPFIX_SAM, ns.sizes) for n in ([ns.only] if ns.only else SCENARIOS) for s in seeds]
        results = [r for f in futures for r in f.result()]
    report = Report(arm=ns.arm, seed=seeds[0], harness_only=False, results=results)
    ns.out.write_text(report.model_dump_json(indent=2) + "\n", encoding="utf-8")
    for name, line in summarize(results).items():
        print(f"{ns.arm.value:9} {name:16} {line}")
    landing = [r for r in results if r.scenario == "6_write_landing"]
    if landing:
        tot = {k: sum(r.detail.get(k, 0) for r in landing) for k in sorted({k for r in landing for k in r.detail})}
        print(f"{ns.arm.value:9} landing attribution (all seeds): {tot}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
