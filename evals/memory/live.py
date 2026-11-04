from __future__ import annotations

import collections
import importlib
import os
import threading
import time
import uuid
from pathlib import Path
from typing import Any

# Threads the agent spawns for background learning (legacy fork / agent.learning drain).
_REVIEW_THREADS = frozenset({"bg-review", "review-drain"})


class LiveArm:
    """Drives a real AIAgent against a real model with an isolated HERMES_HOME.

    One agent per session; ending a session runs the same lifecycle hook the CLI runs at exit
    (shutdown_memory_provider), then waits for background reviews so their writes are counted.
    Must run in a process launched with HERMES_HOME/HOME already pointing at ``home``.
    """

    name = "live"

    def __init__(self, home: Path, model: str, review_wait_s: float = 300.0) -> None:
        self._home = home
        self._model = model
        self._review_wait_s = review_wait_s
        self._db: Any = None
        self._agent: Any = None
        self._messages: list[dict[str, Any]] = []
        self._told: dict[str, str] = {}
        self.ask_tools: collections.Counter[str] = collections.Counter()
        self.tell_tools: collections.Counter[str] = collections.Counter()

    def _new_agent(self) -> Any:
        if self._db is None:
            self._db = importlib.import_module("hermes_state").SessionDB(self._home / "state.db")
        agent_cls = importlib.import_module("run_agent").AIAgent
        return agent_cls(
            model=self._model,
            provider="anthropic",
            api_key=os.environ["ANTHROPIC_API_KEY"],
            enabled_toolsets=["memory", "skills"],
            quiet_mode=True,
            skip_context_files=True,
            session_db=self._db,
            session_id=f"eval-{uuid.uuid4().hex[:12]}",
            max_iterations=10,
            platform="cli",
            save_trajectories=False,
        )

    def _chat(self, text: str) -> str:
        if self._agent is None:
            self._agent = self._new_agent()
            self._messages = []
        result = self._agent.run_conversation(text, conversation_history=self._messages)
        self._messages = result["messages"]
        return str(result.get("final_response") or "")

    def start_session(self) -> None:
        agent = self._agent
        if agent is None:
            return
        agent.shutdown_memory_provider(self._messages)
        self._wait_for_reviews()
        agent.close()
        self._agent = None
        self._messages = []

    def _wait_for_reviews(self) -> None:
        deadline = time.monotonic() + self._review_wait_s
        while time.monotonic() < deadline:
            if not any(t.name in _REVIEW_THREADS and t.is_alive() for t in threading.enumerate()):
                return
            time.sleep(1.0)

    def _count_tools(self, since: int, into: collections.Counter[str]) -> None:
        for m in self._messages[since:]:
            calls: list[dict[str, Any]] = m.get("tool_calls") or []
            for call in calls:
                into[str(call["function"]["name"])] += 1

    def tell(self, key: str, statement: str) -> None:
        self._told[key] = statement
        before = len(self._messages)
        self._chat(statement)
        self._count_tools(before, self.tell_tools)

    def memory_files(self) -> dict[str, int]:
        return {p.name: p.stat().st_size for p in sorted((self._home / "memories").glob("*.md"))}

    def ask(self, key: str, question: str) -> str | None:
        before = len(self._messages)
        reply = self._chat(question)
        self._count_tools(before, self.ask_tools)
        return reply or None

    def writes_claimed(self) -> int:
        return len(self._told)

    def writes_landed(self) -> int:
        """Told facts whose value is present in the persisted memory files once reviews have drained."""
        self.start_session()
        text = "\n".join(p.read_text(encoding="utf-8") for p in sorted((self._home / "memories").glob("*.md")))
        return sum(1 for s in self._told.values() if s.rstrip(".").split()[-1] in text)
