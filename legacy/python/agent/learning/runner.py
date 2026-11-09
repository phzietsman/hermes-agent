from __future__ import annotations

import logging
from collections.abc import Callable
from datetime import datetime, timedelta, timezone
from typing import Protocol

from .models import ReviewJob
from .queue import ReviewQueue
from .scheduler import ReviewSettings

logger = logging.getLogger(__name__)

ORIGIN = "background_review"


class ReviewInterrupted(Exception):
    """The next user message cancelled the review; the job must not be consumed."""


class ReviewExecutor(Protocol):
    """Runs one job in a fork bound to the parent MemoryManager (writes reach the provider
    with metadata origin=ORIGIN). Raise ReviewInterrupted on cancellation."""

    def execute(self, job: ReviewJob) -> None: ...


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


class ReviewRunner:
    def __init__(
        self,
        queue: ReviewQueue,
        executor: ReviewExecutor,
        settings: ReviewSettings,
        clock: Callable[[], datetime] = _utcnow,
    ) -> None:
        self._queue = queue
        self._executor = executor
        self._settings = settings
        self._clock = clock

    def run_next(self) -> ReviewJob | None:
        """Claim and run one due job. Returns it (as claimed) or None if nothing is due."""
        job = self._queue.claim(self._clock())
        if job is None:
            return None
        try:
            self._executor.execute(job)
        except ReviewInterrupted:
            self._queue.release(job.job_id)
        except Exception as exc:  # health: allow BLE001 -- any executor failure must be retried, never lost; traceback logged
            logger.exception("review job %s attempt %d failed", job.job_id, job.attempts + 1)
            attempts = job.attempts + 1
            if attempts >= self._settings.max_attempts:
                logger.warning("review job %s failed permanently after %d attempts", job.job_id, attempts)
                self._queue.fail(job.job_id, str(exc), None)
            else:
                delay = self._settings.backoff_base_s * (2 ** job.attempts)
                self._queue.fail(job.job_id, str(exc), self._clock() + timedelta(seconds=delay))
        else:
            self._queue.complete(job.job_id)
        return job
