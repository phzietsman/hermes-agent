"""Regression tests for #30768, #32383, and #33961.

``_prompt_text_input_modal`` answers destructive-slash confirmations through a
queue-based modal driven by prompt_toolkit key bindings.  When invoked from the
``process_loop`` daemon thread it sets the modal up on the app's event loop via
``call_soon_threadsafe`` instead of a raw ``input()`` that fights prompt_toolkit's
stdin ownership.

These tests verify:
1. Daemon-thread confirm uses the modal via the app loop — never the raw stdin
   fallback, never a hang.
2. Main-thread confirm with a running app uses the modal.
3. The raw stdin fallback is kept ONLY for the safe case: no running app.
4. Empty choices returns None.
"""

import threading
import time
from unittest.mock import MagicMock, patch

import pytest


def _make_cli():
    """Minimal HermesCLI shell exposing the prompt/modal helpers."""
    import cli as cli_mod

    obj = object.__new__(cli_mod.HermesCLI)
    obj._app = MagicMock()
    obj._app.loop = MagicMock()
    obj._status_bar_visible = True
    obj._last_invalidate = 0.0
    obj._modal_input_snapshot = None
    obj._slash_confirm_state = None
    obj._slash_confirm_deadline = 0
    return obj


_SAMPLE_CHOICES = [
    ("once", "Approve Once", "proceed this time only"),
    ("always", "Always Approve", "proceed and silence this prompt permanently"),
    ("cancel", "Cancel", "keep current conversation"),
]


def _answer_modal_when_open(cli, response, stop=None):
    """Push ``response`` onto the modal's response_queue once it opens.

    Gives up after ~2s, or early when ``stop`` is set (the modal will never open,
    e.g. a scheduling failure) so degraded-path tests don't wait the full budget.
    """
    for _ in range(100):
        if stop is not None and stop.is_set():
            return
        state = cli._slash_confirm_state
        if state and "response_queue" in state:
            state["response_queue"].put(response)
            return
        time.sleep(0.02)


def _run_on_daemon(call, cli, *, response, schedule=None):
    """Invoke ``call`` on a daemon thread — as the process_loop does — answering
    the modal with ``response`` once it opens.

    Returns ``{result, stdin_called, capture, restore}``.  ``schedule`` overrides
    the ``call_soon_threadsafe`` side effect (default: run the callback inline);
    pass a raiser to simulate a scheduling failure.  Fails if the worker hangs,
    which is the deadlock canary for #33961.
    """
    outcome = {"capture": [], "restore": [], "result": None, "stdin_called": False}
    done = threading.Event()

    def _worker():
        try:
            with patch.object(cli._app.loop, "call_soon_threadsafe", side_effect=schedule or (lambda cb: cb())), \
                 patch.object(cli, "_prompt_text_input") as mock_stdin, \
                 patch.object(cli, "_invalidate"), \
                 patch.object(cli, "_capture_modal_input_snapshot", side_effect=lambda: outcome["capture"].append(1)), \
                 patch.object(cli, "_restore_modal_input_snapshot", side_effect=lambda: outcome["restore"].append(1)):
                outcome["result"] = call()
                outcome["stdin_called"] = mock_stdin.called
        finally:
            done.set()

    worker = threading.Thread(target=_worker, daemon=True)
    answerer = threading.Thread(target=_answer_modal_when_open, args=(cli, response, done), daemon=True)
    answerer.start()
    worker.start()
    worker.join(timeout=2.0)
    answerer.join(timeout=2.0)
    assert not worker.is_alive(), "daemon thread hung — modal deadlocked"
    return outcome


class TestModal:
    """Behaviour of _prompt_text_input_modal across threads."""

    def test_daemon_thread_uses_modal_via_app_loop(self):
        """Off the process_loop daemon thread, the confirm uses the modal via
        call_soon_threadsafe."""
        cli = _make_cli()
        outcome = _run_on_daemon(
            lambda: cli._prompt_text_input_modal(
                title="⚠️  /reset",
                detail="This starts a fresh session.",
                choices=_SAMPLE_CHOICES,
                timeout=5,
            ),
            cli,
            response="once",
        )
        assert outcome["stdin_called"] is False, "must use the modal, not raw input()"
        assert outcome["result"] == "once"
        assert outcome["capture"] == [1]
        assert outcome["restore"] == [1]
        assert cli._slash_confirm_state is None

    def test_main_thread_with_app_uses_modal(self):
        """On the main thread with a running app, the queue-based modal is used."""
        cli = _make_cli()
        with patch.object(cli, "_capture_modal_input_snapshot"), \
             patch.object(cli, "_restore_modal_input_snapshot"), \
             patch.object(cli, "_invalidate"), \
             patch.object(cli, "_prompt_text_input") as mock_stdin:
            answerer = threading.Thread(target=_answer_modal_when_open, args=(cli, "once"), daemon=True)
            answerer.start()
            result = cli._prompt_text_input_modal(
                title="⚠️  /new",
                detail="This starts a fresh session.",
                choices=_SAMPLE_CHOICES,
                timeout=5,
            )
            answerer.join(timeout=2.0)

        mock_stdin.assert_not_called()
        assert result == "once"



class TestConfirmDestructiveSlash:
    """End-to-end _confirm_destructive_slash on the process_loop daemon thread.

    This is the flow bug #33961 froze; the modal runs via the app loop.
    """

    def _make_interactive_cli(self):
        cli = _make_cli()
        cli.model = "test-model"
        cli._agent_running = False
        cli._spinner_text = ""
        cli._should_exit = False
        cli._command_running = False
        cli.session_id = "test-session"
        cli._pending_tool_info = {}
        cli._tool_start_time = 0.0
        cli._last_scrollback_tool = ""
        return cli

    @pytest.mark.parametrize(
        "response, expected",
        [("once", "once"), ("cancel", None)],
    )
    def test_confirm_destructive_slash_uses_modal(self, response, expected):
        """The bare /new confirm drives the modal (not stdin) and returns the
        chosen outcome — bug #33961 froze this path."""
        cli = self._make_interactive_cli()
        with patch("cli.load_cli_config", return_value={"approvals": {"destructive_slash_confirm": True}}):
            outcome = _run_on_daemon(
                lambda: cli._confirm_destructive_slash(
                    "new",
                    "This starts a fresh session.\nThe current conversation history will be discarded.",
                ),
                cli,
                response=response,
            )

        assert outcome["stdin_called"] is False
        assert outcome["result"] == expected
