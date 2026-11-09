"""Tests for the on_session_switch hook and session_id propagation.

Covers #6672: memory providers must be notified when AIAgent.session_id
rotates mid-process (via /resume, /branch, /reset, /new, or context
compression). Without the notification, providers that cache per-session
state in initialize() (Hindsight, and any plugin that stores session_id
for scoped writes) keep writing into the old session's record.
"""



from agent.memory_manager import MemoryManager
from agent.memory_provider import MemoryProvider


class _RecordingProvider(MemoryProvider):
    """Provider that records every lifecycle call for assertion."""

    def __init__(self, name="rec"):
        self._name = name
        self.switch_calls: list[dict] = []
        self.sync_calls: list[dict] = []
        self.queue_calls: list[dict] = []
        self.initialize_calls: list[dict] = []

    @property
    def name(self) -> str:
        return self._name

    def is_available(self) -> bool:  # pragma: no cover - unused
        return True

    def initialize(self, session_id, **kwargs):
        self.initialize_calls.append({"session_id": session_id, **kwargs})

    def get_tool_schemas(self):
        return []

    def sync_turn(self, user_content, assistant_content, *, session_id=""):
        self.sync_calls.append(
            {"user": user_content, "asst": assistant_content, "session_id": session_id}
        )

    def queue_prefetch(self, query, *, session_id=""):
        self.queue_calls.append({"query": query, "session_id": session_id})

    def on_session_switch(
        self,
        new_session_id,
        *,
        parent_session_id="",
        reset=False,
        **kwargs,
    ):
        self.switch_calls.append(
            {
                "new": new_session_id,
                "parent": parent_session_id,
                "reset": reset,
                "extra": kwargs,
            }
        )


# ---------------------------------------------------------------------------
# MemoryManager.on_session_switch — fan-out
# ---------------------------------------------------------------------------


# ---------------------------------------------------------------------------
# MemoryManager.sync_all / queue_prefetch_all — session_id propagation
# ---------------------------------------------------------------------------
