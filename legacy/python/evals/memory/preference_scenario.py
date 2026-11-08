"""Scenario 4: a preference stated once must still be followed five sessions later, with no reminder.

Each preference carries a run-unique codeword, so a follow can only come from memory, and so the SAM arm can
admit exactly this run's profile entries (the core profile is context-wide; see live_sam).
"""

from __future__ import annotations

import random
from typing import NamedTuple

from .arms import MemoryArm
from .models import ScenarioResult
from .scenarios import Scorer, make_value


class PrefTemplate(NamedTuple):
    key: str
    statement: str  # contains {v}; said once, in passing
    preference: str  # what the judge checks the reply against
    ask: str  # a later, unrelated request


# Compatible with each other, so all can be active at once without conflict.
TEMPLATES: list[PrefTemplate] = [
    PrefTemplate("signoff", "Quick request while we chat: from now on always finish every reply with the exact phrase 'over and out {v}'.",
                 "Every reply must finish with the exact phrase 'over and out {v}'.", "What does the HTTP status code 404 mean?"),
    PrefTemplate("address", "By the way, please always address me as Commander {v} when you reply.",
                 "Every reply must address the user as 'Commander {v}'.", "Give me a one-line shell command that counts the lines in a file."),
    PrefTemplate("tldr", "One more standing preference: begin every reply with a line starting 'TLDR {v}:' giving the answer in a few words.",
                 "Every reply must begin with a line starting 'TLDR {v}:'.", "What is the difference between a process and a thread?"),
]

FILLER = [
    "In one sentence, what is a hash map?",
    "What does `git stash` do?",
    "Name one difference between TCP and UDP.",
    "What does the `chmod 755` command do?",
    "What is a race condition, briefly?",
]


def preference_following(arm: MemoryArm, seed: int, n: int, scorer: Scorer) -> list[ScenarioResult]:
    rng = random.Random(seed * 1000 + 4)
    chosen = TEMPLATES[:n]
    values = [make_value(rng, "code") for _ in chosen]
    tags = list(values)
    allow = getattr(arm, "allow_profile", None)
    if callable(allow):
        allow(tags)
    for t, v in zip(chosen, values):
        arm.tell(t.key, t.statement.format(v=v))
    for task in FILLER:  # five sessions of unrelated work, none restating the preference
        arm.start_session()
        arm.ask("filler", task)
    passed = 0
    for t, v in zip(chosen, values):
        arm.start_session()
        reply = arm.ask(t.key, t.ask)
        passed += scorer.followed(t.preference.format(v=v), reply)
    served = getattr(arm, "profile_entries_served", None)
    detail = {"profile_entries_served": int(served())} if callable(served) else {}
    return [ScenarioResult(scenario="4_preference_following", passed=passed, total=len(chosen), seed=seed, detail=detail)]

