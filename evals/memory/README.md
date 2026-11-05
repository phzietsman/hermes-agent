# evals/memory

Typed, seeded memory eval harness. Scenarios 1, 2, 3, 6 are implemented against a `MemoryArm`
Protocol; 7 (resilience, `resilience.py`) runs offline against the real provider. Live (real agent, Jev judge, Sandbox
credentials in the env): 4 preference following (`preference_scenario.py`), 5 procedural reuse
(`procedural_scenario.py`, `proc_arm.py`), 8 latency (`python -m evals.memory.regression`). The SAM arm is
`live_sam.py` (`--arm loopfix_sam`); the loop-fix-only comparison runs from a worktree at the commit before the
builtin memory was removed (`b7e464fc1d`). Results and caveats: `DECISIONS.md` D25, D41-D46 (marked `requires_live`).

`baseline.json` is produced by `DictArm` (`harness_only: true`): it validates the harness only.
Live results (real agent + Jev judge): `baseline.live.json` (main) and `loopfix.json` (this branch);
see `DECISIONS.md` D25 for the design, numbers and caveats.

    python -m evals.memory.live_run --arm loopfix --checkout . --python <test-env python> --seeds 1,2,3 --out loopfix.json

    uv run python -m evals.memory.run --arm baseline --out evals/memory/baseline.json
