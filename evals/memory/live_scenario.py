"""Run ONE scenario in this process. The orchestrator launches it with HERMES_HOME/HOME isolated."""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from .judge import JevJudge, JudgeScorer
from .live import LiveArm
from .models import ScenarioResult
from .scenarios import abstention, recall_across_sessions, supersession, write_landing

SIZES = {"recall": (8, 4), "supersession": (4, 0), "abstention": (4, 0), "write_landing": (8, 0)}


def run(name: str, arm: LiveArm, seed: int) -> ScenarioResult:
    scorer = JudgeScorer(JevJudge())
    n, sessions = SIZES[name]
    if name == "recall":
        return recall_across_sessions(arm, seed, n, sessions, scorer, "code")
    if name == "supersession":
        return supersession(arm, seed, n, scorer, "code")
    if name == "abstention":
        return abstention(arm, seed, n, scorer, "code")
    return write_landing(arm, seed, n, "code")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--scenario", choices=sorted(SIZES), required=True)
    ap.add_argument("--home", type=Path, required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--seed", type=int, default=1234)
    ns = ap.parse_args()
    arm = LiveArm(ns.home, ns.model)
    result = run(ns.scenario, arm, ns.seed)
    print("TOOLS " + json.dumps({"tell": dict(arm.tell_tools), "ask": dict(arm.ask_tools), "files": arm.memory_files()}))
    print("RESULT " + result.model_dump_json())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
