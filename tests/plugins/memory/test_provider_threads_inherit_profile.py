"""Multiplex invariant: every memory provider's background thread runs under the spawner's profile.

Profile isolation is a ContextVar-scoped HERMES_HOME override; a plain ``threading.Thread`` starts
with an EMPTY context, so a provider's prefetch/writer thread would silently resolve the DEFAULT
profile's home (and fail closed on scoped secrets). Each case drives the provider's real spawn path
with a fake backend and asserts the thread saw the parent's home.
"""
from __future__ import annotations

import json
import threading

import pytest

from hermes_constants import get_hermes_home, reset_hermes_home_override, set_hermes_home_override
from plugins.memory.agent_memory.provider import AgentMemoryProvider
from tests.plugins.memory.agent_memory.fakes import FakeClient


class _ProbeClient(FakeClient):
    def __init__(self, seen: dict) -> None:
        super().__init__()
        self.seen = seen

    def recall(self, query, *, k, lens=()):
        self.seen["recall"] = get_hermes_home()
        return super().recall(query, k=k, lens=lens)

    def remember_many(self, items, *, session_id=None, scopes=None, labels=()):
        self.seen["write"] = get_hermes_home()
        return super().remember_many(items, session_id=session_id, scopes=scopes, labels=labels)


def _provider(seen, profile_home):
    (profile_home / "agent-memory.json").write_text(
        json.dumps({"endpoint": "https://x.example", "context": "c"}), encoding="utf-8")
    p = AgentMemoryProvider(client_factory=lambda _s: _ProbeClient(seen))
    p.initialize("s1", hermes_home=str(profile_home))
    return p


def _prefetch_thread(p, seen):
    p.queue_prefetch("what do you know")
    return [t for t in threading.enumerate() if t.name == "agent-memory-prefetch"]


def _writer_thread(p, seen):
    p.sync_turn("hi", "hello")
    p.shutdown()  # joins the writer so the write has run
    return []


_CASES = {"prefetch": (_prefetch_thread, "recall"), "writer": (_writer_thread, "write")}


@pytest.mark.parametrize("name", sorted(_CASES))
def test_provider_background_thread_sees_spawner_profile_home(name, tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / "default"))
    monkeypatch.setenv("AGENT_MEMORY_API_KEY", "k")
    profile_home = tmp_path / "profiles" / "b"
    profile_home.mkdir(parents=True)
    seen: dict = {}
    drive, key = _CASES[name]
    token = set_hermes_home_override(profile_home)
    try:
        provider = _provider(seen, profile_home)
        threads = drive(provider, seen)
    finally:
        reset_hermes_home_override(token)
    for t in threads:
        t.join(timeout=10)
    provider.shutdown()
    assert seen.get(key) == profile_home, f"{name}: background thread resolved {seen.get(key)}"
