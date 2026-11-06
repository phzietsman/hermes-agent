from __future__ import annotations

import sqlite3
import threading
from collections.abc import Callable
from typing import Any

from agent.learning.agent_host import settings_from_config
from agent.learning.loop import LearningLoop
from agent.learning.models import ReviewOutcome, ReviewTrigger
from agent.learning.queue import SqliteReviewQueue
from agent.learning.scheduler import ReviewSettings


class FakeHost:
    def __init__(self, outcomes: list[ReviewOutcome]) -> None:
        self.outcomes = outcomes
        self.spawned: list[list[dict[str, Any]]] = []
        self.spawn_event = threading.Event()
        self.persisted: dict[str, list[dict[str, Any]]] = {}

    def spawn_review(self, messages: list[dict[str, Any]], on_outcome: Callable[[ReviewOutcome], None]) -> None:
        self.spawned.append(messages)
        self.spawn_event.set()
        on_outcome(self.outcomes.pop(0) if self.outcomes else ReviewOutcome.DONE)

    def load_messages(self, session_id: str) -> list[dict[str, Any]]:
        return self.persisted.get(session_id, [])


def make_loop(host: FakeHost, **kw: Any) -> LearningLoop:
    q = SqliteReviewQueue(sqlite3.connect(":memory:", check_same_thread=False))
    return LearningLoop(q, ReviewSettings(idle_seconds=0, backoff_base_s=0.01, **kw), host)


def msgs(n: int) -> list[dict[str, Any]]:
    return [{"role": "user", "content": f"m{i}"} for i in range(n)]


def wait_idle(loop: LearningLoop) -> None:
    worker = loop._worker  # noqa: SLF001
    if worker is not None:
        worker.join(5)


def test_iteration_enqueues_but_review_only_starts_after_turn_end() -> None:
    host = FakeHost([])
    loop = make_loop(host, iteration_interval=3)
    m = msgs(6)
    assert [loop.on_tool_iteration("s1", m) for _ in range(3)] == [False, False, True]
    assert host.spawned == []  # mid-turn: queued, nothing running
    loop.on_turn_end("s1", m)
    wait_idle(loop)
    assert len(host.spawned) == 1 and len(host.spawned[0]) == 6


def test_boundary_runs_review_immediately() -> None:
    host = FakeHost([])
    loop = make_loop(host)
    assert loop.on_boundary("s1", ReviewTrigger.SESSION_RESET, msgs(4))
    wait_idle(loop)
    assert len(host.spawned) == 1


def test_interrupted_review_is_rerun_and_failure_is_retried() -> None:
    host = FakeHost([ReviewOutcome.INTERRUPTED, ReviewOutcome.FAILED, ReviewOutcome.DONE])
    loop = make_loop(host)
    loop.on_boundary("s1", ReviewTrigger.SESSION_END, msgs(4))
    wait_idle(loop)
    import time

    deadline = time.time() + 5
    while len(host.spawned) < 3 and time.time() < deadline:
        loop.kick()
        wait_idle(loop)
        time.sleep(0.02)
    assert len(host.spawned) == 3


def test_job_from_a_previous_process_loads_persisted_transcript() -> None:
    host = FakeHost([])
    host.persisted["old"] = msgs(5)
    conn = sqlite3.connect(":memory:", check_same_thread=False)
    first = LearningLoop(SqliteReviewQueue(conn), ReviewSettings(idle_seconds=0), FakeHost([]))
    first._scheduler.on_boundary("old", ReviewTrigger.SHUTDOWN, ["a", "b"])  # noqa: SLF001
    second = LearningLoop(SqliteReviewQueue(conn), ReviewSettings(idle_seconds=0), host)
    second.kick()
    wait_idle(second)
    assert host.spawned == [host.persisted["old"]]


def test_config_aliases_map_to_iteration_interval() -> None:
    assert settings_from_config({}).iteration_interval == 10
    assert settings_from_config({"memory": {"nudge_interval": 4}}).iteration_interval == 4
    assert settings_from_config(
        {"memory": {"nudge_interval": 4}, "skills": {"creation_nudge_interval": 7}}
    ).iteration_interval == 7
    cfg = {"skills": {"creation_nudge_interval": 7}, "learning": {"iteration_interval": 2, "bogus": 1}}
    assert settings_from_config(cfg).iteration_interval == 2


def test_shipped_defaults_are_valid_and_defer_to_the_legacy_aliases() -> None:
    from hermes_cli.config_defaults import DEFAULT_CONFIG

    defaults = DEFAULT_CONFIG["learning"]
    ReviewSettings.model_validate({k: v for k, v in defaults.items() if v is not None})
    assert defaults["iteration_interval"] is None  # a non-None default would mask the aliases
    # a user who disabled reviews via the old key still gets them disabled
    assert settings_from_config({**DEFAULT_CONFIG, "memory": {"nudge_interval": 0}}).iteration_interval == 0
