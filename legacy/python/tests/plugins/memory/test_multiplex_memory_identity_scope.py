"""Multiplex invariant: memory-provider identity/tenant/endpoint never comes from the default profile.

Under ``gateway.multiplex_profiles`` ``os.environ`` is the DEFAULT profile's ``.env``. The Agent Memory
provider takes endpoint/context from the active profile's own ``agent-memory.json`` and the key from
the scoped secrets, so a secondary profile never talks to (or writes into) the default profile's account.
"""
from __future__ import annotations

import json

import pytest

from agent import secret_scope
from plugins.memory.agent_memory.client import Profile
from plugins.memory.agent_memory.provider import AgentMemoryProvider
from tests.plugins.memory.agent_memory.fakes import FakeClient

_DEFAULT_ENV = {
    "AGENT_MEMORY_ENDPOINT": "https://default.example", "AGENT_MEMORY_CONTEXT": "ctx-default",
    "AGENT_MEMORY_API_KEY": "key-default",
}


@pytest.fixture
def secondary_profile(monkeypatch, tmp_path):
    """Multiplex ON; default profile's values in environ; secondary profile `b` with its own config
    file and only its own scoped API key."""
    for k, v in _DEFAULT_ENV.items():
        monkeypatch.setenv(k, v)
    prof_b = tmp_path / ".hermes" / "profiles" / "b"
    prof_b.mkdir(parents=True)
    (prof_b / "config.yaml").write_text("{}\n")
    monkeypatch.setenv("HERMES_HOME", str(prof_b))
    secret_scope.set_multiplex_active(True)
    token = secret_scope.set_secret_scope({"AGENT_MEMORY_API_KEY": "key-b"})
    try:
        yield prof_b
    finally:
        secret_scope.reset_secret_scope(token)
        secret_scope.set_multiplex_active(False)


def test_secondary_profile_uses_its_own_config_never_the_default_environ(secondary_profile):
    (secondary_profile / "agent-memory.json").write_text(
        json.dumps({"endpoint": "https://b.example", "context": "ctx-b"}), encoding="utf-8")
    built = []

    def factory(settings):
        built.append(settings)
        c = FakeClient()
        c.profile_value = Profile()
        return c

    provider = AgentMemoryProvider(client_factory=factory)
    provider.initialize("s1", hermes_home=str(secondary_profile))
    provider.shutdown()
    (settings,) = built
    assert str(settings.endpoint).rstrip("/") == "https://b.example"
    assert settings.context == "ctx-b" and settings.api_key.get_secret_value() == "key-b"


def test_secondary_profile_without_its_own_config_is_unavailable_not_the_default_account(secondary_profile):
    provider = AgentMemoryProvider(client_factory=lambda _s: FakeClient())
    provider.initialize("s1", hermes_home=str(secondary_profile))
    assert provider.is_available() is False
