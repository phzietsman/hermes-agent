"""Run ONE scenario in this process. The orchestrator launches it with HERMES_HOME/HOME isolated."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

from .aside_scenarios import aside_abstention, aside_recall, aside_supersession
from .judge import JevJudge, JudgeScorer
from .live import LiveArm
from .live_sam import LiveSamArm
from .models import ScenarioResult
from .preference_scenario import preference_following

SCENARIOS = ("recall", "supersession", "abstention", "preference", "procedural")


def run(name: str, arm: LiveArm, seed: int, sizes: tuple[int, int, int, int] = (8, 4, 4, 3)) -> list[ScenarioResult]:
    scorer = JudgeScorer(JevJudge())
    if name == "recall":
        return aside_recall(arm, seed, n=sizes[0], per_session=2, scorer=scorer)
    if name == "supersession":
        return aside_supersession(arm, seed, n=sizes[1], scorer=scorer)
    if name == "abstention":
        return aside_abstention(arm, seed, n=sizes[2], scorer=scorer)
    if name == "preference":
        return preference_following(arm, seed, n=sizes[3], scorer=scorer)
    raise ValueError(name)


def run_procedural(sam: bool, home: Path, model: str, seed: int) -> list[ScenarioResult]:
    from .proc_arm import ProcArm, ProcSamArm
    from .procedural_scenario import procedural

    work = home / "work"
    work.mkdir(parents=True, exist_ok=True)
    os.environ["TERMINAL_CWD"] = str(work)
    arm = (ProcSamArm if sam else ProcArm)(home, model)
    return procedural(arm, work, seed)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--scenario", choices=SCENARIOS, required=True)
    ap.add_argument("--home", type=Path, required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--seed", type=int, required=True)
    ap.add_argument("--sizes", default="8,4,4,3", help="per seed: recall,supersession,abstention (max 10), preferences (max 3)")
    ap.add_argument("--sam", action="store_true", help="Agent Memory as the memory provider")
    ns = ap.parse_args()
    if ns.scenario == "procedural":
        for r in run_procedural(ns.sam, ns.home, ns.model, ns.seed):
            print("RESULT " + r.model_dump_json())
        return 0
    arm = (LiveSamArm if ns.sam else LiveArm)(ns.home, ns.model)
    a, b, c, d = (int(x) for x in ns.sizes.split(","))
    results = run(ns.scenario, arm, ns.seed, (a, b, c, d))
    print("TOOLS " + json.dumps({"tell": dict(arm.tell_tools), "ask": dict(arm.ask_tools), "files": arm.memory_files()}))
    for r in results:
        print("RESULT " + r.model_dump_json())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
