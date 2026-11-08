"""SurrealDB Agent Memory provider plugin."""

from __future__ import annotations

from typing import Any

from .provider import AgentMemoryProvider
from .skills_bridge import on_skill_lifecycle


def register(ctx: Any) -> None:
    ctx.register_memory_provider(AgentMemoryProvider())
    ctx.register_hook("on_skill_lifecycle", on_skill_lifecycle)
