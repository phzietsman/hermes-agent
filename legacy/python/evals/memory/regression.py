"""Scenario 8 (live part): recall/prefetch latency through the real provider against a real Sandbox.

    python -m evals.memory.regression --out evals/memory/regression.json

Needs AGENT_MEMORY_API_KEY / AGENT_MEMORY_ENDPOINT / AGENT_MEMORY_CONTEXT. Uses its own scopes and a temporary
HERMES_HOME. Reports the raw service recall, the blocking part of ``prefetch`` cold (no warm-up) and warm
(``queue_prefetch`` issued one model-turn earlier), because the user-visible cost is the blocking part.
"""

from __future__ import annotations

import argparse
import json
import os
import statistics
import tempfile
import time
import uuid
from pathlib import Path

from plugins.memory.agent_memory.client import SurrealMemoryAdapter
from plugins.memory.agent_memory.config import load_settings
from plugins.memory.agent_memory.provider import AgentMemoryProvider

TOPICS = ("staging cluster", "on-call channel", "release branch", "VPN profile", "billing database",
          "Jira project", "package registry", "build server", "laptop asset tag", "current sprint")


def stats(ms: list[float]) -> dict[str, float]:
    ordered = sorted(ms)
    p95 = ordered[min(len(ordered) - 1, int(round(0.95 * (len(ordered) - 1))))]
    return {"n": float(len(ms)), "p50": round(statistics.median(ordered), 1), "p95": round(p95, 1),
            "max": round(ordered[-1], 1)}


def _timed(fn: object) -> float:
    t0 = time.monotonic()
    fn()  # type: ignore[operator]
    return (time.monotonic() - t0) * 1000


def measure(n: int, warm_n: int, warm_gap_s: float) -> dict[str, object]:
    tag = uuid.uuid4().hex[:10]
    with tempfile.TemporaryDirectory(prefix="memreg-") as tmp:
        home = Path(tmp)
        (home / "agent-memory.json").write_text(json.dumps({
            "endpoint": os.environ["AGENT_MEMORY_ENDPOINT"], "context": os.environ["AGENT_MEMORY_CONTEXT"],
            "default_scope": f"user/reg-{tag}", "agent_scope": f"agent/reg-{tag}", "timeout_s": 30}), encoding="utf-8")
        settings, why = load_settings(home)
        if settings is None:
            raise RuntimeError(why)
        client = SurrealMemoryAdapter(settings)
        for scope in (settings.default_scope, settings.agent_scope):
            client.register_scope(scope)
        for i, topic in enumerate(TOPICS):
            client.remember(f"Our {topic} is item-{100 + i}.", scopes=settings.default_scope)
        raw = [_timed(lambda t=t: client.recall(f"What is our {t}?", k=settings.top_k)) for t in (TOPICS * 3)[:n]]
        client.close()
        provider = AgentMemoryProvider()
        provider.initialize(f"reg-{tag}", hermes_home=str(home))
        cold = [_timed(lambda t=t: provider.prefetch(f"What is our {t}, again?")) for t in (TOPICS * 3)[:n]]
        warm: list[float] = []
        for t in (TOPICS * 3)[:warm_n]:
            q = f"Remind me what our {t} is."
            provider.queue_prefetch(q)
            time.sleep(warm_gap_s)  # stands in for the model's own turn time
            warm.append(_timed(lambda q=q: provider.prefetch(q)))
        provider.shutdown()
    return {"raw_recall_ms": stats(raw), "prefetch_cold_ms": stats(cold), "prefetch_warm_ms": stats(warm),
            "prefetch_wait_s": settings.prefetch_wait_s, "gate_p95_ms": 1500}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--n", type=int, default=20)
    ap.add_argument("--warm-n", type=int, default=10)
    ap.add_argument("--warm-gap", type=float, default=10.0)
    ns = ap.parse_args()
    result = measure(ns.n, ns.warm_n, ns.warm_gap)
    ns.out.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
