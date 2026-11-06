from __future__ import annotations

import sys
import types
from typing import Any

import pytest
from pydantic import SecretStr

from plugins.memory.agent_memory.client import (
    MemoryAuthFailed, MemoryRejected, MemoryTimeout, MemoryUnavailable, SurrealMemoryAdapter,
)
from plugins.memory.agent_memory.config import AgentMemorySettings


class _AuthError(Exception): ...
class _ScopeError(Exception): ...
class _NotFoundError(Exception): ...


class _ApiError(Exception):
    def __init__(self, message: str, status_code: int = 0) -> None:
        super().__init__(f"[{status_code}] {message}")
        self.status_code = status_code


class _Resp:
    def __init__(self, **kw: Any) -> None:
        self.__dict__.update(kw)

    def model_dump(self) -> dict[str, Any]:
        return dict(self.__dict__)


class _Scopes:
    def __init__(self) -> None:
        self.registered: list[str] = []

    def register(self, path: str) -> None:
        self.registered.append(path)


class FakeSdkMemory:
    instances: list[FakeSdkMemory] = []

    def __init__(self, context: str, *, endpoint: str, api_key: str, timeout: float, max_retries: int) -> None:
        self.init = dict(context=context, endpoint=endpoint, api_key=api_key, timeout=timeout, max_retries=max_retries)
        self.calls: list[tuple[str, tuple[Any, ...], dict[str, Any]]] = []
        self.raise_with: Exception | None = None
        self.scopes = _Scopes()
        FakeSdkMemory.instances.append(self)

    def _do(self, name: str, *a: Any, **kw: Any) -> Any:
        if self.raise_with:
            raise self.raise_with
        self.calls.append((name, a, kw))
        return _Resp(deleted=1)

    def remember(self, *a: Any, **kw: Any) -> Any:
        return self._do("remember", *a, **kw)

    def remember_many(self, *a: Any, **kw: Any) -> Any:
        return self._do("remember_many", *a, **kw)

    def forget(self, *a: Any, **kw: Any) -> Any:
        return self._do("forget", *a, **kw)

    def recall(self, query: str, **kw: Any) -> Any:
        self._do("recall", query, **kw)
        return _Resp(hits=[_Resp(id="h1", text="a fact", score=0.9, source="x")])

    def profile(self) -> Any:
        return _Resp(instructions=[{"text": "be terse"}, {"id": "g1", "label": "concise", "description": "answer in one sentence"}], preferences=[_Resp(key="editor", value="vim")], static=[])

    def close(self) -> None:
        return None


@pytest.fixture
def adapter(monkeypatch: pytest.MonkeyPatch) -> tuple[SurrealMemoryAdapter, FakeSdkMemory]:
    sdk = types.ModuleType("surrealdb.memory")
    sdk.Memory = FakeSdkMemory  # type: ignore[attr-defined]  # fake module for the adapter's lazy import
    sdk.MemoryAuthError, sdk.MemoryScopeError, sdk.MemoryAPIError = _AuthError, _ScopeError, _ApiError  # type: ignore[attr-defined]
    sdk.MemoryNotFoundError = _NotFoundError  # type: ignore[attr-defined]
    monkeypatch.setitem(sys.modules, "surrealdb.memory", sdk)
    settings = AgentMemorySettings(endpoint="https://x.example/", context="ctx", api_key=SecretStr("sekret"))  # type: ignore[arg-type]
    a = SurrealMemoryAdapter(settings)
    return a, FakeSdkMemory.instances[-1]


def test_client_is_built_with_explicit_settings(adapter: tuple[SurrealMemoryAdapter, FakeSdkMemory]) -> None:
    _, sdk = adapter
    assert sdk.init == {"context": "ctx", "endpoint": "https://x.example", "api_key": "sekret", "timeout": 20.0,
                        "max_retries": 2}


def test_remember_forwards_labels_category_and_scope(adapter: tuple[SurrealMemoryAdapter, FakeSdkMemory]) -> None:
    a, sdk = adapter
    a.remember("t", labels=["kind=fact"], category="instructions", scopes="user/a", session_id="s")
    a.remember("u")
    (_, args, kw), (_, _, kw2) = sdk.calls
    assert args == ("t",) and kw == {"labels": ["kind=fact"], "memory_category": "instructions", "scopes": "user/a",
                                     "session_id": "s"}
    assert kw2["labels"] is None  # empty labels are not sent as []


def test_recall_and_profile_are_mapped_to_plain_types(adapter: tuple[SurrealMemoryAdapter, FakeSdkMemory]) -> None:
    a, _ = adapter
    hit = a.recall("q", k=3, lens=["user/a"]).hits[0]
    assert (hit.id, hit.text, hit.score) == ("h1", "a fact", 0.9)
    p = a.profile()
    assert p.instructions == ("be terse", "answer in one sentence") and p.preferences == ("editor: vim",)


def test_register_scope_and_forget(adapter: tuple[SurrealMemoryAdapter, FakeSdkMemory]) -> None:
    a, sdk = adapter
    a.register_scope("user/a")
    assert sdk.scopes.registered == ["user/a"]
    assert a.forget("q", purge=True) == {"deleted": 1}


@pytest.mark.parametrize(("error", "expected"), [
    (_AuthError("no"), MemoryAuthFailed), (_ApiError("bad key", 401), MemoryAuthFailed),
    # one request refused (outside scope, malformed): the service is fine
    (_ScopeError("outside the caller's write region"), MemoryRejected), (_NotFoundError("gone"), MemoryRejected),
    (_ApiError("unknown variant", 422), MemoryRejected), (_ApiError("bad request", 400), MemoryRejected),
    # service trouble: retryable
    (_ApiError("boom", 500), MemoryUnavailable), (_ApiError("slow down", 429), MemoryUnavailable),
    (_ApiError("request timeout", 408), MemoryUnavailable), (ConnectionResetError("reset"), MemoryUnavailable),
    (_ApiError("Connection failed: Read timed out. (read timeout=6.0)", 0), MemoryTimeout),
])
def test_sdk_errors_become_provider_errors(adapter: tuple[SurrealMemoryAdapter, FakeSdkMemory], error: Exception,
                                           expected: type[Exception]) -> None:
    a, sdk = adapter
    sdk.raise_with = error
    with pytest.raises(expected):
        a.remember("t")
