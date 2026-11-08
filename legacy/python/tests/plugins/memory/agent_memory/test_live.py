"""Against a real Agent Memory Sandbox; run with `-m live`. Skipped otherwise (and in default CI).

The suite's conftest unsets every credential-shaped env var before each test, so the Sandbox
credentials are read from a dotenv-style file named by AGENT_MEMORY_LIVE_ENV (not credential-shaped,
so it survives) holding AGENT_MEMORY_ENDPOINT, AGENT_MEMORY_CONTEXT and AGENT_MEMORY_API_KEY."""

from __future__ import annotations

import os
import uuid
from pathlib import Path

import pytest
from pydantic import SecretStr

from plugins.memory.agent_memory.client import MemoryRejected, SurrealMemoryAdapter
from plugins.memory.agent_memory.config import AgentMemorySettings

pytestmark = pytest.mark.live

_ENV = ("AGENT_MEMORY_ENDPOINT", "AGENT_MEMORY_CONTEXT", "AGENT_MEMORY_API_KEY")


def _credentials() -> dict[str, str]:
    path = os.environ.get("AGENT_MEMORY_LIVE_ENV")
    if not path:
        return {}
    pairs = (line.split("=", 1) for line in Path(path).read_text(encoding="utf-8").splitlines() if "=" in line)
    return {k.strip(): v.strip() for k, v in pairs}


@pytest.fixture
def client() -> SurrealMemoryAdapter:
    creds = _credentials()
    missing = [n for n in _ENV if not creds.get(n)]
    if missing:
        pytest.skip(f"live Sandbox needs AGENT_MEMORY_LIVE_ENV with {', '.join(missing)}")
    pytest.importorskip("surrealdb.memory")
    settings = AgentMemorySettings(  # type: ignore[call-arg]  # endpoint coerces str -> HttpUrl
        endpoint=creds["AGENT_MEMORY_ENDPOINT"], context=creds["AGENT_MEMORY_CONTEXT"],
        api_key=SecretStr(creds["AGENT_MEMORY_API_KEY"]))
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


@pytest.mark.parametrize("category", ["identity", "knowledge", "context", None])
def test_every_category_and_label_shape_the_provider_sends_is_accepted(client: SurrealMemoryAdapter, category: str | None) -> None:
    token = f"quartz-{uuid.uuid4().hex[:6]}"
    client.remember(f"Live probe {token}.", labels=["kind=fact", "origin=live_test", "session=live-test"],
                    category=category, scopes="user/default")


@pytest.mark.parametrize(("kwargs", "why"), [
    ({"category": "instructions"}, "category must be identity|knowledge|context"),
    ({"labels": ["bare-label"]}, "labels must be key=value"),
    ({"session_id": "not-issued-to-this-caller"}, "session ids are issued by the service"),
])
def test_requests_the_service_refuses_are_classified_as_rejected_not_unavailable(
    client: SurrealMemoryAdapter, kwargs: dict[str, object], why: str
) -> None:
    with pytest.raises(MemoryRejected):
        client.remember("Live probe that must be refused.", scopes="user/default", **kwargs)  # type: ignore[arg-type]
