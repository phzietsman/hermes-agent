"""Native media producers keep routed owner decisions across alternating homes."""
import json
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest
from gateway.config import GatewayConfig, PlatformConfig
from gateway.run import GatewayRunner
from gateway.platforms.base import SendResult
from hermes_constants import get_hermes_home


@pytest.fixture(params=[None, False, True, "override", "managed", "null"])
def owners(request, tmp_path, monkeypatch):
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    root = tmp_path / ".hermes"
    root.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(root))
    managed = tmp_path / "managed"
    managed.mkdir()
    monkeypatch.setenv("HERMES_MANAGED_DIR", str(managed))
    (root / "config.yaml").write_text('display: {suppress_warning_notifications: true}')
    mode = request.param
    if mode == "managed":
        (managed / "config.yaml").write_text('display: {suppress_warning_notifications: true}')
    for name in ("a", "b"):
        home = root / "profiles" / name
        home.mkdir(parents=True)
        value = mode if name == "a" else not bool(mode)
        cfg = {} if value is None else {"display": {"suppress_warning_notifications": value is True}}
        if name == "a" and mode in ("override", "null"):
            cfg = {"display": {"suppress_warning_notifications": True, "platforms": {
                p: {"suppress_warning_notifications": False if mode == "override" else None}
                for p in ("slack", "telegram")}}}
        (home / "config.yaml").write_text(json.dumps(cfg))
    def suppressed(name):
        return mode == "managed" or (mode in (True, "null") if name == "a" else not bool(mode))
    return root, suppressed


@pytest.mark.asyncio
@pytest.mark.parametrize("kind", ["document"])
async def test_slack_upload_failure_owner_scope_keeps_caption_and_result(owners, tmp_path, caplog, kind):
    from plugins.platforms.slack.adapter import SlackAdapter
    root, suppressed = owners
    adapter = SlackAdapter(PlatformConfig())
    adapter.gateway_runner = GatewayRunner(GatewayConfig(multiplex_profiles=True))
    attempts, wire = [], []
    async def upload(**kwargs):
        attempts.append((get_hermes_home(), kwargs))
        raise RuntimeError("upload rejected")
    client = SimpleNamespace(files_upload_v2=upload)
    adapter._app = SimpleNamespace(client=client)
    adapter._client_for = lambda chat_id, metadata: client
    adapter._dm_target = AsyncMock(return_value="C123")
    async def send(chat_id, content, **kwargs):
        wire.append((get_hermes_home(), content, kwargs))
        return SendResult(success=True, message_id="fallback")
    adapter.send = send
    path = tmp_path / "media.bin"
    path.write_bytes(b"local bytes")
    for name in ("a", "b", "a"):
        source = adapter.build_source(chat_id="C123", user_id="U123")
        source.profile = name
        before = len(attempts)
        with adapter._media_delivery_scope(source):
            result = await getattr(adapter, "send_" + kind)("C123", str(path), caption="Warning: requested caption", metadata={"thread_id": "123.456"})
        assert len(attempts) > before
        assert all(home == root / "profiles" / name for home, _ in attempts[before:])
        assert wire[-1][0] == root / "profiles" / name
        assert wire[-1][2]["metadata"] == {"thread_id": "123.456"}
        assert ("Couldn't deliver" in wire[-1][1]) is not suppressed(name)
        assert "Warning: requested caption" in wire[-1][1]
        assert result.success is not suppressed(name)
        assert result.message_id == (None if suppressed(name) else "fallback")
        assert get_hermes_home() == root
    assert "upload rejected" in caplog.text

