from __future__ import annotations

from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any

from plugins.memory.agent_memory.client import Hit, MemoryAuthFailed, MemoryUnavailable, Profile, Recall
from plugins.memory.agent_memory.config import AgentMemorySettings
from plugins.memory.agent_memory.provider import AgentMemoryProvider


class FakeClient:
    """Records calls; `mode` flips it between healthy and failing."""

    def __init__(self) -> None:
        self.calls: list[tuple[str, dict[str, Any]]] = []
        self.mode = "ok"  # ok | down | auth
        self.hits: tuple[Hit, ...] = ()
        self.profile_value = Profile()
        self.context_text = ""

    def _gate(self, name: str, **kw: Any) -> None:
        if self.mode == "down":
            raise MemoryUnavailable("down")
        if self.mode == "auth":
            raise MemoryAuthFailed("bad key")
        self.calls.append((name, kw))

    def remember(self, text: str, *, labels: Sequence[str] = (), category: str | None = None,
                 scopes: str | None = None, session_id: str | None = None) -> dict[str, Any]:
        self._gate("remember", text=text, labels=list(labels), category=category, scopes=scopes, session_id=session_id)
        return {}

    def remember_many(self, items: Sequence[Mapping[str, str]], *, session_id: str | None = None,
                      scopes: str | None = None, labels: Sequence[str] = ()) -> dict[str, Any]:
        self._gate("remember_many", items=[dict(i) for i in items], session_id=session_id, scopes=scopes,
                   labels=list(labels))
        return {}

    def recall(self, query: str, *, k: int, lens: Sequence[str] = ()) -> Recall:
        self._gate("recall", query=query, k=k, lens=list(lens))
        return Recall(self.hits)

    def query_context(self, query: str, *, k: int, lens: Sequence[str] = ()) -> str:
        self._gate("query_context", query=query, k=k)
        return self.context_text

    def consolidate(self) -> dict[str, Any]:
        self._gate("consolidate")
        return {}

    def reflect(self, query: str, *, persist: bool = False) -> dict[str, Any]:
        self._gate("reflect", query=query, persist=persist)
        return {"reflection": "r"}

    def forget(self, query: str, *, purge: bool = False) -> dict[str, Any]:
        self._gate("forget", query=query, purge=purge)
        return {"deleted": 1}

    def profile(self) -> Profile:
        self._gate("profile")
        return self.profile_value

    def register_scope(self, path: str) -> None:
        self._gate("register_scope", path=path)

    def whoami(self) -> dict[str, Any]:
        self._gate("whoami")
        return {}

    def close(self) -> None:
        return None

    def names(self) -> list[str]:
        return [n for n, _ in self.calls]


def write_config(home: Path, **extra: Any) -> None:
    import json

    (home / "agent-memory.json").write_text(
        json.dumps({"endpoint": "https://x.example", "context": "ctx", **extra}), encoding="utf-8")


def make(home: Path, client: FakeClient, monkeypatch: Any, *, session: str = "s1", **kw: Any) -> AgentMemoryProvider:
    monkeypatch.setenv("AGENT_MEMORY_API_KEY", "sk-test-secret")
    config = {k: kw.pop(k) for k in list(kw) if k in AgentMemorySettings.model_fields}
    write_config(home, **config)
    provider = AgentMemoryProvider(client_factory=lambda _s: client)
    provider.initialize(session, hermes_home=str(home), **kw)
    return provider
