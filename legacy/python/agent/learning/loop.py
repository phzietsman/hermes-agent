from __future__ import annotations

import logging
import threading
from collections.abc import Callable, Mapping, Sequence
from typing import Any, Protocol

# agent.memory_provider is untyped (tuple without params); the signatures used here are simple.
from agent.memory_provider import ctx_bound, spawn_context_thread  # pyright: ignore[reportUnknownVariableType]

from .models import ReviewJob, ReviewOutcome, ReviewTrigger
from .queue import ReviewQueue
from .runner import ReviewExecutor, ReviewInterrupted, ReviewRunner
from .scheduler import ReviewScheduler, ReviewSettings

logger = logging.getLogger(__name__)

Message = Mapping[str, Any]

# A review fork can legitimately run for minutes; past this the job is retried as a failure.
REVIEW_WAIT_TIMEOUT_S = 1800.0


class ReviewHost(Protocol):
    """The agent side of the seam: spawn a provider-bound review fork over a transcript."""

    def spawn_review(self, messages: list[dict[str, Any]], on_outcome: Callable[[ReviewOutcome], None]) -> None: ...

    def load_messages(self, session_id: str) -> list[dict[str, Any]]:
        """Persisted transcript, used for jobs that outlived the process that enqueued them."""
        ...


def message_texts(messages: Sequence[Message]) -> list[str]:
    """Cheap per-message text for digesting; tool-call payloads and structured content collapse to repr."""
    return [str(m.get("content") or m.get("tool_calls") or "") for m in messages]


class HostExecutor:
    """Runs a job through the host and blocks until the fork reports its outcome."""

    def __init__(self, host: ReviewHost, snapshots: dict[str, list[dict[str, Any]]], wait_s: float) -> None:
        self._host = host
        self._snapshots = snapshots
        self._wait_s = wait_s

    def execute(self, job: ReviewJob) -> None:
        messages = self._snapshots.get(job.session_id) or self._host.load_messages(job.session_id)
        if not messages:
            return  # transcript gone (session deleted); nothing to review
        box: list[ReviewOutcome] = []
        done = threading.Event()

        def _on_outcome(outcome: ReviewOutcome) -> None:
            box.append(outcome)
            done.set()

        self._host.spawn_review(messages, _on_outcome)
        if not done.wait(self._wait_s):
            raise TimeoutError(f"review {job.job_id} produced no outcome in {self._wait_s:.0f}s")
        outcome = box[0]
        if outcome is ReviewOutcome.INTERRUPTED:
            raise ReviewInterrupted
        if outcome is ReviewOutcome.FAILED:
            raise RuntimeError("review fork failed")


class LearningLoop:
    """One per agent: scheduler + durable queue + background drain.

    Enqueueing is cheap and may happen mid-turn; reviews only start via ``kick()`` after the
    user's response is delivered, so a review never competes with the live task.
    """

    def __init__(
        self,
        queue: ReviewQueue,
        settings: ReviewSettings,
        host: ReviewHost,
        executor: ReviewExecutor | None = None,
        wait_s: float = REVIEW_WAIT_TIMEOUT_S,
    ) -> None:
        self._settings = settings
        self._snapshots: dict[str, list[dict[str, Any]]] = {}
        self._scheduler = ReviewScheduler(queue, settings)
        self._runner = ReviewRunner(queue, executor or HostExecutor(host, self._snapshots, wait_s), settings)
        self._lock = threading.Lock()
        self._worker: threading.Thread | None = None
        self._idle_timer: threading.Timer | None = None
        self._session_id = ""
        self._closed = False

    def _remember(self, session_id: str, messages: Sequence[Message]) -> None:
        self._snapshots[session_id] = [dict(m) for m in messages]

    def on_tool_iteration(self, session_id: str, messages: Sequence[Message]) -> bool:
        self._session_id = session_id
        if self._scheduler.on_tool_iteration(session_id, lambda: message_texts(messages)):
            self._remember(session_id, messages)
            return True
        return False

    def note_agent_wrote(self) -> None:
        self._scheduler.note_agent_wrote()

    def on_boundary(self, session_id: str, trigger: ReviewTrigger, messages: Sequence[Message]) -> bool:
        queued = self._scheduler.on_boundary(session_id, trigger, message_texts(messages))
        if queued:
            self._remember(session_id, messages)
            self.kick()
        return queued

    def on_turn_end(self, session_id: str, messages: Sequence[Message]) -> None:
        """Post-delivery: arm the idle timer and start draining anything queued."""
        self._session_id = session_id
        self._scheduler.on_turn_end()
        self._snapshots.setdefault(session_id, [dict(m) for m in messages])
        self._arm_idle(session_id, messages)
        self.kick()

    def _arm_idle(self, session_id: str, messages: Sequence[Message]) -> None:
        idle = self._settings.idle_seconds
        if idle <= 0 or self._closed:
            return
        snapshot = [dict(m) for m in messages]

        def _fire() -> None:
            if self._scheduler.check_idle(session_id, message_texts(snapshot)):
                self._snapshots[session_id] = snapshot
                self.kick()

        with self._lock:
            if self._idle_timer is not None:
                self._idle_timer.cancel()
            timer = threading.Timer(idle, ctx_bound(_fire))
            timer.daemon = True
            self._idle_timer = timer
            timer.start()

    def kick(self) -> None:
        """Start the drain thread if idle. Never blocks the caller."""
        with self._lock:
            if self._closed or (self._worker is not None and self._worker.is_alive()):
                return
            self._worker = spawn_context_thread(self._drain, name="review-drain")
            self._worker.start()

    def _drain(self) -> None:
        try:
            while self._runner.run_next() is not None:
                pass
        except Exception:  # health: allow BLE001 -- drain thread must not die silently; traceback logged
            logger.exception("review drain crashed")

    def close(self) -> None:
        with self._lock:
            self._closed = True
            if self._idle_timer is not None:
                self._idle_timer.cancel()
