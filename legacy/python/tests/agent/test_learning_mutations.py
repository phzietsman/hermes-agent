"""Behavior contracts for journey node edit/delete (agent.learning_mutations).

Exercises the real on-disk resolution (skills dir; memory ids are refused)
against a temp HERMES_HOME, never mocks — the id→file mapping is the whole point.
"""

from __future__ import annotations


import pytest

from agent import learning_mutations as lm
from hermes_constants import get_hermes_home

_SKILL = """---
name: my-skill
description: A test skill.
---

# My Skill

Body.
"""


@pytest.fixture
def home():
    base = get_hermes_home()
    (base / "memories").mkdir(parents=True, exist_ok=True)
    (base / "memories" / "MEMORY.md").write_text("alpha note\nline two\n§\nbeta note", encoding="utf-8")
    (base / "memories" / "USER.md").write_text("user profile note", encoding="utf-8")
    skill = base / "skills" / "my-skill"
    skill.mkdir(parents=True, exist_ok=True)
    (skill / "SKILL.md").write_text(_SKILL, encoding="utf-8")
    return base


def test_parse_node_kind():
    assert lm.parse_node_kind("memory:memory:0") == "memory"
    assert lm.parse_node_kind("memory:profile:3") == "memory"
    assert lm.parse_node_kind("debugging-hermes") == "skill"








def test_skill_detail_returns_skill_md(home):
    d = lm.node_detail("my-skill")
    assert d["ok"] and d["kind"] == "skill"
    assert "name: my-skill" in d["content"]




def test_delete_pinned_skill_refused(home):
    from tools import skill_usage

    skill_usage.set_pinned("my-skill", True)
    res = lm.delete_node("my-skill")
    assert not res["ok"]
    assert "pinned" in res["message"]
    assert (home / "skills" / "my-skill").exists()






@pytest.mark.parametrize("mutate", [lambda: lm.delete_node("memory:memory:0"),
                                    lambda: lm.edit_node("memory:memory:0", "x"),
                                    lambda: lm.node_detail("memory:profile:0")],
                         ids=["delete", "edit", "detail"])
def test_memory_ids_are_refused_and_legacy_files_left_untouched(home, mutate):
    path = home / "memories" / "MEMORY.md"
    before = path.read_bytes()

    res = mutate()

    assert not res["ok"] and "Agent Memory" in res["message"]
    assert path.read_bytes() == before
