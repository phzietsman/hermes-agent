"""Recording stand-in for agent.learning.loop.LearningLoop on agent stubs.

Real scheduling/queue/runner behaviour is covered in tests/agent/learning; tests that only
care that a code path *reports* to the loop attach this instead.
"""

from __future__ import annotations

from typing import Any


class FakeLoop:
    def __init__(self) -> None:
        self.iterations = 0
        self.turn_ends: list[list[dict[str, Any]]] = []
        self.boundaries: list[tuple[str, Any]] = []
        self.agent_writes = 0

    def on_tool_iteration(self, session_id: str, messages: Any) -> bool:
        self.iterations += 1
        return False

    def on_turn_end(self, session_id: str, messages: list[dict[str, Any]]) -> None:
        self.turn_ends.append(list(messages))

    def on_boundary(self, session_id: str, trigger: Any, messages: Any) -> bool:
        self.boundaries.append((session_id, trigger))
        return True

    def note_agent_wrote(self) -> None:
        self.agent_writes += 1


def attach_fake_loop(agent: Any) -> FakeLoop:
    loop = FakeLoop()
    agent._learning_loop_built = True
    agent._learning_loop = loop
    return loop
