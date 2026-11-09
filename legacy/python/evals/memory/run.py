from __future__ import annotations

import argparse
import json
from pathlib import Path

from .arms import DictArm, MemoryArm
from .models import ArmName, Report
from .resilience import resilience
from .scenarios import abstention, live_placeholders, recall_across_sessions, supersession, write_landing


def run_all(arm: MemoryArm, arm_name: ArmName, seed: int, harness_only: bool) -> Report:
    results = [
        recall_across_sessions(arm, seed),
        supersession(arm, seed),
        abstention(arm, seed),
        write_landing(arm, seed),
        resilience(seed),
        *live_placeholders(),
    ]
    return Report(arm=arm_name, seed=seed, harness_only=harness_only, results=results)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="Run memory evals")
    ap.add_argument("--arm", type=ArmName, default=ArmName.BASELINE)
    ap.add_argument("--seed", type=int, default=1234)
    ap.add_argument("--out", type=Path, required=True)
    ns = ap.parse_args(argv)
    # No live agent arm is wired yet: DictArm validates the harness only.
    # Replace with a live arm before using numbers for ship gates.
    report = run_all(DictArm(), ns.arm, ns.seed, harness_only=True)
    ns.out.write_text(report.model_dump_json(indent=2) + "\n", encoding="utf-8")
    print(json.dumps({r.scenario: r.rate for r in report.results}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
