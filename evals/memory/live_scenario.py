"""Run ONE scenario in this process. The orchestrator launches it with HERMES_HOME/HOME isolated."""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from .aside_scenarios import aside_abstention, aside_recall, aside_supersession
from .judge import JevJudge, JudgeScorer
from .live import LiveArm
from .live_sam import LiveSamArm
from .models import ScenarioResult

SCENARIOS = ("recall", "supersession", "abstention")


def run(name: str, arm: LiveArm, seed: int, sizes: tuple[int, int, int] = (8, 4, 4)) -> list[ScenarioResult]:
    scorer = JudgeScorer(JevJudge())
    if name == "recall":
        return aside_recall(arm, seed, n=sizes[0], per_session=2, scorer=scorer)
    if name == "supersession":
        return aside_supersession(arm, seed, n=sizes[1], scorer=scorer)
    return aside_abstention(arm, seed, n=sizes[2], scorer=scorer)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--scenario", choices=SCENARIOS, required=True)
    ap.add_argument("--home", type=Path, required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--seed", type=int, required=True)
    ap.add_argument("--sizes", default="8,4,4", help="questions per seed: recall,supersession,abstention (max 10)")
    ap.add_argument("--sam", action="store_true", help="Agent Memory as the memory provider")
    ns = ap.parse_args()
    arm = (LiveSamArm if ns.sam else LiveArm)(ns.home, ns.model)
    a, b, c = (int(x) for x in ns.sizes.split(","))
    results = run(ns.scenario, arm, ns.seed, (a, b, c))
    print("TOOLS " + json.dumps({"tell": dict(arm.tell_tools), "ask": dict(arm.ask_tools), "files": arm.memory_files()}))
    for r in results:
        print("RESULT " + r.model_dump_json())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
