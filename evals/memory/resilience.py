"""Scenario 7: kill the network mid-session; nothing may be lost.

Runs the real AgentMemoryProvider (writer thread, breaker, outbox, core-profile cache) against an in-process
service whose network can be cut. The invariant is outcome-based (every told fact lands exactly once after
recovery), so it does not depend on writer-thread timing. No credentials needed.
"""

from __future__ import annotations

import json
import os
import tempfile
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any

from plugins.memory.agent_memory.client import MemoryUnavailable, Profile, Recall
from plugins.memory.agent_memory.provider import AgentMemoryProvider

from .models import ScenarioResult
from .scenarios import make_facts

CORE_INSTRUCTION = "Always answer tersely"


class FlakyService:
    """Stores what it is sent while up; raises MemoryUnavailable while down."""

    def __init__(self) -> None:
        self.up = True
        self.landed: list[str] = []

    def _gate(self) -> None:
        if not self.up:
            raise MemoryUnavailable("network cut")

    def remember(self, text: str, *, labels: Sequence[str] = (), category: str | None = None,
                 scopes: str | None = None, session_id: str | None = None) -> dict[str, Any]:
        self._gate()
        self.landed.append(text)
        return {}

    def remember_many(self, items: Sequence[Mapping[str, str]], *, session_id: str | None = None,
                      scopes: str | None = None, labels: Sequence[str] = ()) -> dict[str, Any]:
        self._gate()
        self.landed.extend(i["content"] for i in items)
        return {}

    def recall(self, query: str, *, k: int, lens: Sequence[str] = ()) -> Recall:
        self._gate()
        return Recall(())

    def query_context(self, query: str, *, k: int, lens: Sequence[str] = ()) -> str:
        self._gate()
        return ""

    def consolidate(self) -> dict[str, Any]:
        self._gate()
        return {}

    def reflect(self, query: str, *, persist: bool = False) -> dict[str, Any]:
        self._gate()
        return {}

    def forget(self, query: str, *, purge: bool = False) -> dict[str, Any]:
        self._gate()
        return {}

    def profile(self) -> Profile:
        self._gate()
        return Profile(instructions=(CORE_INSTRUCTION,))

    def register_scope(self, path: str) -> None:
        self._gate()

    def whoami(self) -> dict[str, Any]:
        self._gate()
        return {}

    def close(self) -> None:
        return None


def _session(home: Path, service: FlakyService, session: str) -> AgentMemoryProvider:
    (home / "agent-memory.json").write_text(
        json.dumps({"endpoint": "https://x.example", "context": "ctx"}), encoding="utf-8")
    provider = AgentMemoryProvider(client_factory=lambda _s: service)
    provider.initialize(session, hermes_home=str(home))
    return provider


def _tell(provider: AgentMemoryProvider, statements: Sequence[str]) -> None:
    for s in statements:
        provider.sync_turn(s, "noted")


def resilience(seed: int, n: int = 12) -> ScenarioResult:
    statements = [f.statement for f in make_facts(seed + 7, n)]
    third = n // 3
    before, during, after = statements[:third], statements[third:2 * third], statements[2 * third:]
    prev_key = os.environ.get("AGENT_MEMORY_API_KEY")
    os.environ["AGENT_MEMORY_API_KEY"] = "sk-eval-not-a-secret"
    try:
        with tempfile.TemporaryDirectory() as tmp:
            home, service = Path(tmp), FlakyService()
            # s1: healthy start (caches the core profile), then the network dies mid-session.
            s1 = _session(home, service, "s1")
            _tell(s1, before)
            service.up = False
            _tell(s1, during)
            s1.shutdown()
            # s2: process restarts while still down -> degraded start must still serve the cached core profile.
            s2 = _session(home, service, "s2")
            degraded_profile = int(CORE_INSTRUCTION in s2.system_prompt_block())
            _tell(s2, after)
            s2.shutdown()
            pending = len((home / "agent-memory-outbox.jsonl").read_text().splitlines())
            # s3: network back -> initialize() replays the outbox.
            service.up = True
            _session(home, service, "s3").shutdown()
            outbox = home / "agent-memory-outbox.jsonl"
            left = len(outbox.read_text().splitlines()) if outbox.exists() else 0
            blob = "\n".join(service.landed)
            landed = sum(1 for s in statements if s in blob)
            dupes = sum(1 for s in statements if service.landed.count(s) > 1)
    finally:
        if prev_key is None:
            os.environ.pop("AGENT_MEMORY_API_KEY", None)
        else:
            os.environ["AGENT_MEMORY_API_KEY"] = prev_key
    return ScenarioResult(
        scenario="7_resilience", passed=landed, total=len(statements), seed=seed,
        detail={"degraded_profile_served": degraded_profile, "queued_while_down": pending,
                "outbox_left": left, "duplicates": dupes})

