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


def test_aside_scenarios_run_on_the_fake_arm_and_use_natural_facts() -> None:
    from evals.memory.aside_scenarios import aside_abstention, aside_recall, aside_supersession, codeword
    from evals.memory.scenarios import SubstringScorer

    class Fake(DictArm):
        # DictArm stores statements; return the value so SubstringScorer can match it.
        def ask(self, key: str, question: str) -> str | None:
            s = super().ask(key, question)
            return codeword(s) if s else None

    r1, r6 = aside_recall(Fake(), 1, n=6, per_session=2, scorer=SubstringScorer())
    assert (r1.passed, r1.total) == (6, 6) and r6.rate == 1.0
    assert aside_supersession(Fake(), 1, n=4, scorer=SubstringScorer())[0].rate == 1.0
    assert aside_abstention(Fake(), 1, n=4, scorer=SubstringScorer())[0].rate == 1.0
    # an aside is a task request plus a durable fact, not a bare "X is Y" pair
    from evals.memory.aside_scenarios import _facts  # noqa: PLC2701
    assert all("?" in f.statement or "." in f.statement and len(f.statement.split()) > 8 for _, f in _facts(1, 4))
