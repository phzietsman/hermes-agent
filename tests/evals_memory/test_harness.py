from __future__ import annotations

from evals.memory.arms import DictArm
from evals.memory.models import ArmName
from evals.memory.run import run_all
from evals.memory.scenarios import make_facts


def test_seeded_facts_are_deterministic() -> None:
    assert make_facts(7, 5) == make_facts(7, 5)
    assert make_facts(7, 5) != make_facts(8, 5)


def test_perfect_arm_scores_full_and_lossy_arm_loses_writes() -> None:
    good = {r.scenario: r for r in run_all(DictArm(), ArmName.BASELINE, 1, True).results}
    assert good["1_recall"].rate == 1.0 and good["2_supersession"].rate == 1.0
    assert good["6_write_landing"].rate == 1.0
    lossy = {r.scenario: r for r in run_all(DictArm(drop_every=4), ArmName.BASELINE, 1, True).results}
    assert lossy["6_write_landing"].rate < 1.0
