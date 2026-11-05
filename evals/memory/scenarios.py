from __future__ import annotations

import random
from typing import Literal, Protocol

from .arms import MemoryArm
from .models import Fact, ScenarioResult

ValueStyle = Literal["color", "code"]

_COLORS = ["teal", "amber", "violet", "crimson", "olive", "indigo", "coral", "slate"]
_THINGS = ["editor", "shell", "mascot", "laptop", "deploy region", "test runner"]


_WORDS = ["zephyr", "quartz", "marlin", "tundra", "ember", "falcon", "cobalt", "juniper"]


def make_value(rng: random.Random, style: ValueStyle, avoid: str = "") -> str:
    """"code" values (zephyr-417) cannot be guessed by a model; "color" values can (1 in 8)."""
    while True:
        v = rng.choice(_COLORS) if style == "color" else f"{rng.choice(_WORDS)}-{rng.randint(100, 999)}"
        if v != avoid:
            return v


class Scorer(Protocol):
    def recalled(self, fact: Fact, reply: str | None) -> bool: ...
    def abstained(self, reply: str | None) -> bool: ...


class SubstringScorer:
    """Exact-token scoring: valid for arms that return stored text (the fake arm), not live prose."""

    def recalled(self, fact: Fact, reply: str | None) -> bool:
        return reply is not None and fact.answer in reply

    def abstained(self, reply: str | None) -> bool:
        return reply is None


def make_facts(seed: int, n: int, style: ValueStyle = "color") -> list[Fact]:
    rng = random.Random(seed)
    facts: list[Fact] = []
    for i in range(n):
        thing = f"{rng.choice(_THINGS)}-{i}"
        value = make_value(rng, style)
        facts.append(
            Fact(
                key=thing,
                statement=f"My {thing} is {value}.",
                question=f"What is my {thing}?",
                answer=value,
            )
        )
    return facts


def recall_across_sessions(
    arm: MemoryArm, seed: int, n_facts: int = 30, sessions: int = 10,
    scorer: Scorer = SubstringScorer(), style: ValueStyle = "color",
) -> ScenarioResult:
    facts = make_facts(seed, n_facts, style)
    for i, f in enumerate(facts):
        if i % max(1, n_facts // sessions) == 0:
            arm.start_session()
        arm.tell(f.key, f.statement)
    arm.start_session()
    passed = sum(scorer.recalled(f, arm.ask(f.key, f.question)) for f in facts)
    return ScenarioResult(scenario="1_recall", passed=passed, total=len(facts))


def supersession(
    arm: MemoryArm, seed: int, n_facts: int = 10, scorer: Scorer = SubstringScorer(), style: ValueStyle = "color",
) -> ScenarioResult:
    facts = make_facts(seed, n_facts, style)
    rng = random.Random(seed + 1)
    updated: list[Fact] = []
    for f in facts:
        arm.tell(f.key, f.statement)
    arm.start_session()
    for f in facts:
        new = make_value(rng, style, avoid=f.answer)
        nf = f.model_copy(update={"statement": f"Update: my {f.key} is now {new}.", "answer": new})
        arm.tell(nf.key, nf.statement)
        updated.append(nf)
    arm.start_session()
    passed = sum(scorer.recalled(f, arm.ask(f.key, f.question)) for f in updated)
    return ScenarioResult(scenario="2_supersession", passed=passed, total=len(updated))


def abstention(
    arm: MemoryArm, seed: int, n: int = 10, scorer: Scorer = SubstringScorer(), style: ValueStyle = "color",
) -> ScenarioResult:
    facts = [f.model_copy(update={"key": f"unknown-{f.key}"}) for f in make_facts(seed + 2, n, style)]
    arm.start_session()
    # Unknown facts must yield no answer; any non-None reply is a hallucination.
    passed = sum(scorer.abstained(arm.ask(f.key, f.question)) for f in facts)
    return ScenarioResult(scenario="3_abstention", passed=passed, total=n)


def write_landing(arm: MemoryArm, seed: int, n: int = 20, style: ValueStyle = "color") -> ScenarioResult:
    for f in make_facts(seed + 3, n, style):
        arm.tell(f.key, f.statement)
    return ScenarioResult(scenario="6_write_landing", passed=arm.writes_landed(), total=arm.writes_claimed())


LIVE_ONLY = [
    "4_preference_following",
    "5_procedural",
    "8_regression",
]


def live_placeholders() -> list[ScenarioResult]:
    return [ScenarioResult(scenario=s, passed=0, total=0, requires_live=True) for s in LIVE_ONLY]
