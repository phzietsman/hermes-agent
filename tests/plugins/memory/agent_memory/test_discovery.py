from __future__ import annotations

from pathlib import Path

import pytest

from agent.memory_provider import MemoryProvider
from plugins.memory import discover_memory_providers, load_memory_provider


def test_bundled_discovery_finds_and_loads_the_real_provider(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.delenv("AGENT_MEMORY_API_KEY", raising=False)
    names = {n for n, _desc, _avail in discover_memory_providers()}
    assert "agent_memory" in names
    provider = load_memory_provider("agent_memory")
    assert isinstance(provider, MemoryProvider) and provider.name == "agent_memory"
    assert provider.is_available() is False  # no key in this profile: gated off, no network attempted
    assert {s["name"] for s in provider.get_tool_schemas()} == {
        "agent_memory_recall", "agent_memory_remember", "agent_memory_forget", "agent_memory_reflect"}
