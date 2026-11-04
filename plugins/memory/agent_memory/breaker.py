from __future__ import annotations

import logging
import threading

logger = logging.getLogger(__name__)


class Breaker:
    """Consecutive-failure circuit breaker. Opens for the rest of the session; a success resets the count.
    An auth failure opens it immediately (retrying a bad key cannot help)."""

    def __init__(self, threshold: int) -> None:
        self._threshold = threshold
        self._failures = 0
        self._open = False
        self._warned = False
        self._lock = threading.Lock()

    @property
    def is_open(self) -> bool:
        return self._open

    def record_ok(self) -> None:
        with self._lock:
            self._failures = 0

    def record_failure(self, where: str, *, auth: bool = False) -> None:
        with self._lock:
            self._failures += 1
            if auth or self._failures >= self._threshold:
                self._open = True
            should_warn = self._open and not self._warned
            self._warned = self._warned or self._open
        if should_warn:  # once per session
            logger.warning("Agent Memory unavailable (%s): degraded mode, writes are queued for replay", where)
