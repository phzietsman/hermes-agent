"""Inbound-port adapters of a SECONDARY profile are served on the default profile's shared listener
at ``/p/<profile>/<path>`` (gateway/platforms/shared_ingress.py).

Invariants: the forwarded request is verified by the NAMED profile's adapter with that profile's
secret and runs under that profile's runtime scope; the un-prefixed path is untouched; a profile
without an adapter for the path is a 404 rather than the default's adapter; a shared-listener
adapter binds no port of its own.

Uses a minimal signed-webhook adapter so the contract is pinned independently of any one platform.
"""
from __future__ import annotations

import base64
import hashlib
import hmac
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest

pytest.importorskip("aiohttp")
from aiohttp import web  # noqa: E402
from aiohttp.test_utils import TestClient, TestServer  # noqa: E402

from gateway.config import GatewayConfig, Platform, PlatformConfig  # noqa: E402



def _register_fake_plugin_platform(name: str) -> Platform:
    from gateway.platform_registry import PlatformEntry, platform_registry

    platform_registry.register(PlatformEntry(
        name=name, label=name, adapter_factory=lambda cfg: None, check_fn=lambda: True,
    ))
    return Platform(name)


_PLATFORM = _register_fake_plugin_platform("signedhook")
_PATH = "/signedhook/webhook"


def _sig(body: bytes, secret: str) -> str:
    return base64.b64encode(hmac.new(secret.encode(), body, hashlib.sha256).digest()).decode()


class _Runner:
    """Just enough GatewayRunner for shared_ingress: the served-profile adapter map."""

    def __init__(self, adapters: dict[str, dict[Platform, Any]]):
        self.config = GatewayConfig(multiplex_profiles=True)
        self._profile_adapters = adapters
        self.adapters: dict = {}


class _SignedHookAdapter:
    """Verifies ``X-Sig`` with its own secret, records the HERMES_HOME the handler ran under."""

    platform = SimpleNamespace(value="signedhook")

    def __init__(self, secret: str, profile: str):
        self.secret = secret
        self._shared_listener_profile = profile
        self.gateway_runner = None
        self.seen: list[Path] = []
        self.runtime_writes: list[dict] = []

    def _write_runtime_status_safe(self, event, **kw):
        self.runtime_writes.append({"event": event, **kw})

    async def handle(self, request: web.Request) -> web.Response:
        from hermes_constants import get_hermes_home

        body = await request.read()
        if not hmac.compare_digest(request.headers.get("X-Sig", ""), _sig(body, self.secret)):
            return web.Response(status=401)
        self.seen.append(Path(get_hermes_home()))
        return web.Response(status=200)


async def _publish(adapter: _SignedHookAdapter, runner: _Runner) -> None:
    """Wire the adapter's app the way ``connect()`` does, without a bind."""
    from gateway.platforms.shared_ingress import bind_listener

    adapter.gateway_runner = runner
    app = web.Application(client_max_size=1024)
    app.router.add_post(_PATH, adapter.handle)
    assert await bind_listener(adapter, app, "127.0.0.1", 1, _PATH) is None  # shared mode never binds


@pytest.fixture
def mux_home(tmp_path, monkeypatch):
    root = tmp_path / "hermes"
    for name in ("coder", "ops"):
        (root / "profiles" / name).mkdir(parents=True)
    monkeypatch.setenv("HERMES_HOME", str(root))
    import hermes_constants

    monkeypatch.setattr(hermes_constants, "_default_hermes_root_memo", None)
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    from agent import secret_scope

    monkeypatch.setattr(secret_scope, "_MULTIPLEX_ACTIVE", True)
    monkeypatch.setattr(
        "hermes_cli.profiles.profiles_to_serve",
        lambda multiplex: [("default", root), ("coder", root / "profiles" / "coder"), ("ops", root / "profiles" / "ops")],
    )
    return root


async def _shared_listener(runner) -> TestClient:
    """The default profile's listener as the webhook adapter builds it: bare routes + /p/ forwarding."""
    from gateway.platforms.webhook import WebhookAdapter

    listener = WebhookAdapter(PlatformConfig(enabled=True, extra={"port": 1, "routes": {}}))
    listener.gateway_runner = runner
    app = web.Application()
    app.router.add_post("/webhooks/{route_name}", listener._handle_webhook)
    app.router.add_post("/p/{profile}/webhooks/{route_name}", listener._handle_webhook)
    app.router.add_route("*", "/p/{profile}/{tail:.*}", listener._handle_profile_ingress)
    return TestClient(TestServer(app))


@pytest.mark.asyncio
async def test_prefixed_webhook_is_verified_by_the_named_profiles_secret_under_its_scope(mux_home):
    coder, ops = _SignedHookAdapter("secret-coder", "coder"), _SignedHookAdapter("secret-ops", "ops")
    runner = _Runner({"coder": {_PLATFORM: coder}, "ops": {_PLATFORM: ops}})
    await _publish(coder, runner)
    await _publish(ops, runner)
    body = b'{"events":[{"type":"probe"}]}'
    async with await _shared_listener(runner) as client:
        ok = await client.post(f"/p/coder{_PATH}", data=body, headers={"X-Sig": _sig(body, "secret-coder")})
        assert ok.status == 200
        # ops' secret is rejected at coder's URL: the adapter is per profile, so is the secret.
        wrong = await client.post(f"/p/coder{_PATH}", data=body, headers={"X-Sig": _sig(body, "secret-ops")})
        assert wrong.status == 401
        # The un-prefixed path keeps serving only the default profile's own routes.
        bare = await client.post(_PATH, data=body, headers={"X-Sig": _sig(body, "secret-coder")})
        assert bare.status == 404
        # An unserved profile, and a served profile without that platform, are 404, never another adapter.
        assert (await client.post(f"/p/nope{_PATH}", data=body)).status == 404
        runner._profile_adapters["ops"] = {}
        assert (await client.post(f"/p/ops{_PATH}", data=body, headers={"X-Sig": _sig(body, "secret-ops")})).status == 404
    assert coder.seen == [mux_home / "profiles" / "coder"]
    assert ops.seen == []


@pytest.mark.asyncio
async def test_shared_listener_adapter_records_its_public_ingress_url(mux_home):
    """Runtime status carries the /p/<profile>/ URL so `gateway status` / the dashboard can show it."""
    coder = _SignedHookAdapter("secret-coder", "coder")
    runner = _Runner({"coder": {_PLATFORM: coder}})
    runner.adapters = {Platform.API_SERVER: type("L", (), {"_host": "0.0.0.0", "_port": 8642})()}
    await _publish(coder, runner)
    assert coder._shared_ingress_url == f"http://127.0.0.1:8642/p/coder{_PATH}"
    assert coder.runtime_writes[-1] == {"event": "shared_ingress", "ingress_url": coder._shared_ingress_url}


@pytest.mark.asyncio
async def test_default_profile_adapter_binds_its_own_port_not_the_shared_listener(mux_home):
    """Without a shared-listener profile, bind_listener really binds (the default's port-binder)."""
    from gateway.platforms.shared_ingress import bind_listener

    adapter = _SignedHookAdapter("s", "")
    app = web.Application()
    app.router.add_post(_PATH, adapter.handle)
    runner = await bind_listener(adapter, app, "127.0.0.1", 0, _PATH)
    try:
        assert runner is not None
        assert not hasattr(adapter, "_shared_ingress_app")
    finally:
        await runner.cleanup()
