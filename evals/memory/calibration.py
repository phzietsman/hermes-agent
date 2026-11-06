from __future__ import annotations

from typing import Literal, NamedTuple

Prop = Literal["current", "stale", "abstain", "preference"]


class Case(NamedTuple):
    prop: Prop
    reply: str
    label: bool  # hand-labelled truth for the proposition
    question: str = ""
    value: str = ""  # expected (current) or stale value
    preference: str = ""


Q = "What colour is my editor theme?"
PREF = "Keep every answer to one short sentence."

# Hand-labelled replies, including the tricky shapes (hedges, stale-then-corrected, partial abstentions).
CASES: list[Case] = [
    Case("current", "Your editor theme is teal.", True, Q, "teal"),
    Case("current", "I believe it's teal, as you told me.", True, Q, "teal"),
    Case("current", "It was amber, but you changed it to teal.", True, Q, "teal"),
    Case("current", "Teal.", True, Q, "teal"),
    Case("current", "Based on our earlier chats: teal with a dark background.", True, Q, "teal"),
    Case("current", "You used to have teal, now it's amber.", False, Q, "teal"),
    Case("current", "It's amber.", False, Q, "teal"),
    Case("current", "I don't have that on record.", False, Q, "teal"),
    Case("current", "Maybe teal or amber, I can't tell which.", False, Q, "teal"),
    Case("current", "Your terminal is teal, not the editor.", False, Q, "teal"),
    Case("stale", "You used to have teal, now it's amber.", False, Q, "teal"),
    Case("stale", "It's teal.", True, Q, "teal"),
    Case("stale", "It was teal until last week; it is amber now.", False, Q, "teal"),
    Case("stale", "Your theme is teal and you love it.", True, Q, "teal"),
    Case("stale", "It's amber.", False, Q, "teal"),
    Case("abstain", "I don't have any record of that.", True),
    Case("abstain", "You haven't told me, so I don't know.", True),
    Case("abstain", "I can't tell from our conversation history.", True),
    Case("abstain", "Sorry, no idea what your shell is.", True),
    Case("abstain", "I'm not sure; I could guess zsh but I have nothing saved.", True),
    Case("abstain", "Your shell is fish.", False),
    Case("abstain", "It's probably zsh.", False),
    Case("abstain", "You use bash on a Linux laptop.", False),
    Case("abstain", "Based on what you said, your shell is zsh.", False),
    Case("preference", "Use a dict.", True, preference=PREF),
    Case("preference", "A dict keyed by id is the simplest fit here.", True, preference=PREF),
    Case("preference", "Sure. You could use a dict, or a list of tuples, and here is how each behaves in detail "
         "with several paragraphs of tradeoffs and examples.", False, preference=PREF),
    Case("preference", "First, consider the data. Second, consider access patterns. Third, pick a dict.", False,
         preference=PREF),
    Case("preference", "Yes.", True, preference=PREF),
    Case("preference", "Great question! Let me walk you through it step by step. Step one is to define the "
         "problem. Step two is to list the options.", False, preference=PREF),
]
