"""Native diagnostic producers keep captions, controls and truthful receipts.

No live platform connections: actual adapters run against recording transports.
"""
import json
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from gateway.config import PlatformConfig
from plugins.platforms.discord.adapter import DiscordAdapter


@pytest.fixture(params=[None, False, True, "override"])
def policy(request, tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    setting = request.param
    if setting is not None:
        display = {"suppress_warning_notifications": setting is True}
        if setting == "override":
            display["platforms"] = {
                name: {"suppress_warning_notifications": True}
                for name in ("discord", "telegram")
            }
        (tmp_path / "config.yaml").write_text(json.dumps({"display": display}))
    return setting is True or setting == "override"


@pytest.mark.asyncio
@pytest.mark.parametrize("method", ["send_document", "send_voice", "send_multiple_images"])
@pytest.mark.parametrize("forum", [False, True])
async def test_discord_rejected_media_preserves_caption_without_fake_receipt(policy, method, forum, tmp_path, caplog):
    adapter = DiscordAdapter(PlatformConfig())
    frames = []

    async def record(**payload):
        frames.append(payload)
        return SimpleNamespace(id=123, attachments=[])

    async def create_thread(**payload):
        frames.append(payload)
        return SimpleNamespace(thread=SimpleNamespace(id=101), message=SimpleNamespace(id=102))

    channel = SimpleNamespace(send=record, id=99, create_thread=create_thread)
    adapter._client = SimpleNamespace()
    adapter._resolve_channel = AsyncMock(return_value=channel)
    adapter._is_forum_parent = lambda channel: forum
    adapter._discord_upload_limit_bytes = lambda channel: 1
    path = tmp_path / "large.png"
    path.write_bytes(b"too large")
    caption = "Warning: requested caption"
    if method == "send_multiple_images":
        result = await adapter.send_multiple_images("99", [(path.as_uri(), caption)])
    else:
        result = await getattr(adapter, method)("99", str(path), caption=caption)
    assert not result.success and result.message_id is None
    if policy:
        assert [f["content"] for f in frames] == [caption]
    elif forum:
        assert frames == []  # legacy forum rejection sends no notice
    else:
        assert len(frames) == 1 and "⚠️" in frames[0]["content"]
        assert caption not in frames[0]["content"]  # preserve legacy default
    assert "large" in caplog.text


@pytest.mark.asyncio
@pytest.mark.parametrize("lane", ["send", "edit"])
async def test_discord_truncation_warning_covers_send_and_final_edit(policy, lane):
    adapter = DiscordAdapter(PlatformConfig())
    frames = []

    async def record(**payload):
        frames.append(payload["content"])
        return SimpleNamespace(id=123, to_reference=lambda **kwargs: None)

    message = SimpleNamespace(id=123, edit=record, to_reference=lambda **kwargs: None)
    channel = SimpleNamespace(send=record, get_partial_message=lambda ident: message, id=99)
    adapter._client = SimpleNamespace()
    adapter._resolve_channel = AsyncMock(return_value=channel)
    adapter._is_forum_parent = lambda channel: False
    adapter._record_response_async = AsyncMock()
    adapter.MAX_MESSAGE_LENGTH = 80
    adapter.MAX_SPLIT_MESSAGES = 3
    text = "Warning: requested result. " * 40
    if lane == "send":
        result = await adapter.send("99", text)
    else:
        result = await adapter.edit_message("99", "123", text, finalize=True)
    assert result.success
    assert frames[0].startswith("Warning: requested result.")
    assert any("Response truncated" in frame for frame in frames) is (not policy)
    assert len(frames) == (2 if policy else 3)


@pytest.mark.asyncio
async def test_telegram_cache_warning_does_not_hide_inbound_context(policy):
    from plugins.platforms.telegram.adapter import TelegramAdapter

    adapter = TelegramAdapter(PlatformConfig())
    frames = []

    async def reply_text(text):
        frames.append(text)

    from gateway.platforms.event import MessageEvent
    event = MessageEvent(text="Requested content", source=adapter.build_source(chat_id="42", user_id="42"))
    await adapter._surface_media_cache_failure(SimpleNamespace(reply_text=reply_text), event, "photo", RuntimeError("fixture"))
    assert bool(frames) is (not policy)
    assert event.text.startswith("Requested content")
    assert "could not be downloaded" in event.text
    assert ("asked to retry" in event.text) is (not policy)
