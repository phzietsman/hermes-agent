from __future__ import annotations

import copy
import logging
import sqlite3
from collections.abc import Callable
from pathlib import Path
from typing import Any, cast

from .loop import LearningLoop
from .models import ReviewOutcome, ReviewTrigger
from .queue import SqliteReviewQueue
from .scheduler import ReviewSettings

logger = logging.getLogger(__name__)


class AgentReviewHost:
    """Adapts an AIAgent to ReviewHost. ``agent`` stays Any: AIAgent is not importable here
    without a cycle, and only these four attributes/methods are touched."""

    def __init__(self, agent: Any) -> None:
        self._agent = agent

    def spawn_review(self, messages: list[dict[str, Any]], on_outcome: Callable[[ReviewOutcome], None]) -> None:
        from agent.background_review import load_background_review_settings

        agent = self._agent
        enabled, task_cfg = load_background_review_settings()
        if not enabled or getattr(agent, "_delegate_depth", 0) > 0:
            on_outcome(ReviewOutcome.SKIPPED)
            return
        agent._spawn_background_review_now(
            messages_snapshot=copy.deepcopy(messages),  # the fork sanitizes in place
            
            review_memory=bool(getattr(agent, "_memory_store", None) or getattr(agent, "_memory_manager", None)),
            review_skills="skill_manage" in agent.valid_tool_names,
            task_cfg=task_cfg,
            on_outcome=on_outcome,
        )

    def load_messages(self, session_id: str) -> list[dict[str, Any]]:
        db = getattr(self._agent, "_session_db", None)
        if db is None:
            return []
        loaded: list[dict[str, Any]] = db.get_messages_as_conversation(session_id)
        return loaded


def _section(cfg: dict[str, Any], key: str) -> dict[str, Any]:
    value = cfg.get(key)
    return cast("dict[str, Any]", value) if isinstance(value, dict) else {}


def settings_from_config(agent_cfg: dict[str, Any]) -> ReviewSettings:
    """Map existing config onto ReviewSettings. The old turn-based ``memory.nudge_interval`` and
    iteration-based ``skills.creation_nudge_interval`` are aliases for ``iteration_interval``
    (skills key wins); ``agent.learning.*`` overrides both."""
    skills = _section(agent_cfg, "skills")
    memory = _section(agent_cfg, "memory")
    learning = _section(agent_cfg, "learning")
    base: dict[str, Any] = {
        "iteration_interval": skills.get("creation_nudge_interval", memory.get("nudge_interval", 10)),
    }
    base.update({k: v for k, v in learning.items() if v is not None and k in ReviewSettings.model_fields})
    return ReviewSettings.model_validate(base)


def build_learning_loop(agent: Any, agent_cfg: dict[str, Any]) -> LearningLoop | None:
    """None when reviews cannot run for this agent (cron/subagent, no session store, nothing to review)."""
    if getattr(agent, "skip_background_review", False):
        return None
    db = getattr(agent, "_session_db", None)
    db_path = getattr(db, "db_path", None)
    if db_path is None or (
        "skill_manage" not in agent.valid_tool_names and not getattr(agent, "_memory_manager", None)
        and not getattr(agent, "_memory_store", None)
    ):
        return None
    # Own connection: SessionDB's handle is read-only/lock-guarded; the queue is a separate writer.
    conn = sqlite3.connect(Path(db_path), timeout=10.0, check_same_thread=False)
    return LearningLoop(SqliteReviewQueue(conn), settings_from_config(agent_cfg), AgentReviewHost(agent))


def learning_loop_for(agent: Any) -> LearningLoop | None:
    """The agent's loop, built on first use (tools and session store are only known after init)."""
    if not getattr(agent, "_learning_loop_built", False):
        agent._learning_loop_built = True
        cfg = getattr(agent, "_learning_cfg", None)
        agent._learning_loop = build_learning_loop(agent, cast("dict[str, Any]", cfg) if isinstance(cfg, dict) else {})
    loop: LearningLoop | None = agent._learning_loop
    return loop


def review_on_iteration(agent: Any, messages: list[dict[str, Any]]) -> None:
    loop = learning_loop_for(agent)
    if loop is not None:
        loop.on_tool_iteration(agent.session_id, messages)


def review_after_turn(agent: Any, messages: list[dict[str, Any]]) -> None:
    """Call once the response is delivered; starts any queued review."""
    loop = learning_loop_for(agent)
    if loop is not None:
        loop.on_turn_end(agent.session_id, messages)


def review_on_boundary(agent: Any, trigger: ReviewTrigger, messages: list[dict[str, Any]]) -> None:
    loop = learning_loop_for(agent)
    if loop is not None and messages:
        loop.on_boundary(agent.session_id, trigger, messages)


def review_note_agent_wrote(agent: Any) -> None:
    loop = learning_loop_for(agent)
    if loop is not None:
        loop.note_agent_wrote()
