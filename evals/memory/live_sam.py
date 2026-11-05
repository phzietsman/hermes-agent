"""LiveArm with Agent Memory as the provider (the `loopfix_sam` arm).

The core profile is context-wide, not per-scope, so on a shared Sandbox it carries facts from every earlier
run; the arm therefore serves an empty profile and measures recall and the tools only. Landing is measured at the service boundary: every successful remember/remember_many the real adapter
makes is recorded with its origin label, so "landed" means the service accepted it. Each run uses its own
scopes so runs never read each other's facts. Needs AGENT_MEMORY_API_KEY/ENDPOINT/CONTEXT in the env.
"""

from __future__ import annotations

import importlib
import json
import os
import uuid
from collections import Counter
from collections.abc import Callable, Sequence
from pathlib import Path
from typing import Any

from .live import LiveArm

_RECORDED: list[tuple[str, str]] = []  # (origin, text) of writes the service accepted


def _origin(labels: Sequence[str]) -> str:
    return next((x.split("=", 1)[1] for x in labels if x.startswith("origin=")), "unknown")


def _record_writes() -> None:
    adapter = importlib.import_module("plugins.memory.agent_memory.client").SurrealMemoryAdapter
    if getattr(adapter, "_eval_recording", False):
        return
    remember: Callable[..., Any] = adapter.remember
    remember_many: Callable[..., Any] = adapter.remember_many

    def one(self: Any, text: str, *, labels: Sequence[str] = (), **kw: Any) -> Any:
        out = remember(self, text, labels=labels, **kw)
        _RECORDED.append((_origin(labels), text))
        return out

    def many(self: Any, items: Sequence[Any], *, labels: Sequence[str] = (), **kw: Any) -> Any:
        out = remember_many(self, items, labels=labels, **kw)
        _RECORDED.append((_origin(labels), "\n".join(str(i["content"]) for i in items)))
        return out

    def no_profile(self: Any) -> Any:
        return importlib.import_module("plugins.memory.agent_memory.client").Profile()

    adapter.remember, adapter.remember_many, adapter.profile = one, many, no_profile
    adapter._eval_recording = True


class LiveSamArm(LiveArm):
    name = "live_sam"

    def __init__(self, home: Path, model: str, review_wait_s: float = 300.0) -> None:
        super().__init__(home, model, review_wait_s)
        tag = uuid.uuid4().hex[:10]
        (home / "config.yaml").write_text("memory:\n  provider: agent_memory\n", encoding="utf-8")
        settings = {"endpoint": os.environ["AGENT_MEMORY_ENDPOINT"], "context": os.environ["AGENT_MEMORY_CONTEXT"],
                    "default_scope": f"user/eval-{tag}", "agent_scope": f"agent/eval-{tag}", "timeout_s": 20}
        (home / "agent-memory.json").write_text(json.dumps(settings), encoding="utf-8")
        config = importlib.import_module("plugins.memory.agent_memory.config")
        client_mod = importlib.import_module("plugins.memory.agent_memory.client")
        loaded, why = config.load_settings(home)
        if loaded is None:
            raise RuntimeError(f"Agent Memory settings invalid: {why}")
        client = client_mod.SurrealMemoryAdapter(loaded)
        for scope in (loaded.default_scope, loaded.agent_scope):
            client.register_scope(scope)
        client.close()
        _record_writes()
        _RECORDED.clear()
        self.scope = loaded.default_scope

    def _count_tools(self, since: int, into: Counter[str]) -> None:
        for m in self._messages[since:]:
            calls: list[dict[str, Any]] = m.get("tool_calls") or []
            for call in calls:
                name = str(call["function"]["name"])
                into[name] += 1
                if name == "agent_memory_remember" and into is self.tell_tools:
                    self._foreground_args.append(json.dumps(call["function"].get("arguments", "")))

    def memory_files(self) -> dict[str, int]:
        return {}

    def _landed_values(self) -> list[str]:
        text = "\n".join(t for _, t in _RECORDED)
        return [v for v in self._told_values() if v in text]

    def writes_landed(self) -> int:
        self.start_session()  # drains the writer and any background review
        return len(self._landed_values())

    def landing_detail(self) -> dict[str, int]:
        landed = self._landed_values()
        out: dict[str, int] = {"landed": len(landed)}
        for origin in ("assistant_tool", "turn", "session", "background_review"):
            texts = "\n".join(t for o, t in _RECORDED if o == origin)
            out[origin] = sum(1 for v in landed if v in texts)
        return out


__all__ = ["LiveSamArm"]
