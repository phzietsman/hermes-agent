# evals/memory

Typed, seeded memory eval harness. Scenarios 1, 2, 3, 6 are implemented against a `MemoryArm`
Protocol; 7 (resilience, `resilience.py`) runs offline against the real provider; 4, 5, 8 need a live agent arm (marked `requires_live`).

`baseline.json` is produced by `DictArm` (`harness_only: true`): it validates the harness only.
Live results (real agent + Jev judge): `baseline.live.json` (main) and `loopfix.json` (this branch);
see `DECISIONS.md` D25 for the design, numbers and caveats.

    python -m evals.memory.live_run --arm loopfix --checkout . --python <test-env python> --seeds 1,2,3 --out loopfix.json

    uv run python -m evals.memory.run --arm baseline --out evals/memory/baseline.json
