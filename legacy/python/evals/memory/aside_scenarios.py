"""Live scenarios where durable facts arrive as asides inside small task requests.

A model has little reason to save "shell-0 is cobalt-416" and every reason to save what it was
explicitly told to remember, so neither tests the learning loop. A fact dropped in passing during
an unrelated task is what the background review exists to catch.
"""

from __future__ import annotations

import random
import re
from typing import NamedTuple

from .arms import MemoryArm
from .models import Fact, ScenarioResult
from .scenarios import Scorer, make_value


class Template(NamedTuple):
    key: str
    task: str
    aside: str  # contains {v}
    question: str


TEMPLATES: list[Template] = [
    Template("staging", "In one sentence, what does `git rebase --onto` do?",
             "Side note: our staging cluster is called {v}.", "What is our staging cluster called?"),
    Template("oncall", "How do I match a three-digit number with a regex?",
             "Also, our on-call channel is #{v}.", "Which channel is our on-call channel?"),
    Template("branch", "Briefly, what is the difference between a mutex and a semaphore?",
             "By the way, we cut releases from the {v} branch.", "Which branch do we cut releases from?"),
    Template("vpn", "How do I list files sorted by size on macOS?",
             "FYI I always connect through the VPN profile named {v}.", "Which VPN profile do I connect through?"),
    Template("billingdb", "Give me a one-line Python snippet that reverses a string.",
             "My Postgres database for the billing service is named {v}.",
             "What is my Postgres database for the billing service named?"),
    Template("jira", "What does HTTP status 429 mean?",
             "For future reference, our team's Jira project key is {v}.", "What is our team's Jira project key?"),
    Template("registry", "Explain DNS TTL in one line.",
             "Our internal package registry lives at {v}.example.internal.",
             "Where does our internal package registry live?"),
    Template("buildhost", "What is the shortcut to comment a line in VS Code on macOS?",
             "Oh, and the build server's hostname is {v}.", "What is the build server's hostname?"),
    Template("assettag", "What is the difference between `==` and `is` in Python, in a sentence?",
             "My laptop's asset tag is {v}.", "What is my laptop's asset tag?"),
    Template("sprint", "Give me a one-line shell command to count lines in a file.",
             "We're running the {v} sprint right now.", "Which sprint are we running right now?"),
]

CODE = re.compile(r"[a-z]+-\d{3}")


def codeword(text: str) -> str | None:
    m = CODE.search(text)
    return m.group(0) if m else None


def _facts(seed: int, n: int, offset: int = 0) -> list[tuple[Template, Fact]]:
    rng = random.Random(seed * 1000 + offset)
    chosen = rng.sample(TEMPLATES, n)
    out: list[tuple[Template, Fact]] = []
    for t in chosen:
        v = make_value(rng, "code")
        out.append((t, Fact(key=t.key, statement=f"{t.task} {t.aside.format(v=v)}", question=t.question, answer=v)))
    return out


def aside_recall(arm: MemoryArm, seed: int, n: int, per_session: int, scorer: Scorer) -> list[ScenarioResult]:
    """Tell n facts as asides (per_session per session), then ask each in a fresh session.
    Returns recall (1) and write-landing (6): landing is judged from the arm's persisted store."""
    facts = _facts(seed, n)
    for i, (_, f) in enumerate(facts):
        if i and i % per_session == 0:
            arm.start_session()
        arm.tell(f.key, f.statement)
    arm.start_session()
    passed = 0
    for _, f in facts:
        arm.start_session()  # one question per session: no in-context leakage between answers
        passed += scorer.recalled(f, arm.ask(f.key, f.question))
    landed, claimed = arm.writes_landed(), arm.writes_claimed()
    detail = arm.landing_detail()
    return [
        ScenarioResult(scenario="1_recall", passed=passed, total=n, seed=seed),
        ScenarioResult(scenario="6_write_landing", passed=landed, total=claimed, seed=seed, detail=detail),
    ]


def aside_supersession(arm: MemoryArm, seed: int, n: int, scorer: Scorer) -> list[ScenarioResult]:
    old = _facts(seed, n)
    new = _facts(seed, n, offset=7)  # same templates in the same order, different values
    for (_, f) in old:
        arm.tell(f.key, f.statement)
    arm.start_session()
    updated: list[Fact] = []
    for (t, f), (_, g) in zip(old, new):
        stmt = f"{t.task} Correction to what I said before: {t.aside.format(v=g.answer)} The old one is retired."
        arm.tell(t.key, stmt)
        updated.append(f.model_copy(update={"answer": g.answer}))
    arm.start_session()
    passed = 0
    for f in updated:
        arm.start_session()
        passed += scorer.recalled(f, arm.ask(f.key, f.question))
    return [ScenarioResult(scenario="2_supersession", passed=passed, total=n, seed=seed)]


def aside_abstention(arm: MemoryArm, seed: int, n: int, scorer: Scorer) -> list[ScenarioResult]:
    facts = _facts(seed, n, offset=13)
    passed = 0
    for _, f in facts:
        arm.start_session()
        passed += scorer.abstained(arm.ask(f.key, f.question))
    return [ScenarioResult(scenario="3_abstention", passed=passed, total=n, seed=seed)]
