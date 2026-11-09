"""Implicit ``hermes-<platform>`` toolset bundle for a registered plugin platform."""

import pytest

from gateway.platform_registry import PlatformEntry, platform_registry
from toolsets import _HERMES_CORE_TOOLS, resolve_toolset
from tools.registry import registry


@pytest.fixture
def fake_platform():
    platform_registry.register(PlatformEntry(
        name="bundleplat", label="Bundle Platform",
        adapter_factory=lambda cfg: None, check_fn=lambda: True,
    ))
    try:
        yield "bundleplat"
    finally:
        platform_registry.unregister("bundleplat")


def test_registered_plugin_platform_resolves_to_core_tools(fake_platform):
    tools = set(resolve_toolset(f"hermes-{fake_platform}"))
    assert set(_HERMES_CORE_TOOLS) <= tools


def test_plugin_registered_tools_join_the_platform_bundle(fake_platform):
    registry.register(
        name="bundleplat_ping", toolset=fake_platform,
        schema={"name": "bundleplat_ping", "description": "ping", "parameters": {"type": "object", "properties": {}}},
        handler=lambda args, **kw: "{}",
    )
    try:
        assert "bundleplat_ping" in resolve_toolset(f"hermes-{fake_platform}")
        # The registry-added tool is a registry overlay, not part of the static view.
        assert "bundleplat_ping" not in resolve_toolset(f"hermes-{fake_platform}", include_registry=False)
    finally:
        registry.deregister("bundleplat_ping")


def test_unregistered_platform_has_no_implicit_bundle():
    assert resolve_toolset("hermes-nosuchplatform") == []
