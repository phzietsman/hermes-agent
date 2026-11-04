from __future__ import annotations

import sqlite3
from datetime import datetime, timedelta, timezone

import pytest

from agent.learning.models import ReviewJob, ReviewTrigger
from agent.learning.queue import SqliteReviewQueue
from agent.learning.runner import ReviewInterrupted, ReviewRunner
from agent.learning.scheduler import ReviewScheduler, ReviewSettings

T0 = datetime(2026, 1, 1, tzinfo=timezone.utc)


class Clock:
    def __init__(self) -> None:
        self.now = T0

    def __call__(self) -> datetime:
        return self.now

    def advance(self, seconds: float) -> None:
        self.now += timedelta(seconds=seconds)


class FakeExecutor:
    def __init__(self, outcomes: list[Exception | None]) -> None:
        self.outcomes = outcomes
        self.calls: list[ReviewJob] = []

    def execute(self, job: ReviewJob) -> None:
        self.calls.append(job)
        outcome = self.outcomes.pop(0) if self.outcomes else None
        if outcome is not None:
            raise outcome


def make_queue() -> SqliteReviewQueue:
    return SqliteReviewQueue(sqlite3.connect(":memory:", check_same_thread=False))


def texts(n: int) -> list[str]:
    return [f"m{i}" for i in range(n)]


def drain(queue: SqliteReviewQueue) -> list[ReviewJob]:
    out: list[ReviewJob] = []
    while (j := queue.claim(T0 + timedelta(days=1))) is not None:
        out.append(j)
    return out


def test_iteration_trigger_fires_inside_one_tool_heavy_user_turn() -> None:
    q = make_queue()
    s = ReviewScheduler(q, ReviewSettings(iteration_interval=5), Clock())
    fired = [s.on_tool_iteration("s1", texts(i + 1)) for i in range(5)]
    assert fired == [False] * 4 + [True]  # no user turn boundary was crossed
    jobs = drain(q)
    assert len(jobs) == 1 and jobs[0].trigger is ReviewTrigger.ITERATIONS
    assert (jobs[0].start_msg_idx, jobs[0].end_msg_idx) == (0, 5)


def test_same_range_is_not_reviewed_twice() -> None:
    q = make_queue()
    a = ReviewScheduler(q, ReviewSettings(), Clock())
    b = ReviewScheduler(q, ReviewSettings(), Clock())  # e.g. a restarted process
    assert a.on_boundary("s1", ReviewTrigger.SESSION_END, texts(8)) is True
    assert b.on_boundary("s1", ReviewTrigger.SESSION_END, texts(8)) is False
    assert len(drain(q)) == 1


def test_boundaries_enqueue_and_can_be_disabled() -> None:
    q = make_queue()
    s = ReviewScheduler(q, ReviewSettings(), Clock())
    assert s.on_boundary("s1", ReviewTrigger.SESSION_RESET, texts(3))
    assert s.on_boundary("s1", ReviewTrigger.SHUTDOWN, texts(6))
    assert [j.trigger for j in drain(q)] == [ReviewTrigger.SESSION_RESET, ReviewTrigger.SHUTDOWN]
    off = ReviewScheduler(make_queue(), ReviewSettings(review_on_boundary=False), Clock())
    assert off.on_boundary("s1", ReviewTrigger.SESSION_END, texts(3)) is False


def test_idle_trigger_fires_once_per_quiet_period() -> None:
    clock = Clock()
    q = make_queue()
    s = ReviewScheduler(q, ReviewSettings(idle_seconds=60), clock)
    s.on_turn_end()
    clock.advance(30)
    assert s.check_idle("s1", texts(4)) is False
    clock.advance(40)
    assert s.check_idle("s1", texts(4)) is True
    clock.advance(500)
    assert s.check_idle("s1", texts(4)) is False  # nothing new to review


def test_interrupted_review_is_retried_without_consuming_attempts() -> None:
    q = make_queue()
    ReviewScheduler(q, ReviewSettings(), Clock()).on_boundary("s1", ReviewTrigger.SESSION_END, texts(4))
    ex = FakeExecutor([ReviewInterrupted(), None])
    runner = ReviewRunner(q, ex, ReviewSettings(), Clock())
    first = runner.run_next()
    second = runner.run_next()
    assert first is not None and second is not None and first.job_id == second.job_id
    assert second.attempts == 0
    assert runner.run_next() is None  # done


def test_failure_backs_off_then_fails_permanently_at_max_attempts() -> None:
    clock = Clock()
    q = make_queue()
    settings = ReviewSettings(max_attempts=3, backoff_base_s=10)
    ReviewScheduler(q, settings, clock).on_boundary("s1", ReviewTrigger.SESSION_END, texts(4))
    ex = FakeExecutor([RuntimeError("boom")] * 3)
    runner = ReviewRunner(q, ex, settings, clock)
    assert runner.run_next() is not None
    assert runner.run_next() is None  # backing off (10s)
    clock.advance(11)
    assert runner.run_next() is not None
    assert runner.run_next() is None
    clock.advance(21)  # 20s backoff
    assert runner.run_next() is not None  # third attempt -> permanent
    clock.advance(10_000)
    assert runner.run_next() is None
    row = q._conn.execute("SELECT status, attempts, last_error FROM review_jobs").fetchone()  # noqa: SLF001
    assert row == ("failed", 3, "boom")


def test_crash_recovery_requeues_running_jobs() -> None:
    conn = sqlite3.connect(":memory:", check_same_thread=False)
    q1 = SqliteReviewQueue(conn)
    ReviewScheduler(q1, ReviewSettings(), Clock()).on_boundary("s1", ReviewTrigger.SHUTDOWN, texts(2))
    assert q1.claim(T0 + timedelta(days=1)) is not None  # process dies while running
    q2 = SqliteReviewQueue(conn)
    job = q2.claim(T0 + timedelta(days=1))
    assert job is not None and job.attempts == 0


@pytest.mark.parametrize("n", [0])
def test_zero_interval_disables_iteration_trigger(n: int) -> None:
    s = ReviewScheduler(make_queue(), ReviewSettings(iteration_interval=n), Clock())
    assert not any(s.on_tool_iteration("s1", texts(i + 1)) for i in range(50))
