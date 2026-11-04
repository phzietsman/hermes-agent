from __future__ import annotations

import uuid
from collections.abc import Callable
from datetime import datetime, timezone

from pydantic import BaseModel, Field

from .models import ReviewJob, ReviewTrigger, digest_range
from .queue import ReviewQueue


class ReviewSettings(BaseModel):
    iteration_interval: int = Field(10, ge=0)  # 0 disables the iteration trigger
    idle_seconds: int = Field(900, ge=0)  # 0 disables the idle trigger
    max_attempts: int = Field(5, ge=1)
    backoff_base_s: float = Field(30.0, gt=0)
    review_on_boundary: bool = True


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


class ReviewScheduler:
    """Decides when a review is due and enqueues it (idempotently).

    Counts tool iterations, not user turns, so one tool-heavy turn can still trigger.
    The caller supplies the message range and texts; the scheduler never reads history.
    """

    def __init__(
        self,
        queue: ReviewQueue,
        settings: ReviewSettings,
        clock: Callable[[], datetime] = _utcnow,
    ) -> None:
        self._queue = queue
        self._settings = settings
        self._clock = clock
        self._iters = 0
        self._reviewed_upto = 0
        self._last_activity = clock()

    def _enqueue(self, session_id: str, trigger: ReviewTrigger, end: int, texts: list[str]) -> bool:
        start = self._reviewed_upto
        if end <= start:
            return False
        digest = digest_range(session_id, start, end, texts[start:end])
        job = ReviewJob(
            job_id=uuid.uuid4().hex,
            session_id=session_id,
            trigger=trigger,
            start_msg_idx=start,
            end_msg_idx=end,
            transcript_digest=digest,
            created_at=self._clock(),
        )
        # Advance even on a duplicate: that range is already queued or reviewed.
        self._reviewed_upto = end
        self._iters = 0
        return self._queue.enqueue(job)

    def on_tool_iteration(self, session_id: str, texts: list[str]) -> bool:
        self._last_activity = self._clock()
        self._iters += 1
        n = self._settings.iteration_interval
        if n > 0 and self._iters >= n:
            return self._enqueue(session_id, ReviewTrigger.ITERATIONS, len(texts), texts)
        return False

    def on_turn_end(self) -> None:
        self._last_activity = self._clock()

    def on_boundary(self, session_id: str, trigger: ReviewTrigger, texts: list[str]) -> bool:
        if not self._settings.review_on_boundary:
            return False
        return self._enqueue(session_id, trigger, len(texts), texts)

    def check_idle(self, session_id: str, texts: list[str]) -> bool:
        idle = self._settings.idle_seconds
        if idle <= 0:
            return False
        if (self._clock() - self._last_activity).total_seconds() < idle:
            return False
        fired = self._enqueue(session_id, ReviewTrigger.IDLE, len(texts), texts)
        self._last_activity = self._clock()  # one idle review per quiet period
        return fired
