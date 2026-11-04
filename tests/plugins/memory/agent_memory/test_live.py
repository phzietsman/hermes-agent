"""Against a real Agent Memory Sandbox. Needs AGENT_MEMORY_ENDPOINT, AGENT_MEMORY_CONTEXT and
AGENT_MEMORY_API_KEY; run with `-m live`. Skipped otherwise (and in default CI)."""

from __future__ import annotations

import os
import uuid

import pytest
from pydantic import SecretStr

from plugins.memory.agent_memory.client import SurrealMemoryAdapter
from plugins.memory.agent_memory.config import AgentMemorySettings

pytestmark = pytest.mark.live

_ENV = ("AGENT_MEMORY_ENDPOINT", "AGENT_MEMORY_CONTEXT", "AGENT_MEMORY_API_KEY")


@pytest.fixture
def client() -> SurrealMemoryAdapter:
    missing = [n for n in _ENV if not os.environ.get(n)]
    if missing:
        pytest.skip(f"live Sandbox needs {', '.join(missing)}")
    pytest.importorskip("surrealdb.memory")
    settings = AgentMemorySettings(  # type: ignore[call-arg]  # endpoint coerces str -> HttpUrl
        endpoint=os.environ["AGENT_MEMORY_ENDPOINT"], context=os.environ["AGENT_MEMORY_CONTEXT"],
        api_key=SecretStr(os.environ["AGENT_MEMORY_API_KEY"]))
    return SurrealMemoryAdapter(settings)


def test_whoami_and_profile_round_trip(client: SurrealMemoryAdapter) -> None:
    client.whoami()
    client.profile()


def test_a_remembered_fact_is_recalled(client: SurrealMemoryAdapter) -> None:
    token = f"zephyr-{uuid.uuid4().hex[:6]}"
    client.register_scope("user/default")
    client.remember(f"My staging cluster is called {token}.", scopes="user/default")
    texts = [h.text for h in client.recall("what is my staging cluster called?", k=5, lens=["user/default"]).hits]
    assert any(token in t for t in texts)
