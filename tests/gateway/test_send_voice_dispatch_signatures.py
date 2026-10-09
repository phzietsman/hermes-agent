"""Every platform adapter's ``send_voice`` must accept the ``is_voice`` keyword the base media
dispatch passes (``BasePlatformAdapter._deliver_media_from_response`` →
``send_voice(..., is_voice=is_voice)``). An explicit signature without it raises ``TypeError`` and
the attachment is silently dropped (#102221, #116776)."""

from __future__ import annotations

import importlib
import inspect
from pathlib import Path

import pytest

from gateway.config import PlatformConfig
from gateway.platforms.base import BasePlatformAdapter, SendResult
from gateway.platforms.event import MessageEvent
from gateway.session import Platform, SessionSource

_PLUGINS_ROOT = Path(__file__).resolve().parents[2] / "plugins" / "platforms"


def _adapter_classes():
    classes = [BasePlatformAdapter]
    from gateway.platforms.whatsapp_cloud import WhatsAppCloudAdapter

    classes.append(WhatsAppCloudAdapter)
    for plugin_dir in sorted(p for p in _PLUGINS_ROOT.iterdir() if (p / "adapter.py").exists()):
        module = importlib.import_module(f"plugins.platforms.{plugin_dir.name}.adapter")
        classes += [
            obj for obj in vars(module).values()
            if inspect.isclass(obj) and issubclass(obj, BasePlatformAdapter)
            and obj is not BasePlatformAdapter and obj.__module__ == module.__name__
        ]
    return classes


@pytest.mark.parametrize("adapter_cls", _adapter_classes(), ids=lambda c: c.__name__)
def test_send_voice_accepts_is_voice(adapter_cls) -> None:
    params = inspect.signature(adapter_cls.send_voice).parameters
    assert "is_voice" in params or any(p.kind is inspect.Parameter.VAR_KEYWORD for p in params.values()), (
        f"{adapter_cls.__name__}.send_voice rejects the is_voice keyword the base media dispatch passes"
    )


class _VoiceProbeAdapter(BasePlatformAdapter):
    """Minimal adapter whose ``send_voice`` narrows to ``**kwargs`` the way platform adapters do."""

    def __init__(self):
        super().__init__(PlatformConfig(enabled=True), Platform.TELEGRAM)
        self.voice_calls: list[dict] = []

    async def connect(self):
        return True

    async def disconnect(self):
        return None

    async def send(self, *a, **k):
        return SendResult(success=True)

    async def get_chat_info(self, chat_id):
        return {"name": chat_id}

    async def send_voice(self, chat_id, audio_path, caption=None, reply_to=None, metadata=None, **kwargs):
        self.voice_calls.append({"path": audio_path, **kwargs})
        return SendResult(success=True, message_id="v1")


@pytest.mark.asyncio
async def test_media_dispatch_delivers_audio_through_send_voice(tmp_path) -> None:
    """Behavioural pin: the real base dispatch calls ``send_voice(..., is_voice=...)`` for an
    audio-extension MEDIA attachment and the file reaches the adapter's voice sender."""
    adapter = _VoiceProbeAdapter()
    clip = tmp_path / "clip.mp3"
    clip.write_bytes(b"ID3")
    event = MessageEvent(text="", source=SessionSource(platform=Platform.TELEGRAM, chat_id="chan-1"))
    results = []

    await adapter._deliver_media_attachments(
        event, [(str(clip), False)], [], force_document_attachments=False, human_delay=0,
        metadata={}, record_delivery=results.append)

    assert [r.success for r in results] == [True]
    assert adapter.voice_calls and adapter.voice_calls[0]["path"] == str(clip)
    assert adapter.voice_calls[0]["is_voice"] is False
