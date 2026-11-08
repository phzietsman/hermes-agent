"""Golden fixtures for the Go cron schedule parser and next-run computation, from the real cron.jobs.

usage: python scripts/port/gen_cron_golden.py internal/cron/testdata
"""
import json
import pathlib
import sys
from datetime import datetime, timezone
from zoneinfo import ZoneInfo

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

import cron.jobs as jobs  # noqa: E402

TZ = ZoneInfo("America/Toronto")
NOW = datetime(2026, 6, 1, 12, 0, 0, tzinfo=TZ)
jobs._hermes_now = lambda: NOW
jobs.get_timezone = lambda: TZ

SCHEDULES = [
    "30m", "every 30m", "every 2h", "every hour", "every day", "1d", "2 hours", "hour", "every 90 minutes", "45 min",
    "every monday 9am", "every Monday at 9:30am", "weekdays at 9am", "every weekday 17:00", "every day at 9am", "daily 7",
    "every mon, wed and fri at 6pm", "every weekend noon", "every sat midnight", "every sunday 12am", "every tuesday 12pm",
    "0 9 * * *", "*/15 * * * *", "0 9 * * MON-FRI", "30 2 1 JAN-MAR *", "0 0 29 2 *", "5 4 * * sun", "0 9 1,15 * *",
    "in 30m", "in 2h", "in 1d", "2026-06-02T09:00:00", "2026-06-02T09:00:00Z", "2026-06-02T09:00:00+02:00", "2026-06-02",
    # invalid
    "", "tomorrow", "every", "every blah", "99 * * * *", "0 25 * * *", "in", "in banana", "2026-13-45", "every monday", "0 9 * *",
    "every 0m", "0m",
]

NEXT = [
    ("0 9 * * *", "2026-06-01T12:00:00-04:00"), ("0 9 * * *", "2026-06-01T14:00:00-04:00"), ("*/15 * * * *", "2026-06-01T12:07:00-04:00"),
    ("0 9 * * MON-FRI", "2026-06-05T14:00:00-04:00"), ("0 9 * * MON-FRI", "2026-06-06T14:00:00-04:00"),
    ("30 2 * * *", "2026-03-07T12:00:00-05:00"),   # next day is the spring-forward gap (02:30 does not exist)
    ("30 1 * * *", "2026-11-01T01:45:00-04:00"),   # fall-back: 01:30 happens twice
    ("30 1 * * *", "2026-11-01T01:45:00-05:00"),
    ("0 9 29 2 *", "2026-06-01T12:00:00-04:00"),    # next 29 Feb
    ("0 0 31 * *", "2026-04-15T12:00:00-04:00"),    # skips 30-day months
    ("0 9 1,15 * *", "2026-06-15T14:00:00-04:00"),
    ("0 12 15 * 1", "2026-06-01T12:00:00-04:00"),   # day-of-month OR day-of-week
    ("5 4 * * sun", "2026-06-01T12:00:00-04:00"),
    ("*/20 9-17 * * 1-5", "2026-06-05T17:50:00-04:00"),
    ("0 0 * * 7", "2026-06-01T12:00:00-04:00"),     # 7 is Sunday
    ("59 23 31 12 *", "2026-12-31T23:59:00-05:00"),
    ("0 9 * * *", "2026-06-01T12:00:00+00:00"),     # base stored in UTC: still the configured wall clock
]


def parse(s):
    try:
        r = jobs.parse_schedule(s)
    except ValueError as e:
        return {"error": str(e)}
    return {k: r[k] for k in ("kind", "expr", "minutes", "run_at", "display") if k in r}


def main(outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    parsed = [{"in": s, "out": parse(s)} for s in SCHEDULES]
    nxt = []
    for expr, base in NEXT:
        sched = {"kind": "cron", "expr": expr}
        nxt.append({"expr": expr, "base": base, "next": jobs.compute_next_run(sched, base)})
    intervals = [{"minutes": m, "base": "2026-06-01T12:00:00-04:00", "next": jobs.compute_next_run({"kind": "interval", "minutes": m}, "2026-06-01T12:00:00-04:00")}
                 for m in (1, 30, 90, 1440)]
    grace = [{"schedule": s, "grace": jobs._compute_grace_seconds(jobs.parse_schedule(s))} for s in ("1m", "30m", "2h", "1d", "0 9 * * *", "*/5 * * * *", "0 9 1 * *")]
    (out / "cron.json").write_text(json.dumps({"now": NOW.isoformat(), "tz": "America/Toronto", "parse": parsed, "next": nxt, "intervals": intervals, "grace": grace},
                                              indent=1, ensure_ascii=False, sort_keys=True) + "\n")
    print(len(parsed), len(nxt))


if __name__ == "__main__":
    main(sys.argv[1])
