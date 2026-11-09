"""Pins the slice of the prerelease SDK the adapter relies on. Skipped when the agent-memory extra is absent."""

from __future__ import annotations

import inspect

import pytest

memory = pytest.importorskip("surrealdb.memory")

EXPECTED = {
    "remember": {"text", "labels", "memory_category", "scopes", "session_id"},
    "remember_many": {"items", "session_id", "scopes", "labels"},
    "recall": {"query", "k", "lens"},
    "query_context": {"query", "k", "lens"},
    "forget": {"query", "purge"},
    "reflect": {"query", "persist"},
    "consolidate": set(),
    "profile": set(),
    "whoami": set(),
    "close": set(),
}


@pytest.mark.parametrize("method", sorted(EXPECTED))
def test_method_accepts_the_arguments_the_adapter_sends(method: str) -> None:
    params = set(inspect.signature(getattr(memory.Memory, method)).parameters) - {"self", "on_behalf_of"}
    assert EXPECTED[method] <= params, f"{method}: SDK dropped {EXPECTED[method] - params}"


def test_error_classes_and_response_fields_exist() -> None:
    for name in ("MemoryAuthError", "MemoryScopeError", "MemoryAPIError"):
        assert issubclass(getattr(memory, name), Exception)
    assert {"hits"} <= set(memory.RecallResponse.__annotations__)
    assert {"id", "text", "score"} <= set(memory.RecallHit.__annotations__)
    assert {"context"} <= set(memory.ContextQueryResponse.__annotations__)
    assert {"instructions", "preferences", "static"} <= set(memory.ProfileResponse.__annotations__)
    assert hasattr(memory.BlockingScopes, "register")
