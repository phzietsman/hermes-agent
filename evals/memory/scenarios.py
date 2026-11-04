from __future__ import annotations

import random

from .arms import MemoryArm
from .models import Fact, ScenarioResult

_COLORS = ["teal", "amber", "violet", "crimson", "olive", "indigo", "coral", "slate"]
_THINGS = ["editor", "shell", "mascot", "laptop", "deploy region", "test runner"]


def make_facts(seed: int, n: int) -> list[Fact]:
    rng = random.Random(seed)
    facts: list[Fact] = []
    for i in range(n):
        thing = f"{rng.choice(_THINGS)}-{i}"
        value = rng.choice(_COLORS)
        facts.append(
            Fact(
                key=thing,
                statement=f"My {thing} is {value}.",
                question=f"What is my {thing}?",
                answer=value,
            )
        )
    return facts


def _correct(reply: str | None, fact: Fact) -> bool:
    return reply is not None and fact.answer in reply


def recall_across_sessions(arm: MemoryArm, seed: int, n_facts: int = 30, sessions: int = 10) -> ScenarioResult:
    facts = make_facts(seed, n_facts)
    for i, f in enumerate(facts):
        if i % max(1, n_facts // sessions) == 0:
            arm.start_session()
        arm.tell(f.key, f.statement)
    arm.start_session()
    passed = sum(_correct(arm.ask(f.key, f.question), f) for f in facts)
    return ScenarioResult(scenario="1_recall", passed=passed, total=len(facts))


def supersession(arm: MemoryArm, seed: int, n_facts: int = 10) -> ScenarioResult:
    facts = make_facts(seed, n_facts)
    rng = random.Random(seed + 1)
    updated: list[Fact] = []
    for f in facts:
        arm.tell(f.key, f.statement)
    arm.start_session()
    for f in facts:
        new = rng.choice([c for c in _COLORS if c != f.answer])
        nf = f.model_copy(update={"statement": f"Update: my {f.key} is now {new}.", "answer": new})
        arm.tell(nf.key, nf.statement)
        updated.append(nf)
    arm.start_session()
    passed = sum(_correct(arm.ask(f.key, f.question), f) for f in updated)
    return ScenarioResult(scenario="2_supersession", passed=passed, total=len(updated))


def abstention(arm: MemoryArm, seed: int, n: int = 10) -> ScenarioResult:
    facts = [f.model_copy(update={"key": f"unknown-{f.key}"}) for f in make_facts(seed + 2, n)]
    arm.start_session()
    # Unknown facts must yield no answer; any non-None reply is a hallucination.
    passed = sum(arm.ask(f.key, f.question) is None for f in facts)
    return ScenarioResult(scenario="3_abstention", passed=passed, total=n)


def write_landing(arm: MemoryArm, seed: int, n: int = 20) -> ScenarioResult:
    for f in make_facts(seed + 3, n):
        arm.tell(f.key, f.statement)
    return ScenarioResult(scenario="6_write_landing", passed=arm.writes_landed(), total=arm.writes_claimed())


LIVE_ONLY = [
    "4_preference_following",
    "5_procedural",
    "7_resilience",
    "8_regression",
]


def live_placeholders() -> list[ScenarioResult]:
    return [ScenarioResult(scenario=s, passed=0, total=0, requires_live=True) for s in LIVE_ONLY]
