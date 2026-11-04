"""Tests for the skip_background_review constructor flag.

Verifies that AIAgent can be instructed to skip the end-of-turn
_spawn_background_review fork (~30K tokens / event), which is essential
on cron sessions that have no human-in-the-loop value from skill/memory
review forks.
"""
from __future__ import annotations

from unittest.mock import MagicMock

from run_agent import AIAgent
from agent.turn_finalizer import finalize_turn


def _make_agent(skip_background_review: bool = False) -> AIAgent:
    """Construct a minimally-configured AIAgent for unit testing."""
    return AIAgent(
        model="openai/gpt-4o-mini",
        provider="openrouter",
        api_key="sk-dummy",
        base_url="https://openrouter.ai/api/v1",
        quiet_mode=True,
        skip_context_files=True,
        skip_memory=True,
        skip_background_review=skip_background_review,
        platform="cli",
    )


def _stub_agent_for_finalize(agent: AIAgent) -> None:
    """Stub the heavy finalizer dependencies to isolate the review gate."""
    agent._spawn_background_review = MagicMock()
    agent._save_trajectory = MagicMock()
    agent._cleanup_task_resources = MagicMock()
    agent._persist_session = MagicMock()
    agent._session_messages = []
    agent._file_mutation_verifier_enabled = lambda: False
    agent.clear_interrupt = MagicMock()
    agent._stream_callback = None
    agent._sync_external_memory_for_turn = MagicMock()
    agent.valid_tool_names = {"skill_manage"}
    agent.iteration_budget = MagicMock()
    agent.iteration_budget.remaining = 100
    agent.iteration_budget.used = 5
    agent.iteration_budget.max_total = 100
    agent.max_iterations = 50
    agent._emit_status = MagicMock()
    agent._safe_print = MagicMock()
    agent._apply_persist_user_message_override = MagicMock()
    agent.context_compressor = None
    agent._turn_preflight_display_snapshot = None
    agent._turn_received_provider_response = False
    agent.model = "test-model"
    agent.session_id = "test-session"
    agent.quiet_mode = True
    agent._turn_failed_file_mutations = {}
    agent._db_flush_scan_prefix = None


def _run_finalize(agent: AIAgent) -> None:
    """Call finalize_turn with conditions that would trigger background review."""
    finalize_turn(
        agent,
        final_response="ok",
        api_call_count=1,
        interrupted=False,
        failed=False,
        messages=[{"role": "assistant", "content": "ok"}],
        conversation_history=[],
        effective_task_id="test",
        turn_id="test-turn",
        user_message="test",
        original_user_message="test",
        _should_review_memory=True,
        _turn_exit_reason="text_response(1)",
    )






def test_skip_flag_builds_no_learning_loop(tmp_path) -> None:
    """cron/subagent agents (skip_background_review) never get a review loop."""
    from agent.learning.agent_host import build_learning_loop

    for skip, expect_loop in ((True, False), (False, True)):
        agent = _make_agent(skip_background_review=skip)
        agent.valid_tool_names = {"skill_manage"}
        agent._session_db = MagicMock(db_path=tmp_path / "state.db")
        loop = build_learning_loop(agent, {})
        assert (loop is not None) is expect_loop
        if loop is not None:
            loop.close()


def test_finalize_turn_reports_delivered_turn_to_loop() -> None:
    """finalize_turn hands a delivered turn to the learning loop (which owns WHEN to review)."""
    from tests.agent.review_loop_fake import attach_fake_loop

    agent = _make_agent(skip_background_review=False)
    _stub_agent_for_finalize(agent)
    loop = attach_fake_loop(agent)
    _run_finalize(agent)
    assert len(loop.turn_ends) == 1
    agent._spawn_background_review.assert_not_called()  # the loop, not the finalizer, spawns
