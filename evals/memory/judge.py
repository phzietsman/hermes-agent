from __future__ import annotations

from typing import Protocol

from typesafe_sdk import Noul, TypeSafeClient

from .models import Fact

# Versioned id, never an alias: `jev-latest` moves on release and would silently shift every score.
JUDGE_MODEL = "jev-1.14.0"

YES = 0.7  # P(yes) at or above this counts as yes; tuned by `python -m evals.memory.calibrate`
NO = 0.3  # at or below counts as no; in between is "uncertain" and scored as a miss


class Judge(Protocol):
    """Probability (0..1) that each proposition holds for a reply. Injected so tests never hit the network."""

    def states_current_value(self, question: str, expected: str, reply: str) -> float: ...
    def states_stale_value(self, question: str, stale: str, reply: str) -> float: ...
    def abstains(self, reply: str) -> float: ...
    def follows_preference(self, preference: str, reply: str) -> float: ...


class JevJudge:
    """Jev (TypeSafe System One) Noul judgments. Credentials come from TYPESAFE_API_KEY."""

    def __init__(self, client: TypeSafeClient | None = None) -> None:
        self._client = client or TypeSafeClient()

    def _noul(self, state: dict[str, str], instructions: str) -> float:
        # The SDK's overloaded signature carries partially-unknown generics in its own annotations.
        response = self._client.system_one(  # pyright: ignore[reportUnknownMemberType]
            state=state, questions={"q": Noul(instructions=instructions)}, model=JUDGE_MODEL
        )
        return float(response.nouls["q"].noul)

    def states_current_value(self, question: str, expected: str, reply: str) -> float:
        return self._noul(
            {"question": question, "expected": expected, "reply": reply},
            "Does `reply` assert that the current answer to `question` is `expected`? "
            "Mentioning `expected` only as an outdated or rejected value is a no.",
        )

    def states_stale_value(self, question: str, stale: str, reply: str) -> float:
        return self._noul(
            {"question": question, "stale": stale, "reply": reply},
            "Does `reply` assert that the current answer to `question` is `stale`? "
            "Saying it used to be `stale` but changed is a no.",
        )

    def abstains(self, reply: str) -> float:
        return self._noul(
            {"reply": reply},
            "Does `reply` say the assistant does not know, has no record, or cannot answer, "
            "instead of giving a specific answer?",
        )

    def follows_preference(self, preference: str, reply: str) -> float:
        return self._noul(
            {"preference": preference, "reply": reply},
            "Does `reply` comply with the user's stated `preference`?",
        )


def verdict(p: float) -> bool | None:
    """True/False when confident, None when in the uncertain band."""
    if p >= YES:
        return True
    if p <= NO:
        return False
    return None


class JudgeScorer:
    """Scorer over a Judge: uncertain verdicts count as a miss so noise never inflates a score."""

    def __init__(self, judge: Judge) -> None:
        self._judge = judge

    def recalled(self, fact: Fact, reply: str | None) -> bool:
        if reply is None:
            return False
        return verdict(self._judge.states_current_value(fact.question, fact.answer, reply)) is True

    def abstained(self, reply: str | None) -> bool:
        if reply is None:
            return True
        return verdict(self._judge.abstains(reply)) is True
