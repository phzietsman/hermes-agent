from __future__ import annotations

from types import SimpleNamespace
from typing import Any
from unittest.mock import MagicMock

import pytest

from agent.learning.agent_host import AgentReviewHost
from agent.learning.models import ReviewOutcome, ReviewTrigger
from tests.agent.review_loop_fake import attach_fake_loop


def make_agent() -> Any:
    return SimpleNamespace(
        session_id="s1",
        valid_tool_names={"skill_manage"},
        _memory_store=None,
        _memory_manager=None,
        _delegate_depth=0,
        _spawn_background_review_now=MagicMock(),
    )


@pytest.fixture
def review_enabled(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr("agent.background_review.load_background_review_settings", lambda: (True, {}))


def test_normal_review_spawns_now_and_reports_through_on_outcome(
    review_enabled: None, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr("run_agent._review_should_defer", lambda agent, cfg: False)
    agent = make_agent()
    seen: list[ReviewOutcome] = []
    AgentReviewHost(agent).spawn_review([{"role": "user", "content": "x"}], seen.append)
    kwargs = agent._spawn_background_review_now.call_args.kwargs
    assert kwargs["on_outcome"] == seen.append and kwargs["review_skills"] is True
    assert seen == []  # the fork reports later


def test_local_runtime_review_is_handed_to_the_idle_queue(
    review_enabled: None, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr("run_agent._review_should_defer", lambda agent, cfg: True)
    queue = MagicMock()
    monkeypatch.setattr("agent.review_idle_queue.QUEUE", queue)
    agent = make_agent()
    seen: list[ReviewOutcome] = []
    AgentReviewHost(agent).spawn_review([{"role": "user", "content": "x"}], seen.append)
    agent._spawn_background_review_now.assert_not_called()
    queue.enqueue.assert_called_once()
    assert "on_outcome" not in queue.enqueue.call_args.args[2]  # the idle queue's own requeue logic applies
    assert seen == [ReviewOutcome.DONE]


def test_disabled_review_is_skipped_not_retried(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr("agent.background_review.load_background_review_settings", lambda: (False, {}))
    agent = make_agent()
    seen: list[ReviewOutcome] = []
    AgentReviewHost(agent).spawn_review([{"role": "user", "content": "x"}], seen.append)
    assert seen == [ReviewOutcome.SKIPPED]
    agent._spawn_background_review_now.assert_not_called()


def test_pre_compress_boundary_reports_to_the_loop() -> None:
    from agent.conversation_compression_memory import _pre_compress_memory_context

    agent = SimpleNamespace(session_id="s1", _memory_manager=None, _persist_disabled=False)
    loop = attach_fake_loop(agent)
    _pre_compress_memory_context(agent, [{"role": "user", "content": "x"}], False)
    assert loop.boundaries == [("s1", ReviewTrigger.PRE_COMPRESS)]
