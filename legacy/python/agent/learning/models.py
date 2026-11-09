from __future__ import annotations

import hashlib
from datetime import datetime
from enum import Enum
from typing import Literal

from pydantic import BaseModel, ConfigDict

JobStatus = Literal["pending", "running", "done", "failed"]


class ReviewTrigger(str, Enum):
    ITERATIONS = "iterations"
    SESSION_END = "session_end"
    SESSION_RESET = "session_reset"
    PRE_COMPRESS = "pre_compress"
    IDLE = "idle"
    SHUTDOWN = "shutdown"


class ReviewOutcome(str, Enum):
    DONE = "done"
    SKIPPED = "skipped"  # review disabled/unsupported: nothing to retry
    INTERRUPTED = "interrupted"  # a live turn or teardown cancelled it: retry, attempts unchanged
    FAILED = "failed"


class ReviewJob(BaseModel):
    """A unit of review work over messages [start_msg_idx, end_msg_idx) of a session."""

    model_config = ConfigDict(frozen=True)

    job_id: str
    session_id: str
    trigger: ReviewTrigger
    start_msg_idx: int
    end_msg_idx: int
    transcript_digest: str
    attempts: int = 0
    created_at: datetime
    status: JobStatus = "pending"
    not_before: datetime | None = None
    last_error: str | None = None


def digest_range(session_id: str, start: int, end: int, texts: list[str]) -> str:
    """Stable digest of a message range; identical ranges dedupe in the queue."""
    h = hashlib.sha256(f"{session_id}:{start}:{end}".encode())
    for t in texts:
        h.update(b"\0")
        h.update(t.encode())
    return h.hexdigest()
