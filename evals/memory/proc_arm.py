"""Arms for scenario 5: the live agent with terminal + file tools, in a scratch working directory."""

from __future__ import annotations

import importlib
import os
import uuid
from collections import Counter
from pathlib import Path
from typing import Any

from .live import LiveArm
from .live_sam import LiveSamArm

_ERROR_MARKERS = ('"error"', '"exit_code": 1', "Traceback", "No such file")


class _Procedural:
    """Mixin over a LiveArm: more toolsets, more iterations, and per-task cost measurement."""

    _home: Path
    _model: str
    _db: Any
    _agent: Any
    _messages: list[dict[str, Any]]

    def _new_agent(self) -> Any:
        if self._db is None:
            self._db = importlib.import_module("hermes_state").SessionDB(self._home / "state.db")
        return importlib.import_module("run_agent").AIAgent(
            model=self._model, provider="anthropic", api_key=os.environ["ANTHROPIC_API_KEY"],
            enabled_toolsets=["terminal", "file", "memory", "skills"], quiet_mode=True, skip_context_files=True,
            session_db=self._db, session_id=f"eval-{uuid.uuid4().hex[:12]}", max_iterations=30, platform="cli",
            save_trajectories=False)

    def run_task(self, text: str) -> dict[str, Any]:
        before = len(self._messages)
        self._chat(text)  # type: ignore[attr-defined]
        tools: Counter[str] = Counter()
        errors = 0
        for m in self._messages[before:]:
            for call in m.get("tool_calls") or []:
                tools[str(call["function"]["name"])] += 1
            if m.get("role") == "tool" and any(k in str(m.get("content", ""))[:400] for k in _ERROR_MARKERS):
                errors += 1
        agent = self._agent
        return {"calls": sum(tools.values()), "errors": errors, "tokens": int(agent.session_total_tokens),
                "skill_views": tools["skill_view"], "skill_writes": tools["skill_manage"],
                "memory_recalls": tools["agent_memory_recall"], "memory_saves": tools["agent_memory_remember"]
                + tools["memory"]}

    def skills_created(self) -> int:
        return len(list((self._home / "skills").rglob("SKILL.md")))


class ProcArm(_Procedural, LiveArm):
    name = "proc"


class ProcSamArm(_Procedural, LiveSamArm):
    name = "proc_sam"
