from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from plugins.memory.agent_memory import register, skills_bridge

from .fakes import FakeClient, make, write_config


def _outbox(home: Path) -> list[dict[str, Any]]:
    path = home / "agent-memory-outbox.jsonl"
    return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []


@pytest.fixture
def home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.setenv("AGENT_MEMORY_API_KEY", "k")
    write_config(tmp_path)
    return tmp_path


def test_created_skill_becomes_a_pointer_with_kind_and_name_labels(home: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(skills_bridge, "_description", lambda name: "Ships the API. Use when deploying.")
    skills_bridge.on_skill_lifecycle(action="created", skill_name="deploy-api")
    (entry,) = _outbox(home)
    p = entry["payload"]
    assert entry["op"] == "remember" and p["text"] == "Skill 'deploy-api': Ships the API. Use when deploying."
    assert p["labels"] == ["kind=skill", "skill=deploy-api"] and p["category"] == "instructions"
    assert p["scopes"] == "agent/hermes"


def test_archived_skill_soft_forgets_its_pointer(home: Path) -> None:
    skills_bridge.on_skill_lifecycle(action="archived", skill_name="old-skill")
    (entry,) = _outbox(home)
    assert entry["op"] == "forget" and entry["payload"] == {"query": "Skill 'old-skill':", "purge": False}


@pytest.mark.parametrize("action", ["loaded", "installed", "stale"])
def test_other_lifecycle_actions_are_ignored(home: Path, action: str) -> None:
    skills_bridge.on_skill_lifecycle(action=action, skill_name="x")
    assert _outbox(home) == []


def test_unconfigured_profile_writes_nothing(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.delenv("AGENT_MEMORY_API_KEY", raising=False)
    skills_bridge.on_skill_lifecycle(action="archived", skill_name="x")
    assert _outbox(tmp_path) == []


def test_pointer_replays_through_the_provider_and_is_then_recalled_as_a_skill_hint(
    home: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr(skills_bridge, "_description", lambda name: "Ships the API.")
    skills_bridge.on_skill_lifecycle(action="created", skill_name="deploy-api")
    c = FakeClient()
    p = make(home, c, monkeypatch)  # initialize() replays the outbox
    p.shutdown()
    kw = next(kw for n, kw in c.calls if n == "remember")
    assert kw["text"].startswith("Skill 'deploy-api':") and kw["scopes"] == "agent/hermes"


def test_register_wires_provider_and_hook() -> None:
    seen: dict[str, Any] = {}

    class Ctx:
        def register_memory_provider(self, provider: Any) -> None:
            seen["provider"] = provider.name

        def register_hook(self, name: str, cb: Any) -> None:
            seen["hook"] = (name, cb)

    register(Ctx())
    assert seen["provider"] == "agent_memory" and seen["hook"] == ("on_skill_lifecycle", skills_bridge.on_skill_lifecycle)
