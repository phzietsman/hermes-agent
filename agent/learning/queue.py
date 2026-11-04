from __future__ import annotations

import sqlite3
import threading
from datetime import datetime
from typing import Protocol

from .models import JobStatus, ReviewJob, ReviewTrigger


class ReviewQueue(Protocol):
    def enqueue(self, job: ReviewJob) -> bool:
        """Insert job. False (no-op) if the same session range was already queued."""
        ...

    def claim(self, now: datetime) -> ReviewJob | None:
        """Atomically move the oldest runnable pending job to running."""
        ...

    def complete(self, job_id: str) -> None: ...

    def release(self, job_id: str) -> None:
        """Return a running job to pending with attempts unchanged (interrupted)."""
        ...

    def fail(self, job_id: str, error: str, retry_at: datetime | None) -> None:
        """Count an attempt. retry_at=None is terminal (status=failed)."""
        ...


_SCHEMA = """
CREATE TABLE IF NOT EXISTS review_jobs (
    job_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    trigger TEXT NOT NULL,
    start_msg_idx INTEGER NOT NULL,
    end_msg_idx INTEGER NOT NULL,
    transcript_digest TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    status TEXT NOT NULL,
    not_before TEXT,
    last_error TEXT,
    UNIQUE (session_id, transcript_digest)
)
"""

_COLS = (
    "job_id, session_id, trigger, start_msg_idx, end_msg_idx, transcript_digest, "
    "attempts, created_at, status, not_before, last_error"
)


def _row_to_job(row: tuple[object, ...]) -> ReviewJob:
    (job_id, session_id, trigger, start, end, digest, attempts, created, status, nb, err) = row
    status_s: JobStatus = status  # type: ignore[assignment]  # CHECKed by writers; Pydantic revalidates
    return ReviewJob(
        job_id=str(job_id),
        session_id=str(session_id),
        trigger=ReviewTrigger(str(trigger)),
        start_msg_idx=int(start),  # type: ignore[call-overload]  # sqlite INTEGER column
        end_msg_idx=int(end),  # type: ignore[call-overload]  # sqlite INTEGER column
        transcript_digest=str(digest),
        attempts=int(attempts),  # type: ignore[call-overload]  # sqlite INTEGER column
        created_at=datetime.fromisoformat(str(created)),
        status=status_s,
        not_before=datetime.fromisoformat(str(nb)) if nb else None,
        last_error=str(err) if err else None,
    )


class SqliteReviewQueue:
    """Durable queue in a sqlite database (state.db). Thread-safe via one lock."""

    def __init__(self, conn: sqlite3.Connection) -> None:
        self._conn = conn
        self._lock = threading.Lock()
        with self._lock, self._conn:
            self._conn.execute(_SCHEMA)
            # A crash mid-review leaves 'running' rows; they are retried, attempts unchanged.
            self._conn.execute("UPDATE review_jobs SET status='pending' WHERE status='running'")

    def enqueue(self, job: ReviewJob) -> bool:
        with self._lock, self._conn:
            cur = self._conn.execute(
                f"INSERT OR IGNORE INTO review_jobs ({_COLS}) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                (
                    job.job_id,
                    job.session_id,
                    job.trigger.value,
                    job.start_msg_idx,
                    job.end_msg_idx,
                    job.transcript_digest,
                    job.attempts,
                    job.created_at.isoformat(),
                    job.status,
                    job.not_before.isoformat() if job.not_before else None,
                    job.last_error,
                ),
            )
            return cur.rowcount == 1

    def claim(self, now: datetime) -> ReviewJob | None:
        with self._lock, self._conn:
            row = self._conn.execute(
                f"SELECT {_COLS} FROM review_jobs WHERE status='pending' "
                "AND (not_before IS NULL OR not_before <= ?) ORDER BY created_at, rowid LIMIT 1",
                (now.isoformat(),),
            ).fetchone()
            if row is None:
                return None
            self._conn.execute("UPDATE review_jobs SET status='running' WHERE job_id=?", (row[0],))
            return _row_to_job(row).model_copy(update={"status": "running"})

    def complete(self, job_id: str) -> None:
        with self._lock, self._conn:
            self._conn.execute("UPDATE review_jobs SET status='done', last_error=NULL WHERE job_id=?", (job_id,))

    def release(self, job_id: str) -> None:
        with self._lock, self._conn:
            self._conn.execute("UPDATE review_jobs SET status='pending' WHERE job_id=?", (job_id,))

    def fail(self, job_id: str, error: str, retry_at: datetime | None) -> None:
        status = "failed" if retry_at is None else "pending"
        with self._lock, self._conn:
            self._conn.execute(
                "UPDATE review_jobs SET attempts=attempts+1, status=?, not_before=?, last_error=? WHERE job_id=?",
                (status, retry_at.isoformat() if retry_at else None, error, job_id),
            )
