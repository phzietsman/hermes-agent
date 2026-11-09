from __future__ import annotations

from evals.memory.calibrate import score
from evals.memory.calibration import CASES, Case
from evals.memory.judge import verdict


class OracleJudge:
    """Returns the hand label as a probability, so the wiring (not Jev) is under test."""

    def __init__(self) -> None:
        self.calls: list[str] = []

    def _p(self, case_label: bool) -> float:
        return 0.95 if case_label else 0.05

    def states_current_value(self, question: str, expected: str, reply: str) -> float:
        self.calls.append("current")
        return self._lookup("current", reply)

    def states_stale_value(self, question: str, stale: str, reply: str) -> float:
        self.calls.append("stale")
        return self._lookup("stale", reply)

    def abstains(self, reply: str) -> float:
        self.calls.append("abstain")
        return self._lookup("abstain", reply)

    def follows_preference(self, preference: str, reply: str) -> float:
        self.calls.append("preference")
        return self._lookup("preference", reply)

    def _lookup(self, prop: str, reply: str) -> float:
        label = next(c.label for c in CASES if c.prop == prop and c.reply == reply)
        return self._p(label)


def test_verdict_bands() -> None:
    assert verdict(0.7) is True and verdict(0.95) is True
    assert verdict(0.3) is False and verdict(0.01) is False
    assert verdict(0.5) is None


def test_every_proposition_routes_to_its_judge_method() -> None:
    judge = OracleJudge()
    results = [(c, verdict(score(judge, c))) for c in CASES]
    assert all(v is c.label for c, v in results)
    assert set(judge.calls) == {"current", "stale", "abstain", "preference"}


def test_calibration_set_covers_each_proposition_with_both_labels() -> None:
    by: dict[tuple[str, bool], int] = {}
    for c in CASES:
        by[(c.prop, c.label)] = by.get((c.prop, c.label), 0) + 1
    for prop in ("current", "stale", "abstain", "preference"):
        assert by.get((prop, True), 0) >= 2 and by.get((prop, False), 0) >= 2, prop


def test_case_is_hashable_tuple() -> None:
    assert isinstance(CASES[0], Case)
