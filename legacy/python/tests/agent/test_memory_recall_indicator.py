"""MemoryManager.describe_recall — the deterministic recall indicator.

When auto-recall injects memory, Hermes surfaces a model-independent
"🧠 <provider> — recalled N memories" status line so the user SEES memory
working regardless of whether the model chooses to mention it. These tests
lock the formatting (singular/plural/generic) and the aggregation across
providers, all deterministically (no LLM, no network).
"""
from typing import Optional

from agent.memory_manager import MemoryManager
from agent.memory_provider import MemoryProvider, RecallStatus


class _FakeProvider(MemoryProvider):
    """Provider with a settable recall_status for indicator tests."""

    def __init__(self, name: str, status: Optional[RecallStatus], *, raises: bool = False):
        self._name = name
        self._status = status
        self._raises = raises

    @property
    def name(self) -> str:
        return self._name

    def is_available(self) -> bool:
        return True

    def initialize(self, session_id: str = "", **kwargs) -> None:
        pass

    def get_tool_schemas(self):
        return []

    def handle_tool_call(self, tool_name, args, **kwargs) -> str:
        return ""

    def recall_status(self) -> Optional[RecallStatus]:
        if self._raises:
            raise RuntimeError("boom")
        return self._status


def test_no_status_returns_empty_string():
    mgr = MemoryManager()
    mgr.add_provider(_FakeProvider("hindsight", None))
    assert mgr.describe_recall() == ""

